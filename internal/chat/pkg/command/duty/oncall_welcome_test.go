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

package duty

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
)

// welcomeDuty answers the two lookups ticket creation makes with one scripted
// group and leaves the rest of the duty service to the embedded interface.
type welcomeDuty struct {
	dutystore.Service
	group dutystore.Group
}

func (d welcomeDuty) GetGroupInfo(context.Context, string) (dutystore.Group, bool, error) {
	return d.group, true, nil
}

func (d welcomeDuty) GetGroupMeta(context.Context, string) (dutystore.Group, bool, error) {
	return d.group, true, nil
}

// TestCreateTicketGroupSendsTheWelcome pins the one promise the duty-group row
// makes about its chat: the welcome message is posted to the new group. It is
// sent to the chat that was just created (not to the room the command came
// from), and it is sent on the card-action path too, which is why the test
// drives IsCardAction.
func TestCreateTicketGroupSendsTheWelcome(t *testing.T) {
	env := newTestEnv(t, nil, &model.SessionM{
		SessionID:   "oc_new",
		ChatID:      "oc_new",
		SessionType: model.SessionTypeTicketGroup,
	})
	env.dr.IsCardAction = true
	env.sr.Duty = welcomeDuty{group: dutystore.Group{
		ID:             "cc",
		Name:           "cc",
		WelcomeMessage: "**欢迎**加入值班群",
		Agents:         []string{"agent"},
		Watchers:       []*v1.Watcher{{UserId: "u-oncall", UserName: "Oncall"}},
	}}

	if err := createTicketForModule(context.Background(), env.sr, env.dr, nil, "cc", "磁盘告警", 4); err != nil {
		t.Fatalf("createTicketForModule() = %v", err)
	}

	if len(env.lark.markdowns) != 1 {
		t.Fatalf("markdown cards = %d, want exactly the welcome message", len(env.lark.markdowns))
	}
	got := env.lark.markdowns[0]
	if got.ChatID != "oc_new" {
		t.Errorf("welcome chat = %q, want the newly created chat", got.ChatID)
	}
	if got.Content != "**欢迎**加入值班群" {
		t.Errorf("welcome content = %q, want the configured Markdown sent verbatim", got.Content)
	}
}

// TestCreateTicketGroupSkipsAnEmptyWelcome pins the other half: a group with no
// welcome message must not send an empty card to the new chat.
func TestCreateTicketGroupSkipsAnEmptyWelcome(t *testing.T) {
	env := newTestEnv(t, nil, &model.SessionM{
		SessionID:   "oc_new",
		ChatID:      "oc_new",
		SessionType: model.SessionTypeTicketGroup,
	})
	env.dr.IsCardAction = true
	env.sr.Duty = welcomeDuty{group: dutystore.Group{
		ID:       "cc",
		Name:     "cc",
		Watchers: []*v1.Watcher{{UserId: "u-oncall", UserName: "Oncall"}},
	}}

	if err := createTicketForModule(context.Background(), env.sr, env.dr, nil, "cc", "磁盘告警", 4); err != nil {
		t.Fatalf("createTicketForModule() = %v", err)
	}
	if len(env.lark.markdowns) != 0 {
		t.Errorf("markdown cards = %d, want none for an empty welcome", len(env.lark.markdowns))
	}
}
