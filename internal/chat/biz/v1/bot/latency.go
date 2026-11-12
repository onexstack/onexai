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
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// commandTimings records when each stage of one slash-command message finished.
//
// It exists because the command's own work is trivial and everything around it
// is not: /help is a string join, yet answering it costs a couple of database
// reads, a resolved grant set, and several platform round trips. None of that
// was visible — the process recorded how many commands ran and nothing about
// how long they took — so "the bot is slow" had no answer that was not a guess.
//
// One line is logged per command, with a duration for each leg. A zero field
// means that stage never ran, which is how a dropped message reads: it shows
// the legs it did complete rather than implying the rest were instant.
//
// Every field is stamped by the goroutine that handles the message, so nothing
// here needs a lock.
type commandTimings struct {
	// received is when the message reached the ladder.
	received time.Time
	// runtime is when the sender, conversation, session and roles were known.
	runtime time.Time
	// session is when the conversation's session/ticket reads returned.
	session time.Time
	// roles is when the caller's SRE/admin standing was known.
	roles time.Time
	// tree is when the grant set was resolved and the command tree was built.
	tree time.Time
	// executed is when the command itself returned.
	executed time.Time
	// sent is when the reply was delivered to the platform.
	sent time.Time
}

// inboundMarks records when the stages of resolving one message finished, before
// it is known whether the message is a command.
//
// It is a value carried on the inbound rather than a *commandTimings allocated
// up front: most messages are questions, and a question needs none of this. The
// command rung turns these three timestamps into a record of its own.
type inboundMarks struct {
	// session is when the conversation's session and ticket reads returned.
	session time.Time
	// roles is when the caller's SRE/admin standing was known.
	roles time.Time
	// runtime is when the whole per-request runtime was assembled.
	runtime time.Time
}

func (m *inboundMarks) markSession() {
	if m != nil {
		m.session = time.Now()
	}
}

func (m *inboundMarks) markRoles() {
	if m != nil {
		m.roles = time.Now()
	}
}

func (m *inboundMarks) markRuntime() {
	if m != nil {
		m.runtime = time.Now()
	}
}

// messageIDOf names the message a request is answering, so a latency line can
// be matched to a conversation. It is empty for the surfaces that answer
// without a triggering message (a TUI or MCP call), which is itself worth
// seeing.
func messageIDOf(dr *runtime.DynamicRuntime) string {
	// MessageID is nil-safe and is the one accessor for "which message this
	// call answers"; reading the receiver directly is what the runtime's own
	// guard rejects (see internal/chat/pkg/runtime/identity_test.go).
	return dr.MessageID()
}

// The marks below are nil-safe so a caller that built a request by hand — as the
// card and menu paths do, which pass no record at all — needs no special case.

func (t *commandTimings) markTree() {
	if t != nil {
		t.tree = time.Now()
	}
}

func (t *commandTimings) markExecuted() {
	if t != nil {
		t.executed = time.Now()
	}
}

func (t *commandTimings) markSent() {
	if t != nil {
		t.sent = time.Now()
	}
}

// finish reports the completed stages and records the total in the command
// duration histogram — the one metric every command should contribute to.
func (t *commandTimings) finish(ctx context.Context, command, messageID string) {
	if t == nil {
		return
	}

	end := time.Now()
	leg := func(from, to time.Time) int64 {
		if from.IsZero() || to.IsZero() {
			return -1
		}
		return to.Sub(from).Milliseconds()
	}
	total := end.Sub(t.received)

	slog.InfoContext(ctx, "command latency",
		"command", command,
		"message_id", messageID,
		"total_ms", total.Milliseconds(),
		"runtime_ms", leg(t.received, t.runtime),
		"session_ms", leg(t.received, t.session),
		"roles_ms", leg(t.session, t.roles),
		"tree_ms", leg(t.runtime, t.tree),
		"exec_ms", leg(t.tree, t.executed),
		"send_ms", leg(t.executed, t.sent),
	)
	metrics.M.RecordLarkCommandDuration(ctx, command, total.Seconds())
}
