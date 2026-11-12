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
	"errors"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/onexstack/onexstack/pkg/ptr"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

func TestExtractCardActionValue(t *testing.T) {
	tests := []struct {
		name   string
		action *callback.CallBackAction
		want   string
	}{
		{
			name:   "button value action",
			action: &callback.CallBackAction{Value: map[string]interface{}{"action": "cmd:/help"}},
			want:   "cmd:/help",
		},
		{
			name:   "select option fallback",
			action: &callback.CallBackAction{Option: "act:/select 1"},
			want:   "act:/select 1",
		},
		{
			name:   "form submit name fallback",
			action: &callback.CallBackAction{Name: "delete_mode_submit"},
			want:   "act:/delete-mode form-submit",
		},
		{
			name:   "form cancel name fallback",
			action: &callback.CallBackAction{Name: "delete_mode_cancel"},
			want:   "act:/delete-mode cancel",
		},
		{
			name:   "empty",
			action: &callback.CallBackAction{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &callback.CardActionTriggerEvent{
				Event: &callback.CardActionTriggerRequest{Action: tt.action},
			}
			if got := extractCardActionValue(event); got != tt.want {
				t.Errorf("extractCardActionValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractCardActionValueNilSafe(t *testing.T) {
	if got := extractCardActionValue(nil); got != "" {
		t.Errorf("nil event = %q, want empty", got)
	}
	if got := extractCardActionValue(&callback.CardActionTriggerEvent{}); got != "" {
		t.Errorf("empty event = %q, want empty", got)
	}
}

func TestRecordAndCardMessageID(t *testing.T) {
	b := &botBiz{cardMsgIDs: make(map[string]string)}

	if got := b.cardMessageID("s1"); got != "" {
		t.Errorf("unset key = %q, want empty", got)
	}
	b.recordCardMessageID("s1", "om_123")
	if got := b.cardMessageID("s1"); got != "om_123" {
		t.Errorf("cardMessageID = %q, want om_123", got)
	}
	// Empty inputs are ignored.
	b.recordCardMessageID("", "om_x")
	b.recordCardMessageID("s2", "")
	if got := b.cardMessageID("s2"); got != "" {
		t.Errorf("empty message id should not be recorded, got %q", got)
	}
}

// stopClick builds the card callback a stop button sends, as the platform would
// deliver it: the clicker, the card's conversation, and the button's value.
func stopClick(value map[string]interface{}) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_clicker", UserID: ptr.To("u_clicker")},
			Action:   &callback.CallBackAction{Tag: "button", Value: value},
			Context: &callback.Context{
				OpenChatID:    "oc_test_conv",
				OpenMessageID: "om_card",
			},
		},
	}
}

// stopValue is the callback value a streaming card's stop button carries.
func stopValue(runID string) map[string]interface{} {
	return map[string]interface{}{
		"action":      StopCardAction,
		"run_id":      runID,
		"session_key": "oc_test_conv",
		"chat_type":   v1.ChatTypeGroup,
	}
}

// TestStopButtonStopsTheConversationTheCardNames is the regression test for a
// stop button that did nothing in a group. The click arrives from a person whose
// open id is not the conversation's key, so a resolver that used the clicker
// looked up a conversation that has no run in it: nothing was stopped, and the
// toast said it had been.
func TestStopButtonStopsTheConversationTheCardNames(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	const runID = "run-in-flight"
	f.sess.SetCurrentRun(runID)

	resp, err := f.biz.ProcessCard(context.Background(), stopClick(stopValue(runID)))
	if err != nil {
		t.Fatalf("ProcessCard returned error: %v", err)
	}

	if len(f.llm.stops) != 1 || f.llm.stops[0] != runID {
		t.Errorf("agent stops = %v, want exactly [%s]", f.llm.stops, runID)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("stop click answered %v, want a toast", resp)
	}
	if resp.Toast.Type != "info" {
		t.Errorf("toast type = %q, want info (the run really was stopped)", resp.Toast.Type)
	}
}

// TestStopButtonDropsQueuedTurns pins the second half of what stopping means:
// the messages sent behind the run were sent before the user asked for silence,
// and answering them afterwards answers a question they withdrew.
func TestStopButtonDropsQueuedTurns(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	const runID = "run-in-flight"
	f.sess.SetCurrentRun(runID)

	// A turn in flight and two behind it, as a burst of messages produces. The
	// running turn blocks until the test releases it: nothing in this fake ends
	// it, and a turn outliving its own lock is exactly the state being tested.
	release := make(chan struct{})
	running := make(chan struct{})
	go func() {
		_, _ = f.sess.Submit(context.Background(), func(context.Context, uint64) error {
			close(running)
			<-release
			return nil
		})
	}()
	<-running

	for i := 0; i < 2; i++ {
		if _, err := f.sess.Submit(context.Background(), func(context.Context, uint64) error {
			return nil
		}); err != nil {
			t.Fatalf("failed to queue a turn: %v", err)
		}
	}
	if depth := f.sess.QueueDepth(); depth != 2 {
		t.Fatalf("QueueDepth() = %d before the stop, want 2", depth)
	}

	if _, err := f.biz.ProcessCard(context.Background(), stopClick(stopValue(runID))); err != nil {
		t.Fatalf("ProcessCard returned error: %v", err)
	}

	// The lock is broken, but the drop happens when the running turn returns —
	// the drain loop only looks at the queue between turns.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.sess.QueueDepth() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("QueueDepth() = %d after a stop, want 0 (the queue must be dropped)", f.sess.QueueDepth())
}

// TestStopButtonRefusesALeftoverCard pins that a card whose run has already
// ended does not cancel whatever runs next: the button outlives the turn it was
// rendered for, and a click on it must not be read as a click on the new one.
func TestStopButtonRefusesALeftoverCard(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	f.sess.SetCurrentRun("run-now-running")

	resp, err := f.biz.ProcessCard(context.Background(), stopClick(stopValue("run-that-ended")))
	if err != nil {
		t.Fatalf("ProcessCard returned error: %v", err)
	}

	if len(f.llm.stops) != 0 {
		t.Errorf("agent stops = %v, want none: the clicked card's run is not the one in flight", f.llm.stops)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Errorf("answer = %v, want a warning toast", resp)
	}
}

// TestStopButtonReportsAFailedStop pins that a stop which did not reach the
// backend is not reported as one that did. The user's next move differs: retry,
// versus wait for an answer that is still coming.
func TestStopButtonReportsAFailedStop(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	const runID = "run-in-flight"
	f.sess.SetCurrentRun(runID)
	f.llm.stopErr = errors.New("agent stop: connection refused")

	resp, err := f.biz.ProcessCard(context.Background(), stopClick(stopValue(runID)))
	if err != nil {
		t.Fatalf("ProcessCard returned error: %v", err)
	}

	if len(f.llm.stops) != 1 {
		t.Errorf("agent stops = %v, want the stop to have been attempted", f.llm.stops)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("stop click answered %v, want a toast", resp)
	}
	if resp.Toast.Type != "error" {
		t.Errorf("toast type = %q, want error (the stop did not reach the backend)", resp.Toast.Type)
	}
}

// TestStopButtonWithoutARunIsRefused pins that a clicked value carrying no run
// id is answered rather than silently ignored — from the card it is impossible
// to tell the two apart.
func TestStopButtonWithoutARunIsRefused(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)

	value := stopValue("")
	value["action"] = StopCardAction
	resp, err := f.biz.ProcessCard(context.Background(), stopClick(value))
	if err != nil {
		t.Fatalf("ProcessCard returned error: %v", err)
	}

	if len(f.llm.stops) != 0 {
		t.Errorf("agent stops = %v, want none", f.llm.stops)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Errorf("answer = %v, want a warning toast", resp)
	}
}
