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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// now is a fixed stamp for the retry path's session.
var now = time.Now()

// closeAndSummarize runs /close once with whatever the fake model is scripted to
// answer.
func closeAndSummarize(t *testing.T, env *testEnv) (*runtime.CommandResult, error) {
	t.Helper()
	return env.exec(t, func(out runtime.Responder) *cobra.Command { return NewCloseCmd(env.sr, out) })
}

// TestClosingATicketWritesItsSummary is the whole point of the change: the closing
// write-up is produced when the ticket closes, and it lands on the ticket.
func TestClosingATicketWritesItsSummary(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "## 问题现象\n网关 502\n\n## 解决方案\n重启后恢复"

	if _, err := closeAndSummarize(t, env); err != nil {
		t.Fatalf("close = %v", err)
	}

	if ticket.Status != model.TicketStatusClosed {
		t.Fatalf("status = %q, want closed", ticket.Status)
	}
	if ticket.Summary != env.llm.answer {
		t.Fatalf("summary = %q, want what the model answered", ticket.Summary)
	}
	if len(env.llm.reqs) != 1 {
		t.Fatalf("model called %d times, want 1", len(env.llm.reqs))
	}
	// The group is told, because the reply it already got said the summary was
	// coming. A promise with no follow-up is worse than no promise.
	if last := last(env.lark.texts); !strings.Contains(last, "解决方案") {
		t.Errorf("the follow-up in the group = %q, want the summary", last)
	}
}

// TestThePromptCarriesTheHandlingLog pins that the /mark entries — the only
// written record of what was done, and unavailable anywhere else — reach the model.
func TestThePromptCarriesTheHandlingLog(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.store.ticketNotes.notes = append(env.store.ticketNotes.notes, &model.TicketNoteM{
		TicketID: ticket.TicketID, AuthorName: "Lingfei Kong", Note: "先重启了网关，观察十分钟没再报错",
	})
	env.llm.answer = "## 问题现象\n…"

	if _, err := closeAndSummarize(t, env); err != nil {
		t.Fatalf("close = %v", err)
	}

	if len(env.llm.reqs) != 1 {
		t.Fatalf("model called %d times, want 1", len(env.llm.reqs))
	}
	prompt := env.llm.reqs[0].GetMessages()[0].GetContent()
	if !strings.Contains(prompt, "先重启了网关") {
		t.Errorf("the prompt does not carry the handling log:\n%s", prompt)
	}
	// And it must say the conversation is absent rather than leave the model to
	// read a gap as the whole story. This fixture wires no Oncall reader.
	if !strings.Contains(prompt, "未接线") {
		t.Errorf("the prompt does not say the conversation is unavailable:\n%s", prompt)
	}
}

// TestClosingTwiceDoesNotPayForASecondSummary is the cost guarantee, asserted on
// the call count.
//
// A ticket that was already summarized has nothing owed, and /close on it must
// stay the no-op it always was — not a second model run announced as a retry.
func TestClosingTwiceDoesNotPayForASecondSummary(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "## 问题现象\n…"
	build := func(out runtime.Responder) *cobra.Command { return NewCloseCmd(env.sr, out) }

	if _, err := env.exec(t, build); err != nil {
		t.Fatalf("first close = %v", err)
	}
	res, err := env.exec(t, build)
	if err != nil {
		t.Fatalf("second close = %v", err)
	}

	if len(env.llm.reqs) != 1 {
		t.Errorf("model called %d times, want 1: an already-summarized ticket owes nothing", len(env.llm.reqs))
	}
	if !strings.Contains(res.PlainText, "工单已经关闭，无需执行此命令") {
		t.Errorf("second close = %q, want the plain idempotent notice", res.PlainText)
	}
}

// TestClosingAClosedTicketWithoutASummaryRetriesIt is the only retry this design
// has.
//
// The summary is generated beside the close, so a model outage — or a process that
// died between the two — leaves a closed ticket with no write-up and nothing in
// the group to say so. Saying /close again is how a person asks for it, and the
// command has to answer that request rather than declare the ticket done.
func TestClosingAClosedTicketWithoutASummaryRetriesIt(t *testing.T) {
	ticket, session := ticketGroup()
	ticket.Status = model.TicketStatusClosed
	session.ClosedAt = &now
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "## 问题现象\n…"

	res, err := closeAndSummarize(t, env)
	if err != nil {
		t.Fatalf("close = %v", err)
	}

	if ticket.Summary != env.llm.answer {
		t.Errorf("summary = %q, want the retry to have written it", ticket.Summary)
	}
	if !strings.Contains(res.PlainText, "补生成") {
		t.Errorf("reply = %q, want it to say the summary is being retried", res.PlainText)
	}
	// The status must not be re-pushed: updateTicketStatus would reach Regen and
	// bump updated_at for a ticket whose state did not change.
	if session.ClosedAt == nil {
		t.Error("the retry must not touch the session's close stamp")
	}
}

