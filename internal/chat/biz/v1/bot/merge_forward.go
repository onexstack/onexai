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
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// formatMergeForward fetches the sub-messages of a merge_forward (合并转发) message
// message and flattens them into readable text for the AI pipeline. It is
// best-effort: on any failure it returns "" so the message is dropped rather
// than surfaced as raw JSON.
func (b *botBiz) formatMergeForward(ctx context.Context, rootMessageID string) string {
	if rootMessageID == "" {
		return ""
	}

	// The card's own JSON, for the same reason quote.go asks for it: a forward
	// can carry cards, and the default answer is the degraded rendering.
	req := larkim.NewGetMessageReqBuilder().
		MessageId(rootMessageID).
		CardMsgContentType(larkcard.CardContentTypeUser).
		Build()
	resp, err := b.lark().Im.Message.Get(ctx, req)
	if err != nil || !resp.Success() || resp.Data == nil || len(resp.Data.Items) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[合并转发消息]\n")
	for _, item := range resp.Data.Items {
		// Skip the root container message itself.
		if ptr.EqualTo(item.MessageId, rootMessageID) {
			continue
		}
		sender := "用户"
		if item.Sender != nil {
			if name := ptr.From(item.Sender.SenderName); name != "" {
				sender = name
			} else if id := ptr.From(item.Sender.Id); id != "" {
				sender = id
			}
		}

		msgType := ptr.From(item.MsgType)
		content := ""
		if item.Body != nil {
			content = ptr.From(item.Body.Content)
		}
		text := extractMsgWithMentions(msgType, content, item.Mentions)
		if text == "" {
			text = imlark.ExtractMessageText(msgType, content)
		}
		if text == "" {
			text = "[非文本消息]"
		}
		sb.WriteString(sender)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	sb.WriteString("[/合并转发消息]")
	return sb.String()
}
