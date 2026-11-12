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

package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	larkv3 "github.com/larksuite/oapi-sdk-go/v3"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// memberNameCache is the cached member-name map for a single conversation.
type memberNameCache struct {
	names   map[string]string // member identity (user_id / app_id) → display name
	fetched time.Time
}

// memberNameTTL is how long a conversation's member names are cached before a
// refresh. Member lists change rarely, so this keeps the per-message sender
// name resolution from hitting the Lark API on every inbound message.
const memberNameTTL = 5 * time.Minute

// resolvedNameTTL is how long a resolved display name (from the contact or
// application API) is cached. These are tenant-wide and change very rarely.
const resolvedNameTTL = 30 * time.Minute

// resolvedName is a globally cached display name for a single member ID.
type resolvedName struct {
	name    string
	fetched time.Time
}

// Backend is the Lark IM client backed by the Lark SDK. It provides the
// message-sync operations used by the chat biz: send text, query history /
// conversations / members, and download image resources. Event reception is
// handled by the handler layer (bot.go), not here.
type Backend struct {
	client *larkv3.Client

	memberMu    sync.Mutex
	memberNames map[string]memberNameCache // convID → cached member names

	nameMu   sync.Mutex
	resolved map[string]resolvedName // member id → display name (cross-conversation)
}

// New creates a Lark backend wrapping the given SDK client. Pass nil to disable
// message sync (e.g. when Lark is not configured).
func New(client *larkv3.Client) *Backend {
	return &Backend{
		client:      client,
		memberNames: make(map[string]memberNameCache),
		resolved:    make(map[string]resolvedName),
	}
}

// Name returns the backend identifier.
func (b *Backend) Name() string { return "lark" }

// SendMessage sends a text message to the given conversation. Both group and P2P
// conversations are addressed by chat_id here.
func (b *Backend) SendMessage(ctx context.Context, req *im.SendMessageReq) (*im.Message, error) {
	if req == nil {
		return nil, fmt.Errorf("lark: nil request")
	}
	if b.client == nil {
		return nil, fmt.Errorf("lark: no SDK client configured")
	}

	raw, _ := json.Marshal(req.Content.Text)
	content := fmt.Sprintf(`{"text":%s}`, string(raw))
	larkReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(req.ConvID).
			MsgType(larkim.MsgTypeText).
			Content(content).
			Build()).
		Build()

	resp, err := b.client.Im.Message.Create(ctx, larkReq)
	if err != nil {
		return nil, fmt.Errorf("lark: send message: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark: send message: code=%d msg=%s", resp.Code, resp.Msg)
	}

	msgID := ""
	if resp.Data != nil {
		msgID = ptr.From(resp.Data.MessageId)
	}
	return &im.Message{
		ID:         msgID,
		ConvID:     req.ConvID,
		SenderName: "bot",
		Content:    im.MessageContent{Type: "text", Text: req.Content.Text},
	}, nil
}

