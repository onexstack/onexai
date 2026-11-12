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
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// TestPodCardActionsAreNotCommands pins the routing that keeps a /pod card's
// own controls from being read as command names: a "pod:" body is answered with
// a pod card, not dispatched through renderCommandCard (which would run
// "pod:sel:..." as a command and report an unknown command).
func TestPodCardActionsAreNotCommands(t *testing.T) {
	b := &botBiz{}
	recv := &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_pod"}

	// No card event: this path renders a pod card, which needs neither the
	// operator nor the session the event would be resolved through.
	card := b.defaultCardNavHandler(context.Background(), nil, "nav:pod:sel:unknown-token:0", recv)
	if card == nil {
		t.Fatal("a pod card action produced no card")
	}
	// The expiry card is a /pod card too, so it carries no title bar either; what
	// identifies it is the line telling the operator to run the command again.
	if card.Header != nil {
		t.Errorf("header = %+v, want none on a /pod card", card.Header)
	}
	var text string
	for _, elem := range card.Elements {
		if md, ok := elem.(larkcard.CardMarkdown); ok {
			text += md.Content
		}
	}
	if !strings.Contains(text, "重新执行") {
		t.Errorf("card text = %q, want the expiry notice", text)
	}
}

// TestChatGroupLabel pins the group names the menu shows. The key stays ASCII
// (the group's buttons carry it, so renaming it would strand every card already
// sent); the name a user reads comes from the command catalog, which is the one
// place a group is declared — keeping a list here as well is how the card and
// the text help came to name the same command differently.
func TestChatGroupLabel(t *testing.T) {
	for _, g := range command.Groups() {
		if got := chatGroupLabel(g.Key); got != g.Label {
			t.Errorf("chatGroupLabel(%q) = %q, want %q", g.Key, got, g.Label)
		}
	}

	// A key from a card sent before its group was removed is shown unchanged
	// rather than blanked: the card is what the user is looking at.
	if got := chatGroupLabel("no-such-group"); got != "no-such-group" {
		t.Errorf("chatGroupLabel(unknown) = %q, want the key unchanged", got)
	}
}
