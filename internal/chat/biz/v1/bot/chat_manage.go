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

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// UpdateChatName renames a Lark group chat. The duty commands call it after a
// ticket's status or level changes so the conversation list keeps showing
// "[处理中][P3]title" (in progress, priority 3) rather than a stale name.
func (b *botBiz) UpdateChatName(ctx context.Context, chatID, name string) error {
	if name == "" {
		return fmt.Errorf("lark: refusing to set an empty chat name")
	}
	return b.updateChat(ctx, chatID, larkim.NewUpdateChatReqBodyBuilder().Name(name).Build())
}

// UpdateChatAvatar replaces a group chat's avatar. An empty imageKey is a
// caller bug: it would clear the avatar, so it is rejected rather than sent.
func (b *botBiz) UpdateChatAvatar(ctx context.Context, chatID, imageKey string) error {
	if imageKey == "" {
		return fmt.Errorf("lark: refusing to set an empty chat avatar")
	}
	return b.updateChat(ctx, chatID, larkim.NewUpdateChatReqBodyBuilder().Avatar(imageKey).Build())
}

// updateChat issues one PATCH to the chat API and turns a platform rejection
// into an error naming the code and message.
func (b *botBiz) updateChat(ctx context.Context, chatID string, body *larkim.UpdateChatReqBody) error {
	cli := b.lark()
	if cli == nil {
		return fmt.Errorf("lark: no SDK client configured")
	}
	req := larkim.NewUpdateChatReqBuilder().ChatId(chatID).Body(body).Build()
	resp, err := cli.Im.Chat.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to update chat %s: %w", chatID, err)
	}
	if !resp.Success() {
		return fmt.Errorf("update chat %s failed, error=%s", chatID, larkcore.Prettify(resp.CodeError))
	}
	return nil
}