// SendImage uploads image bytes and sends them to the given conversation.
//
// # Why the upload lives here
//
// Lark addresses an image by a key its own API issued: there is no way to send
// bytes, only a key obtained by uploading them first. That upload is two API
// calls that must agree on the chat, so it sits beside SendMessage rather than
// being split across callers — and it keeps the sender of a TUI message able to
// attach a picture without a second component knowing Lark's image protocol.
func (b *Backend) SendImage(ctx context.Context, convID string, data []byte) error {
	if b.client == nil {
		return fmt.Errorf("lark: no SDK client configured")
	}
	if len(data) == 0 {
		return fmt.Errorf("lark: no image data to send")
	}

	upReq := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType("message").
			Image(bytes.NewReader(data)).
			Build()).
		Build()
	upResp, err := b.client.Im.Image.Create(ctx, upReq)
	if err != nil {
		return fmt.Errorf("lark: upload image: %w", err)
	}
	if !upResp.Success() {
		return fmt.Errorf("lark: upload image: code=%d msg=%s", upResp.Code, upResp.Msg)
	}
	if upResp.Data == nil || upResp.Data.ImageKey == nil {
		return fmt.Errorf("lark: upload image returned no image_key")
	}

	content, err := (&larkim.MessageImage{ImageKey: *upResp.Data.ImageKey}).String()
	if err != nil {
		return fmt.Errorf("lark: build image message: %w", err)
	}
	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(convID).
			MsgType(larkim.MsgTypeImage).
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, msgReq)
	if err != nil {
		return fmt.Errorf("lark: send image: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark: send image: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// DissolveChat dissolves a group conversation.
//
// # This is irreversible, and it takes the history with it
//
// Lark's own documentation for this call says the group's chat history is not
// kept once it is dissolved. So it is not a way to close a conversation — the
// messages stop being reachable from the platform the moment it returns, and
// anything that was going to be kept had to be read out first. Every caller
// before the dissolution sweep in biz/v1/dissolve treats the archive as a
// precondition rather than as a follow-up.
//
// It requires the bot to own the group: either as its owner, or as its creator
// with the im:chat:operate_as_owner scope. A group the bot was merely added to
// cannot be dissolved this way, and the refusal comes back as a permission
// error rather than as a silent no-op.
func (b *Backend) DissolveChat(ctx context.Context, chatID string) error {
	if b.client == nil {
		return fmt.Errorf("lark: no SDK client configured")
	}
	if chatID == "" {
		return fmt.Errorf("lark: no chat id to dissolve")
	}

	req := larkim.NewDeleteChatReqBuilder().ChatId(chatID).Build()
	resp, err := b.client.Im.Chat.Delete(ctx, req)
	if err != nil {
		return fmt.Errorf("lark: dissolve chat %s: %w", chatID, err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark: dissolve chat %s: code=%d msg=%s", chatID, resp.Code, resp.Msg)
	}
	return nil
}

// GetHistory fetches the most recent messages for a conversation, newest-first,
// then reverses to chronological order (oldest-first) for the client.
func (b *Backend) GetHistory(ctx context.Context, convID string, limit int) ([]*im.Message, error) {
	if b.client == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	req := larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(convID).
		PageSize(limit).
		SortType("ByCreateTimeDesc").
		CardMsgContentType(larkcard.CardContentTypeUser).
		Build()

	resp, err := b.client.Im.Message.List(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark: list messages: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark: list messages: code=%d msg=%s", resp.Code, resp.Msg)
	}

	msgs := make([]*im.Message, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		msgType := ptr.From(item.MsgType)
		rawContent := ptr.From(item.Body.Content)
		text := ExtractMessageText(msgType, rawContent)
		if len(item.Mentions) > 0 {
			mentions := make([]Mention, 0, len(item.Mentions))
			for _, mm := range item.Mentions {
				mentions = append(mentions, Mention{Key: ptr.From(mm.Key), Name: ptr.From(mm.Name)})
			}
			text = ApplyMentions(text, mentions)
		}

		senderID, senderName := "", ""
		if item.Sender != nil {
			senderID = ptr.From(item.Sender.Id)
			senderName = ptr.From(item.Sender.SenderName)
			if senderName == "" {
				senderName = senderID
			}
		}

		createTime := time.Now()
		if ct := ptr.From(item.CreateTime); ct != "" {
			if ms, err := strconv.ParseInt(ct, 10, 64); err == nil {
				createTime = time.UnixMilli(ms)
			}
		}

		msgs = append(msgs, &im.Message{
			ID:         ptr.From(item.MessageId),
			ConvID:     convID,
			SenderID:   senderID,
			SenderName: senderName,
			Content:    im.MessageContent{Type: msgType, Text: text, Raw: rawContent},
			CreateTime: createTime,
			Backend:    "lark",
		})
	}

	// API returned newest-first; reverse to chronological (oldest-first).
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// ListConversation returns the group chats the bot belongs to. The userID
// argument is accepted for interface parity with other backends and ignored
// (Lark lists chats scoped to the app, not a user).
func (b *Backend) ListConversation(ctx context.Context, _ string) ([]*im.Conversation, error) {
	if b.client == nil {
		return nil, nil
	}

	var convs []*im.Conversation
	pageToken := ""
	for {
		// types=group lists group chats (normal groups + topic groups + external groups); the API does
		// not return p2p single chats here.
		req := larkim.NewListChatReqBuilder().Types("group").PageSize(50)
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		resp, err := b.client.Im.Chat.List(ctx, req.Build())
		if err != nil {
			return nil, fmt.Errorf("lark: list chats: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("lark: list chats: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil {
			break
		}
		for _, c := range resp.Data.Items {
			t := im.ConvTypeGroup
			if ptr.From(c.ChatMode) == "p2p" {
				t = im.ConvTypeP2P
			}
			convs = append(convs, &im.Conversation{
				ID:        ptr.From(c.ChatId),
				Type:      t,
				Name:      ptr.From(c.Name),
				AvatarURL: ptr.From(c.Avatar),
				Backend:   "lark",
			})
		}
		// Stop when Lark signals there are no more pages, or returns no token.
		if !ptr.From(resp.Data.HasMore) || ptr.From(resp.Data.PageToken) == "" {
			break
		}
		pageToken = *resp.Data.PageToken
	}
	return convs, nil
}

// GetChatMembers returns the members of a Lark group chat.
func (b *Backend) GetChatMembers(ctx context.Context, convID string) ([]im.Member, error) {
	if b.client == nil {
		return nil, nil
	}

	// member_id_type is user_id so the returned ids are identities, not
	// addresses: the caller keys names by the sender's SenderID, which is a
	// user_id, and an open_id would never match it.
	req := larkim.NewGetChatMembersReqBuilder().
		ChatId(convID).
		MemberIdType("user_id").
		PageSize(100).
		Build()
	resp, err := b.client.Im.ChatMembers.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark: get chat members: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark: get chat members: code=%d msg=%s", resp.Code, resp.Msg)
	}

	members := make([]im.Member, 0, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		id := ptr.From(it.MemberId)
		name := ptr.From(it.Name)
		if isRawMemberID(name) {
			name = "" // Lark sometimes returns a raw id (app_id) as the "name"
		}
		// External members / bots often have no name in the member list; resolve
		// them via the contact / application APIs so the TUI shows real names.
		if name == "" {
			name = b.displayName(ctx, id)
		}
		members = append(members, im.Member{UserID: id, Name: name})
	}
	return members, nil
}

// displayName resolves a member's display name for the given id. It is
// best-effort: it returns "" when the backend is disabled, the id is empty, or
// the contact/application API can't resolve it. Results are cached globally.
func (b *Backend) displayName(ctx context.Context, id string) string {
	if b.client == nil || id == "" {
		return ""
	}

	b.nameMu.Lock()
	if r, ok := b.resolved[id]; ok && time.Since(r.fetched) < resolvedNameTTL {
		b.nameMu.Unlock()
		return r.name
	}
	b.nameMu.Unlock()

	name := ""
	if strings.HasPrefix(id, "cli_") {
		name = b.resolveAppName(ctx, id)
	} else {
		name = b.resolveUserName(ctx, id)
	}

	b.nameMu.Lock()
	b.resolved[id] = resolvedName{name: name, fetched: time.Now()}
	b.nameMu.Unlock()
	return name
}

// resolveUserName resolves a user's display name via the contact API. userID is
// an identity (Lark user_id), the same value the member list is keyed by.
func (b *Backend) resolveUserName(ctx context.Context, userID string) string {
	return ResolveUserName(ctx, b.client, userID)
}

// ResolveUserName returns the Lark display name for a user id, or "" when it
// cannot be resolved — no client, an unknown user, a transport error, or an app
// without the contact scope. An empty answer is a normal outcome, not a
// failure, so callers treat it as "no name" and carry on.
//
// It is a function rather than a Backend method because the ticket-creation
// path holds only a Lark client, not a message-sync backend. Unlike the
// Backend's cache it does no caching: it is called once per user action, and a
// stale display name is worse than one extra request.
func ResolveUserName(ctx context.Context, client *larkv3.Client, userID string) string {
	if client == nil || userID == "" {
		return ""
	}
	req := larkcontact.NewGetUserReqBuilder().UserId(userID).UserIdType("user_id").Build()
	resp, err := client.Contact.User.Get(ctx, req)
	if err != nil || !resp.Success() || resp.Data == nil || resp.Data.User == nil {
		return ""
	}
	return ptr.From(resp.Data.User.Name)
}

// resolveAppName resolves a bot's display name via the application API.
func (b *Backend) resolveAppName(ctx context.Context, appID string) string {
	req := larkapp.NewGetApplicationReqBuilder().AppId(appID).Build()
	resp, err := b.client.Application.Application.Get(ctx, req)
	if err != nil || !resp.Success() || resp.Data == nil || resp.Data.App == nil {
		return ""
	}
	return ptr.From(resp.Data.App.AppName)
}

// isRawMemberID reports whether s is a Lark raw identifier (open_id, app_id,
// chat_id, etc.) rather than a human display name.
func isRawMemberID(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range []string{"ou_", "on_", "oc_", "om_", "od_", "cli_"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// MemberName resolves a member's display name in a conversation from the
// (cached) member list. It returns "" when the backend is disabled, the member
// is unknown, or Lark has no display name for it. The caller falls back to the
// raw ID in that case.
func (b *Backend) MemberName(ctx context.Context, convID, id string) string {
	if b.client == nil || convID == "" || id == "" {
		return ""
	}

	b.memberMu.Lock()
	cached, ok := b.memberNames[convID]
	b.memberMu.Unlock()
	if ok && time.Since(cached.fetched) < memberNameTTL {
		return cached.names[id]
	}

	members, err := b.GetChatMembers(ctx, convID)
	if err != nil {
		return ""
	}
	names := make(map[string]string, len(members))
	for _, m := range members {
		if m.Name != "" {
			names[m.UserID] = m.Name
		}
	}

	b.memberMu.Lock()
	b.memberNames[convID] = memberNameCache{names: names, fetched: time.Now()}
	b.memberMu.Unlock()

	if name := names[id]; name != "" {
		return name
	}
	// The sender id may not key the member list (e.g. a bot's app_id "cli_xxx"
	// alongside the member list's ids). Fall back to the contact/application
	// APIs.
	return b.displayName(ctx, id)
}

// DownloadImage fetches a message image resource and returns its raw bytes and
// content type.
func (b *Backend) DownloadImage(ctx context.Context, messageID, fileKey string) ([]byte, string, error) {
	if b.client == nil {
		return nil, "", fmt.Errorf("lark: no SDK client configured")
	}

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type("image").
		Build()
	resp, err := b.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("lark: download image: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("lark: download image returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("lark: read image: %w", err)
	}

	contentType := "application/octet-stream"
	if resp.FileName != "" {
		contentType = resp.FileName
	}
	return body, contentType, nil
}