// TestABriefMarkerIsStoredAndSaidSoOutLoud pins the gate the downstream sync reads.
//
// The model declines low-value tickets with one sentence instead of a write-up, and
// that sentence is what the ticket stores — a reader who opens it sees that somebody
// looked and that there was nothing to record, rather than an empty field that could
// mean four different things.
func TestABriefMarkerIsStoredAndSaidSoOutLoud(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	// With the punctuation a model tends to add — the value has to reach the column
	// in the one form the query recognises. See known/chat.CanonicalSummary.
	env.llm.answer = chatknown.SummaryBrief + "。"

	if _, err := closeAndSummarize(t, env); err != nil {
		t.Fatalf("close = %v", err)
	}

	if ticket.Summary != chatknown.SummaryBrief {
		t.Fatalf("summary = %q, want the marker the model answered", ticket.Summary)
	}
	got := last(env.lark.texts)
	if !strings.Contains(got, "记录简略") {
		t.Errorf("follow-up = %q, want the brief notice", got)
	}
	// Not dressed up as an answer: the group must not be shown a message that
	// looks like a write-up when the model declined to write one.
	if strings.Contains(got, "结案总结已生成：") {
		t.Errorf("follow-up = %q, want it to read as a decline rather than a summary", got)
	}
}

// TestAFailedSummaryLeavesTheTicketClosed is the ordering guarantee: closing does
// not depend on the model.
//
// If it did, a model outage would mean "you cannot close tickets" — a worse failure
// than a missing write-up, and one an operator would have to work around by hand.
func TestAFailedSummaryLeavesTheTicketClosed(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.err = errors.New("the model is unavailable")

	res, err := closeAndSummarize(t, env)
	if err != nil {
		t.Fatalf("close = %v, want the close to succeed despite the model", err)
	}

	if ticket.Status != model.TicketStatusClosed {
		t.Errorf("status = %q, want closed", ticket.Status)
	}
	if ticket.Summary != "" {
		t.Errorf("summary = %q, want empty after a failed generation", ticket.Summary)
	}
	// The failure reaches the group, because the summary is the only thing the
	// downstream sync publishes and a silent loss would be invisible everywhere.
	if got := last(env.lark.texts); !strings.Contains(got, "生成失败") {
		t.Errorf("follow-up = %q, want the failure reported to the group", got)
	}
	// The reply itself is not pinned: it is the generic closing message, and its
	// wording is a product decision (87f4f8d dropped the sentence that promised a
	// summary). What this test owns is that the caller was answered at all —
	// closing must never depend on the model or leave the person with silence.
	if strings.TrimSpace(res.PlainText) == "" {
		t.Error("reply is empty; a close that fails its summary still owes the group an answer")
	}
}

// TestASummaryAlreadyOnTheTicketIsNotAskedForAgain pins the flight's own guard.
//
// The cheap in-process flight covers two closes arriving together; what it cannot
// see is a summary that another process (or an earlier flight) wrote while this one
// was queued. Re-reading the ticket inside the flight is what closes that, and it is
// the difference between a retry costing one read and costing a model run.
func TestASummaryAlreadyOnTheTicketIsNotAskedForAgain(t *testing.T) {
	ticket, session := ticketGroup()
	ticket.Summary = "## 问题现象\n早已写好"
	env := newTestEnv(t, ticket, session)

	got, err := generateWithinFlight(context.Background(), env.sr, ticket.TicketID, ticket.Title, ticket.Description)
	if err != nil {
		t.Fatalf("generateWithinFlight = %v", err)
	}

	if got != ticket.Summary {
		t.Errorf("returned %q, want the summary already on the ticket", got)
	}
	if len(env.llm.reqs) != 0 {
		t.Errorf("model called %d times, want 0: the work was already done", len(env.llm.reqs))
	}
}
