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

package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// privateChat is the caller /sessions answers: a direct message, whose receiver
// type is what makes the command allowed at all.
func privateChat() *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		IsAdmin:  true,
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: "ou_me"},
		ConvID:   "ou_me",
	}
}

// TestSessionsRefusesInGroupChats pins the surface restriction. What this
// command reports describes the host the bot runs on, so a group chat is refused
// outright rather than served a redacted answer: a member who can read the count
// of running processes has learned something about the host, and no amount of
// withholding identifiers changes that.
func TestSessionsRefusesInGroupChats(t *testing.T) {
	f := newBindingFixture(t)
	group := &runtime.DynamicRuntime{
		IsAdmin:  true,
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_group"},
	}

	for _, tc := range []struct {
		name string
		dr   *runtime.DynamicRuntime
	}{
		{name: "a group chat", dr: group},
		{name: "a caller with no receiver", dr: &runtime.DynamicRuntime{IsAdmin: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &sessionsOptions{out: f.reply}
			err := o.Run(context.Background(), f.sr, tc.dr)
			if err == nil {
				t.Fatal("Run() answered a caller it should have refused")
			}
			if !runtime.IsUserError(err) {
				t.Errorf("Run() = %v, want a user-facing error", err)
			}
			if got := f.reply.text(); got != "" {
				t.Errorf("reply = %q, want nothing sent", got)
			}
		})
	}
}

// TestSessionsCountsWithoutNamingOtherConversations pins the privacy rule the
// answer is built around: an operator learns how many conversations are running
// and on which backends, and not one identifier belonging to somebody else. The
// ids it withholds are those conversations' chat ids — the list of chats the bot
// has served.
func TestSessionsCountsWithoutNamingOtherConversations(t *testing.T) {
	f := newBindingFixture(t)
	f.bindKey(t, "oc_someone_elses_group", session.State{Backend: agent.BackendCLI, BackendSessionID: "s1"})
	f.bindKey(t, "oc_another_group", session.State{Backend: agent.BackendLLM})
	f.bindKey(t, "ou_me", session.State{Backend: agent.BackendCLI, BackendSessionID: "s2"})

	o := &sessionsOptions{out: f.reply}
	if err := o.Run(context.Background(), f.sr, privateChat()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	reply := f.reply.text()
	if !strings.Contains(reply, "3") {
		t.Errorf("reply = %q, want the number of live conversations", reply)
	}
	// Both backends are reported, because a count that omitted one would not add
	// up to the total on the line above it.
	for _, backend := range []string{agent.BackendCLI, agent.BackendLLM} {
		if !strings.Contains(reply, backend) {
			t.Errorf("reply = %q, want the %s backend counted", reply, backend)
		}
	}
	for _, other := range []string{"oc_someone_elses_group", "oc_another_group"} {
		if strings.Contains(reply, other) {
			t.Errorf("reply = %q, must not name another conversation (%s)", reply, other)
		}
	}
	// The caller's own conversation is the one whose details may be reported.
	if !strings.Contains(reply, "本对话：后端") {
		t.Errorf("reply = %q, want the caller's own conversation described", reply)
	}
}

// TestSessionsReportsAConversationThatHasNotStarted pins what an operator sees
// before the first message: the command must not imply a conversation exists, and
// must not create one by looking.
func TestSessionsReportsAConversationThatHasNotStarted(t *testing.T) {
	f := newBindingFixture(t)

	o := &sessionsOptions{out: f.reply}
	if err := o.Run(context.Background(), f.sr, privateChat()); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !strings.Contains(f.reply.text(), "还没有开始") {
		t.Errorf("reply = %q, want it to say the conversation has not started", f.reply.text())
	}
	if _, ok := f.sr.Sessions.Lookup("ou_me"); ok {
		t.Error("/sessions brought a conversation into existence")
	}
}
