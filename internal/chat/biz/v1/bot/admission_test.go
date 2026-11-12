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
	"testing"
	"time"
)

// The tests here pin the invariant the two-card bug broke: one message produces
// one turn, and a turn is the only thing that draws a card.
//
// The platform re-delivers an event when it does not acknowledge in time, and
// nothing in the second delivery says it is a repeat: the payload is the same and
// the event ID is whatever the platform chose. So these tests deliver the same
// *message* twice — the identity the invariant is about — and check that the
// conversation answers it once.

// newTriggerFixture is a conversation whose messages carry the identity a real
// one does. The shared fixture leaves the message ID empty (it was written for the
// queueing paths), and an empty ID is deliberately treated as "cannot be a
// duplicate" — see claimTrigger — so the tests below need it filled in.
func newTriggerFixture(t *testing.T) *aiQueueFixture {
	t.Helper()
	f := newAIQueueFixture(t, 5, 1)
	f.triggerMessage("om_trigger")
	return f
}

// TestRunAIDropsASecondDeliveryOfTheSameMessage is the regression test for the
// two-card incident, stated as the invariant rather than as the mechanism.
//
// The duplicate here arrives after the first turn has finished, which is the
// harder half: the card is already closed and the conversation is idle, so
// nothing but the claim itself stands between the repeat and a second run of the
// request the user made once — a second build, in the transcript's case.
func TestRunAIDropsASecondDeliveryOfTheSameMessage(t *testing.T) {
	f := newTriggerFixture(t)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("the second delivery failed: %v", err)
	}

	if n := len(f.lark.cardRunIDs); n != 1 {
		t.Errorf("the message drew %d cards, want 1: a repeat is not a second request", n)
	}
	if n := len(f.llm.requests); n != 1 {
		t.Errorf("agent saw %d requests, want 1", n)
	}
	if len(f.lark.texts) != 0 {
		t.Errorf("a duplicate was acknowledged as a new message: %v", f.lark.texts)
	}
}

// TestRunAIDropsADuplicateThatArrivesWhileTheConversationIsBusy covers the shape
// the incident actually had: the repeat lands while the first turn is still
// running.
//
// A duplicate that reached admission in that window used to do real damage rather
// than merely duplicate work — it took the conversation over from the running
// turn and started a second card beside it. The claim stops it before any of
// that: no takeover, no queue slot, no card.
func TestRunAIDropsADuplicateThatArrivesWhileTheConversationIsBusy(t *testing.T) {
	f := newTriggerFixture(t)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	// The conversation is busy again, as it was for the twenty seconds the
	// platform waited for an acknowledgement before re-delivering.
	f.holdRunningTurn(t)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("the duplicate failed: %v", err)
	}

	if n := len(f.lark.cardRunIDs); n != 1 {
		t.Errorf("the message drew %d cards, want 1", n)
	}
	if f.sess.QueueDepth() != 0 {
		t.Errorf("QueueDepth() = %d, want 0: a repeat is not a message waiting its turn", f.sess.QueueDepth())
	}
	if len(f.lark.texts) != 0 {
		t.Errorf("a duplicate was acknowledged as a new message: %v", f.lark.texts)
	}
	if n := len(f.llm.requests); n != 1 {
		t.Errorf("agent saw %d requests, want 1", n)
	}
}

// TestRunAIRunsADifferentMessageFromTheSameConversation is the counterweight: the
// claim is keyed on the message, not on the conversation, so a user who sends two
// things gets two answers.
//
// Without this the invariant could be satisfied by dropping everything after the
// first message of a conversation, which would be a worse bug than the one being
// fixed.
func TestRunAIRunsADifferentMessageFromTheSameConversation(t *testing.T) {
	f := newTriggerFixture(t)

	if err := f.biz.runAI(context.Background(), f.dr, "第一条", nil); err != nil {
		t.Fatalf("the first message failed: %v", err)
	}
	f.triggerMessage("om_second")
	if err := f.biz.runAI(context.Background(), f.dr, "第二条", nil); err != nil {
		t.Fatalf("the second message failed: %v", err)
	}

	if n := len(f.lark.cardRunIDs); n != 2 {
		t.Errorf("two different messages drew %d cards, want 2", n)
	}
	if n := len(f.llm.requests); n != 2 {
		t.Errorf("agent saw %d requests, want 2", n)
	}
}

// TestRunAIRunsAMessageWithNoID pins the fail-open rule: a message this process
// cannot identify is not thereby a duplicate.
//
// Turning "no identity" into "refuse" would be the safe-looking choice and the
// wrong one — it would drop work whenever the identity is missing, to guard
// against a repeat that could not be recognised anyway.
func TestRunAIRunsAMessageWithNoID(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1) // the shared fixture leaves MessageId empty

	for range 2 {
		if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
			t.Fatalf("runAI returned error: %v", err)
		}
	}

	if n := len(f.lark.cardRunIDs); n != 2 {
		t.Errorf("an unidentified message drew %d cards, want 2", n)
	}
}

// TestClaimTrigger pins the table's own semantics, which the tests above only
// exercise through the turn.
func TestClaimTrigger(t *testing.T) {
	// A bot assembled without New() must not panic on its first message: the table
	// is created on demand, like the other caches on this struct.
	var b botBiz

	if !b.claimTrigger("oc_a", "om_1") {
		t.Error("the first claim of a message was not treated as first")
	}
	if b.claimTrigger("oc_a", "om_1") {
		t.Error("the same message in the same conversation was claimed twice")
	}
	if !b.claimTrigger("oc_a", "om_2") {
		t.Error("a different message was treated as a repeat")
	}
	if !b.claimTrigger("oc_b", "om_1") {
		t.Error("a claim in one conversation was counted against another")
	}

	// An empty ID is never a claim: there is nothing to remember, and the answer
	// has to be "not a duplicate" rather than "already seen".
	if !b.claimTrigger("", "om_3") || !b.claimTrigger("oc_a", "") {
		t.Error("a claim with an empty identity was refused")
	}

	// A claim older than the TTL is forgotten, so a message re-delivered hours
	// later starts a turn rather than being swallowed by it.
	b.claims["oc_a\x00om_expired"] = time.Now().Add(-2 * triggerClaimTTL)
	if !b.claimTrigger("oc_a", "om_expired") {
		t.Error("an expired claim was still treated as a repeat")
	}
}
