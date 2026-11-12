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
	"log/slog"
	"strconv"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// cardCreateTicketAction is the submit action of the /oncall module-picker card
// (template OncallFlatSelectCardID). It is a fourth callback prefix alongside
// nav:/act:/cmd:, and exists because the flow needs the card's *form fields* and
// has to patch the submitting card — neither of which cmd: can do.
const cardCreateTicketAction = "create_ticket"

// handleCreateTicketAction turns a submitted module-picker card into a ticket.
//
// The card carries the module, level and title as form values; the work is
// delegated to /oncall so the card flow and the typed command flow share one
// creation path. The command runs detached from the callback (which has a ~3s
// budget that group creation easily outlives) and reports its outcome by
// patching the submitted card.
//
// A direct message and a group go down this same path: the receipt replaces the
// submitted card in place, so it lands in whichever conversation the card was
// submitted from. A ticket group raised from inside an existing one is allowed —
// the nesting is deliberate, and refusing it is what used to make the group and
// the p2p flows disagree.
func (b *botBiz) handleCreateTicketAction(ctx context.Context, event *callback.CardActionTriggerEvent, action, sessionKey string) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, nil
	}

	form, err := parseCreateTicketForm(action, event.Event.Action)
	if err != nil {
		return cardToast("warning", err.Error()), nil
	}
	module, title, level := form.Module, form.Title, form.Level

	dr, err := b.BuildCommandContext(ctx, event)
	if err != nil {
		return nil, err
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = event.Event.Context.OpenMessageID
	}

	runtime.GoSafe(ctx, "lark.card.create_ticket", func() {
		bg := context.WithoutCancel(ctx)
		cctx := runtime.Inject(bg, dr)
		cmdText := fmt.Sprintf("/oncall --level %d %s %s", level, module, title)
		if err := b.handleCommandMessage(cctx, dr, cmdText, nil); err != nil {
			slog.WarnContext(cctx, "create_ticket card action failed",
				"module", module, "chat_type", form.ChatType, "error", err)
			if messageID != "" {
				card := larkcard.NewCard().Title("建单失败", "red").Markdown(err.Error()).Build()
				if perr := b.PatchCardMessage(bg, messageID, sessionKey, card); perr != nil {
					slog.WarnContext(cctx, "failed to render the create-ticket error card", "error", perr)
				}
			}
			return
		}
		if messageID != "" {
			// Patched by message ID, so the receipt shows up in whatever
			// conversation the card was submitted from — group or p2p alike.
			// The template's P2P in the name predates group support.
			if perr := b.PatchCard(bg, messageID, chat.SREP2PCompleteCreateTicketCard, map[string]any{"title": title}); perr != nil {
				slog.WarnContext(cctx, "failed to patch the create-ticket receipt card", "error", perr)
			}
		}
	})

	return cardToast("success", "正在创建工单…"), nil
}

// createTicketForm is the parsed payload of a submitted module-picker card.
type createTicketForm struct {
	Module   string
	Title    string
	Level    int64
	ChatType string
}

// parseCreateTicketForm reads the module, title, level and originating chat
// type out of a submitted card. It is separated from the handler so the parsing
// rules — which field wins, what the defaults are — are testable without a Lark
// event or a botBiz.
//
// The module comes from the form ("selected_module"), which is what the shared
// OncallFlatSelectCardID template writes. A card that instead carries the module
// in the button value ("create_ticket:<code>") is also accepted.
func parseCreateTicketForm(action string, act *callback.CallBackAction) (createTicketForm, error) {
	if act == nil {
		return createTicketForm{}, fmt.Errorf("请先选择值班模块")
	}
	module := formString(act.FormValue, "selected_module")
	if module == "" {
		module = strings.TrimPrefix(action, cardCreateTicketAction+":")
		if module == cardCreateTicketAction {
			module = ""
		}
	}
	if module == "" {
		return createTicketForm{}, fmt.Errorf("请先选择值班模块")
	}

	title := formString(act.FormValue, "selected_title")
	if strings.TrimSpace(title) == "" {
		return createTicketForm{}, fmt.Errorf("请填写工单标题")
	}

	level := int64(4)
	if raw := formString(act.FormValue, "selected_level"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 1 && n <= 4 {
			level = n
		}
	}

	chatType, _ := act.Value["chat_type"].(string)
	return createTicketForm{Module: module, Title: title, Level: level, ChatType: chatType}, nil
}

// formString reads a string field from a card form, tolerating the SDK's
// interface{} values.
func formString(form map[string]interface{}, key string) string {
	s, _ := form[key].(string)
	return s
}

// cardToast builds a toast-only card response.
func cardToast(typ, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: typ, Content: content}}
}
