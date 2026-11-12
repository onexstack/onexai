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

package handler

import (
	"strconv"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/im"
	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
)

// buildIMEvent converts a Lark message event into the unified IM event consumed
// by the message-sync relay (ChatBiz.ProcessIMEvent). It returns nil when the
// event carries no message or no conversation ID.
func buildIMEvent(event *larkim.P2MessageReceiveV1) *im.Event {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	message := event.Event.Message
	chatID := ptr.From(message.ChatId)
	if chatID == "" {
		return nil
	}

	msgType := ptr.From(message.MessageType)
	rawContent := ptr.From(message.Content)
	text := imlark.ExtractMessageText(msgType, rawContent)

	var mentions []im.Mention
	if len(message.Mentions) > 0 {
		larkMentions := make([]imlark.Mention, 0, len(message.Mentions))
		for _, mm := range message.Mentions {
			openID, userID := "", ""
			if mm.Id != nil {
				openID = ptr.From(mm.Id.OpenId)
				userID = ptr.From(mm.Id.UserId)
			}
			larkMentions = append(larkMentions, imlark.Mention{Key: ptr.From(mm.Key), Name: ptr.From(mm.Name)})
			mentions = append(mentions, im.Mention{
				Key:    ptr.From(mm.Key),
				Name:   ptr.From(mm.Name),
				OpenID: openID,
				UserID: userID,
				IsBot:  imlark.IsBotMention(mm),
			})
		}
		text = imlark.ApplyMentions(text, larkMentions)
	}

	senderOpenID, senderUserID := "", ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderOpenID = ptr.From(event.Event.Sender.SenderId.OpenId)
		senderUserID = ptr.From(event.Event.Sender.SenderId.UserId)
	}

	convType := im.ConvTypeGroup
	if ptr.From(message.ChatType) == "p2p" {
		convType = im.ConvTypeP2P
	}

	return &im.Event{
		Type:   im.EventTypeMessage,
		ConvID: chatID,
		Message: &im.Message{
			ID:     ptr.From(message.MessageId),
			ConvID: chatID,
			// The identity is the user_id, never the open_id: an open_id is
			// per-application, so nothing configured outside the app could name
			// one. An empty user_id means the app cannot read user IDs (or the
			// sender is external) — the identity paths reject that rather than
			// falling back to a value that would silently split the identity in
			// two.
			SenderID:     senderUserID,
			Content:      im.MessageContent{Type: msgType, Text: text, Raw: rawContent},
			CreateTime:   parseLarkTimeMs(ptr.From(message.CreateTime)),
			Backend:      "lark",
			RawID:        ptr.From(message.MessageId),
			ChatType:     convType,
			SenderOpenID: senderOpenID,
			ParentID:     ptr.From(message.ParentId),
			Mentions:     mentions,
		},
	}
}

// parseLarkTimeMs converts Lark's millisecond-epoch string to time.Time. It
// falls back to time.Now() when the value is empty or malformed.
func parseLarkTimeMs(ms string) time.Time {
	if ms == "" {
		return time.Now()
	}
	v, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.UnixMilli(v)
}
