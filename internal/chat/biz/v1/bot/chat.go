// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// CreateChat creates a new Lark group chat for a ticket and persists a session
// record. It invites the primary on-caller and the operator.
//
// Both ids must be Lark user_ids: the request is typed, and a member that is an
// open_id fails the whole call with 99992360 "not a valid {user_id}" — the
// operator's open_id is exactly what broke /oncall. The check below turns that
// into a local error naming the offending value.
func (b *botBiz) CreateChat(
	ctx context.Context,
	senderID, title string,
	primaryOncaller *v1.Watcher,
) (string, error) {
	if err := requireUserID("operator", senderID); err != nil {
		return "", err
	}
	if err := requireUserID("primary on-caller", primaryOncaller.GetUserId()); err != nil {
		return "", err
	}

	userIDList := []string{primaryOncaller.UserId, senderID}
	req := larkim.NewCreateChatReqBuilder().
		UserIdType("user_id").
		SetBotManager(true).
		Body(larkim.NewCreateChatReqBodyBuilder().
			Name(title).
			Description("Ticket group, created automatically by OneX").
			UserIdList(userIDList).
			GroupMessageType("chat").
			ChatMode("group").
			ChatType("private").
			JoinMessageVisibility("all_members").
			LeaveMessageVisibility("all_members").
			MembershipApproval("no_approval_required").
			Build()).
		Build()

	resp, err := b.lark().Im.Chat.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create chat: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("create chat failed, logId=%s, error=%s", resp.RequestId(), larkcore.Prettify(resp.CodeError))
	}

	chatID := *resp.Data.ChatId
	sessionM := &model.SessionM{
		SessionID:   chatID,
		ChatID:      chatID,
		SessionType: model.SessionTypeTicketGroup,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := b.sr.Store.Session().Create(ctx, sessionM); err != nil {
		return chatID, fmt.Errorf("failed to persist session: %w", err)
	}

	return chatID, nil
}

// requireUserID rejects an identity that is not a Lark user_id. It only checks
// the namespace prefix — Lark's own marker, so there is nothing to guess — which
// is enough to catch the mistake that matters here: an open_id handed to an API
// that asked for user_id. Without it the caller learns nothing until the
// platform refuses the whole member list with 99992360.
func requireUserID(role, id string) error {
	switch {
	case id == "":
		return fmt.Errorf("no %s user_id: cannot create the ticket group", role)
	case strings.HasPrefix(id, "ou_"), strings.HasPrefix(id, "on_"):
		return fmt.Errorf("the %s id %q is a Lark open_id; creating a ticket group needs a user_id", role, id)
	}
	return nil
}

// InviteMembers adds the given member IDs (identified by memberIDType, e.g.
// "user_id" or "open_id") to a Lark group chat. When inviting bots by open_id,
// the caller must include the inviting user's own open_id in members, or Lark
// silently no-ops the invite.
func (b *botBiz) InviteMembers(ctx context.Context, chatID, memberIDType string, members []string) error {
	req := larkim.NewCreateChatMembersReqBuilder().
		ChatId(chatID).
		MemberIdType(memberIDType).
		Body(larkim.NewCreateChatMembersReqBodyBuilder().IdList(members).Build()).
		Build()

	resp, err := b.lark().Im.ChatMembers.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to invite to chat by %s: %w", memberIDType, err)
	}
	if !resp.Success() {
		return fmt.Errorf("invite to chat by %s failed, error=%s", memberIDType, larkcore.Prettify(resp.CodeError))
	}
	return nil
}

// GetRecentMessages fetches the most recent `limit` text messages from the chat
// and returns them as a formatted plain-text string (oldest-first).
func (b *botBiz) GetRecentMessages(ctx context.Context, recv *v1.Receiver, limit int) (string, error) {
	if recv.IsP2P() {
		return "", nil
	}

	var allMessages []*larkim.Message
	var pageToken string
	for len(allMessages) < limit {
		remaining := limit - len(allMessages)
		pageSize := remaining
		if pageSize > 50 {
			pageSize = 50
		}
		req := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(recv.Id).
			PageSize(pageSize).
			PageToken(pageToken).
			SortType("ByCreateTimeAsc").
			CardMsgContentType(larkcard.CardContentTypeUser).
			Build()
		resp, err := b.lark().Im.Message.List(ctx, req)
		if err != nil {
			return "", err
		}
		if !resp.Success() {
			return "", fmt.Errorf("API error: %s", resp.Msg)
		}
		allMessages = append(allMessages, resp.Data.Items...)
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil {
			break
		}
		pageToken = *resp.Data.PageToken
	}
	if len(allMessages) > limit {
		allMessages = allMessages[len(allMessages)-limit:]
	}

	var result strings.Builder
	for i, message := range allMessages {
		if message.Body == nil || ptr.From(message.Body.Content) == "" {
			continue
		}
		msgType := ptr.FromOr(message.MsgType, "")
		text := extractMsgWithMentions(msgType, *message.Body.Content, message.Mentions)
		if text == "" {
			continue
		}
		createTimeMs, err := strconv.ParseInt(ptr.FromOr(message.CreateTime, ""), 10, 64)
		if err != nil {
			continue
		}
		timestamp := time.Unix(createTimeMs/1000, 0).Format("2006-01-02 15:04:05")
		result.WriteString(fmt.Sprintf("[%s]: %s", timestamp, text))
		if i < len(allMessages)-1 {
			result.WriteString("\n")
		}
	}
	return result.String(), nil
}

// extractMsgWithMentions extracts message text and replaces @_user_N
// placeholders with real user names from the mentions array.
func extractMsgWithMentions(msgType, rawContent string, mentions []*larkim.Mention) string {
	mc := parseMsgContent(msgType, rawContent)
	if mc == nil || mc.Text == "" {
		return ""
	}
	text := mc.Text
	for i, m := range mentions {
		name := ptr.From(m.Name)
		if ptr.From(m.Key) != "" && name != "" {
			placeholder := fmt.Sprintf("@_user_%d", i+1)
			text = strings.ReplaceAll(text, placeholder, "@"+name)
		}
	}
	return text
}
