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

	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// ProcessBotMenu handles a bot custom-menu callback. The menu item's event key
// is treated as a slash command (e.g. "/help") and dispatched through the same
// cobra command tree as message commands. The reply goes to the operator's P2P
// conversation, since a bot menu carries no group context.
func (b *botBiz) ProcessBotMenu(ctx context.Context, event *larkapp.P2BotMenuV6) error {
	if event == nil || event.Event == nil || event.Event.EventKey == nil {
		return nil
	}
	eventKey := strings.TrimSpace(ptr.From(event.Event.EventKey))
	if eventKey == "" {
		return nil
	}

	var sender v1.Sender
	if event.Event.Operator != nil && event.Event.Operator.OperatorId != nil {
		sender = v1.NewSender(
			ptr.From(event.Event.Operator.OperatorId.UserId),
			ptr.From(event.Event.Operator.OperatorId.OpenId),
		)
	}

	// A bot menu always opens a direct conversation with the clicker, so the reply
	// is addressed to them — by the same fallback every other entry point uses,
	// since the operator may be outside this tenant and have no user_id here.
	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		IsSRE:        b.sr.IsSREUser(sender.Identity()),
		IsAdmin:      b.sr.IsAdminUser(sender.Identity()),
		Sender:       sender,
		ConvID:       sender.Address(),
		Receiver:     v1.DirectReceiver(sender, "", ""),
		LarkServices: b,
	}

	// Tolerate menu keys both with and without a leading "/".
	cmdText := eventKey
	if !strings.HasPrefix(cmdText, "/") {
		cmdText = "/" + cmdText
	}
	return b.handleCommandMessage(ctx, dr, cmdText, nil)
}
