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
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The tests here are about the decision as the reader meets it: a pause is drawn
// on the turn's own card, the card it is drawn on is the same one the answer
// streams into, and every way a decision can end — answered, taken over, stopped,
// timed out — leaves that card with no button anybody can press.
//
// They run against the same fixture the streaming tests use, because the point is
// not that decision.go renders the right strings but that a real turn, driven by
// a real stream, comes to look like this.

// waitFor polls until cond holds, failing the test after a deadline.
//
// It is a wait rather than an assertion wherever the thing waited on happens on
// another goroutine: delivering an answer resumes the run, and the rest of that
// run arrives through the sink the first half was given, on a goroutine the click
// that started it does not join.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// questionInterruptEvent asks a question with options, which is the pause kind
// whose buttons carry the answer itself rather than a yes/no decision.
func questionInterruptEvent(requestID, question string, options ...string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{
		Type: llmv1.AgentEventType_agentEventTypeInterrupted,
		Interrupt: &llmv1.AgentInterrupt{
			RequestID: requestID,
			Requests: []*llmv1.AgentInterruptRequest{{
				Address:  "ask:call-1",
				ToolName: "AskUserQuestion",
				Question: question,
				Kind:     agent.InterruptKindQuestion,
				Options:  options,
			}},
		},
	}
}

// decisionClick builds the callback a decision button sends. It is the platform's
// shape: the clicker, the card's conversation, and the button's value.
func decisionClick(value map[string]interface{}) *callback.CardActionTriggerEvent {
	return stopClick(value)
}

// permClickValue is the callback value of a permission button as the card
// carries it, plus whatever the test overrides.
func permClickValue(action string, overrides map[string]interface{}) map[string]interface{} {
	value := map[string]interface{}{
		"action":      action,
		"session_key": "oc_test_conv",
		"inline":      inlineDecisionMark,
	}
	for k, v := range overrides {
		value[k] = v
	}
	return value
}

// TestDecisionLogMarkdownIsOnlyItsLines pins the one pure function the whole
// decision rendering is built on: blank lines are dropped, and the rest are one
// line each, in the order they were appended.
func TestDecisionLogMarkdownIsOnlyItsLines(t *testing.T) {
	if got := decisionLogMarkdown(nil); got != "" {
		t.Errorf("an empty log renders %q, want the empty string", got)
	}
	if got := decisionLogMarkdown([]string{"a", "  ", "", "b"}); got != "a\nb" {
		t.Errorf("blank lines survived: %q", got)
	}

	var log decisionLog
	log.append("? 需要你的批准：pipeline")
	log.append("   ")
	if got := log.markdown(); got != "? 需要你的批准：pipeline" {
		t.Errorf("log = %q, want only the one real line", got)
	}
	if log.empty() {
		t.Error("a log with a line reports itself empty")
	}
}

// TestPauseDrawsTheDecisionOnTheTurnsOwnCard is the headline of the rework: the
// turn the reader is watching apply the pause writes the question and its buttons
// into the card already on screen, in the gap above the stop button, and says so
// in the conversation-list summary. It does not send a second card.
func TestPauseDrawsTheDecisionOnTheTurnsOwnCard(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	// The log lands immediately above the stop row, so the card reads top to
	// bottom: the question, its buttons, and the thing that ends the turn.
	var target string
	for _, ins := range f.lark.insertList() {
		if ins.ID == decisionLogElementID {
			target = ins.Target
		}
	}
	if target != stopRowCardElementID {
		t.Errorf("the decision log was inserted before %q, want the stop row %q", target, stopRowCardElementID)
	}
	if got := f.lark.elementText(decisionLogElementID); !strings.Contains(got, "需要你的批准") ||
		!strings.Contains(got, "pipeline") {
		t.Errorf("the decision log says %q, want the pending tool named", got)
	}

	// The buttons, and the second row that grants the tool for the session.
	main := f.lark.buttonsUnder("decision_row_1")
	if len(main) != 2 || main[0] != buttonAllowText || main[1] != buttonDenyText {
		t.Errorf("the decision row holds %q, want [%q %q]", main, buttonAllowText, buttonDenyText)
	}
	if last := f.lark.buttonsUnder("decision_grant_1"); len(last) != 1 ||
		last[0] != buttonAllowAllText {
		t.Errorf("the session-grant row holds %q, want [%q]", last, buttonAllowAllText)
	}

	// Every button carries what answering it needs: which run, which pause, what
	// the click is about, and the mark that says the question lives on this card
	// rather than on a message of its own.
	btn := firstButtonUnder(t, f.lark, "decision_row_1")
	value, _ := btn["value"].(map[string]string)
	if value["run_id"] == "" || value["req_id"] != "req-1" {
		t.Errorf("the button names run %q pause %q, want this turn and req-1", value["run_id"], value["req_id"])
	}
	if value["perm_subject"] != "pipeline" {
		t.Errorf("the button's subject is %q, want the tool the pause is about", value["perm_subject"])
	}
	if value["inline"] != inlineDecisionMark {
		t.Errorf("the button carries inline = %q, so a click would not know to rewrite this card", value["inline"])
	}
	if value["session_key"] != "oc_test_conv" {
		t.Errorf("the button carries session_key %q, want the conversation", value["session_key"])
	}

	// The card stops claiming to be thinking, and says in the conversation list
	// that it is waiting on a person. Cleared is the zero-width space rather than
	// the empty string, because cardkit refuses an empty content outright — the
	// placeholder stayed on the card for the whole pause until that was found.
	if got := f.lark.lastUpdate("answer"); got != placeholderCleared {
		t.Errorf("the answer element holds %q, want the thinking placeholder cleared", got)
	}
	if strings.ReplaceAll(f.lark.lastUpdate("answer"), "​", "") != "" {
		t.Errorf("the answer element holds %q, which is not a blank card", f.lark.lastUpdate("answer"))
	}
	if got := f.lark.summaryList(); len(got) == 0 || got[len(got)-1] != summaryAwaiting {
		t.Errorf("the card summary is %v, want %q last", got, summaryAwaiting)
	}
	if f.lark.closedCount() != 0 {
		t.Error("a pause finalized the card; it has not finished")
	}
	if len(f.lark.markdowns) != 0 {
		t.Errorf("a pause sent a card of its own: %v", f.lark.markdowns)
	}
}

// TestTheDecisionLineNamesTheCallItIsAskingAbout pins the arguments on the
// decision line. "May I run pipeline?" is not a decision anyone can make, and a
// card that asks it is one the reader has to answer by guessing; the line has to
// carry what the call would do, in the one form short enough to sit above the
// buttons.
func TestTheDecisionLineNamesTheCallItIsAskingAbout(t *testing.T) {
	event := interruptedEvent("req-1", "pipeline")
	event.Interrupt.Requests[0].Arguments = `{"service":"content","env":"edu-onex"}`

	f := newAIStreamFixture(t, event, suspendedEvent())
	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	got := f.lark.elementText(decisionLogElementID)
	for _, want := range []string{"pipeline", "env=edu-onex", "service=content"} {
		if !strings.Contains(got, want) {
			t.Errorf("the decision log says %q, want it to name %q", got, want)
		}
	}
}

// firstButtonUnder returns the raw button component under an element id.
func firstButtonUnder(t *testing.T, f *fakeLarkServices, id string) map[string]any {
	t.Helper()
	for _, col := range columnsOf(f.liveElements[id]) {
		for _, sub := range subElements(col) {
			if tag, _ := sub["tag"].(string); tag == "button" {
				return sub
			}
		}
	}
	t.Fatalf("no button under %q", id)
	return nil
}

// TestPauseKeepsWhatTheModelAlreadySaid is the other half of clearing the
// placeholder: the placeholder goes because it stopped being true, but a model
// that had already written something must not have it taken away. The pause is
// drawn under the answer, not instead of it.
func TestPauseKeepsWhatTheModelAlreadySaid(t *testing.T) {
	f := newAIStreamFixture(t,
		answerEvent("我先看一下仓库"),
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	if got := f.lark.lastUpdate("answer"); got != "我先看一下仓库" {
		t.Errorf("the answer element holds %q, want what the model had already said", got)
	}
	if !f.lark.hasLiveElement(decisionLogElementID) {
		t.Error("the pause was not drawn on the card that carries the answer")
	}
}

// TestPausedToolRowWaitsOnAPersonInsteadOfRunning is the half of a pause the tool
// panel owns: the row that was running stops counting — and says why, on the card,
// at the moment it stops. The ticker is stopped when the run parks, so a row that
// was not rewritten here would keep a clock frozen mid-count and read as a hung
// tool rather than as a question.
func TestPausedToolRowWaitsOnAPersonInsteadOfRunning(t *testing.T) {
	f := newAIStreamFixture(t,
		toolStepEvent("pipeline", larkcard.RichStepStatusPending),
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	if got := f.lark.lastUpdate("progress"); !strings.Contains(got, "等待人工确认") {
		t.Errorf("the paused tool row says %q, want it waiting on a person", got)
	}
	// And it is not still counting: the pause is what stops the panel redrawing.
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}
	if turn.progress.hasPending() {
		t.Error("the panel still reports a running tool while the run is parked on a person")
	}
}

// TestAnsweringADecisionLeavesOneLineAndNoButtons is the second half of the
// reader's experience: the buttons go away, the log keeps what was decided, and
// the card is finalized carrying both the answer and the decision that let it
// continue.
func TestAnsweringADecisionLeavesOneLineAndNoButtons(t *testing.T) {
	f := newAIStreamFixture(t,
		answerEvent("我先看一下"),
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	f.llm.approveEvents = []*llmv1.AgentStreamEvent{answerEvent("已经构建完成"), doneEvent("我先看一下已经构建完成")}

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	value := permClickValue(PermAllow, map[string]interface{}{
		"run_id":       turn.runID,
		"req_id":       "req-1",
		"perm_label":   labelAllowed,
		"perm_subject": "pipeline",
	})
	resp, err := f.biz.ProcessCard(context.Background(), decisionClick(value))
	if err != nil {
		t.Fatalf("the click failed: %v", err)
	}
	// An in-card decision answers with a toast; the card carries the outcome, so a
	// click must not replace the card it came from.
	if resp != nil && resp.Card != nil {
		t.Error("clicking a decision on the turn's card replaced the card")
	}

	// The run was resumed, and by the pause the button named.
	got := f.llm.awaitApprovals(t, 1)[0]
	if got.runID != turn.runID || got.requestID != "req-1" || got.action != agent.DecisionAllow {
		t.Errorf("the backend was sent %+v, want an allow for this run and pause", got)
	}

	if f.lark.hasLiveElement("decision_row_1") {
		t.Error("the answered decision kept its buttons; a dead button is a decision that can be made twice")
	}
	if f.lark.hasLiveElement("decision_grant_1") {
		t.Error("the answered decision kept its session-grant button")
	}
	log := f.lark.lastUpdate(decisionLogElementID)
	if !strings.Contains(log, decisionTitleAsking+"pipeline") {
		t.Errorf("the log lost the question when the answer was appended:\n%s", log)
	}
	if !strings.Contains(log, larkcard.GlyphDone+" 已允许：pipeline") {
		t.Errorf("the log does not record the allow:\n%s", log)
	}
	// The buttons are gone, so the card is no longer waiting on anyone.
	if got := f.lark.summaryList(); got[len(got)-1] != summaryThinking {
		t.Errorf("the card summary is %q after being answered, want %q", got[len(got)-1], summaryThinking)
	}

	waitFor(t, "the resumed run to finish and close the card", func() bool { return f.lark.closedCount() == 1 })
	final := f.lark.closedCards()[0]
	if !strings.Contains(final, "已经构建完成") {
		t.Errorf("the final card lost the answer the decision let through:\n%s", final)
	}
	if !strings.Contains(final, "已允许：pipeline") {
		t.Errorf("the final card lost the decision log; the pause is invisible afterwards:\n%s", final)
	}
}

// TestADecisionDeliveredAsTheRunEndsStillReachesTheCard is the deterministic half
// of a race the click path used to lose: the answer resumes the run on a goroutine
// of its own, so a run that ends quickly finalizes the card before the click that
// delivered it gets to write anything. The outcome is armed before the answer goes
// out (armDecision) and settled by whoever gets there first, which is what makes
// "the answer was delivered" and "the card says so" one event instead of two that
// can be separated.
//
// The order is forced here rather than raced: arm, then end the turn, then let the
// click arrive far too late. Waiting for it to happen on its own is what the
// end-to-end test above does, and it happened roughly one run in three.
func TestADecisionDeliveredAsTheRunEndsStillReachesTheCard(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	// The click has delivered the answer and armed its outcome, and has not drawn
	// it yet.
	line := decisionOutcomeLine(agent.DecisionAllow, "pipeline", time.Now())
	if !turn.armDecision(line) {
		t.Fatal("a live turn refused to arm a decision that was just delivered")
	}

	// The run ends first — on its own goroutine, in production.
	turn.finish()

	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the turn ended without closing its card")
	}
	if !strings.Contains(final, larkcard.GlyphDone+" 已允许：pipeline") {
		t.Errorf("the decision was delivered but the final card does not say so:\n%s", final)
	}

	// The click's own draw, arriving after all of that, has nothing left to do: the
	// card is closed and accepts no element writes, and the outcome is already in
	// the log the final card was built from.
	drawnBefore := len(f.lark.updates[decisionLogElementID])
	turn.resolveDecision(context.Background())
	if drawn := len(f.lark.updates[decisionLogElementID]); drawn != drawnBefore {
		t.Errorf("a click drew into a card that had already been closed (%d writes, want %d)", drawn, drawnBefore)
	}
	after, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the turn ended without closing its card")
	}
	if !strings.Contains(after, "已允许：pipeline") {
		t.Error("drawing after the turn ended changed the final card")
	}

	// And a decision offered after the turn is over is refused outright, so it
	// cannot be held for a card that has already been built without it.
	if turn.armDecision(decisionOutcomeLine(agent.DecisionDeny, "pipeline", time.Now())) {
		t.Error("a turn that has ended accepted an outcome it can no longer show")
	}
}

// TestADecisionThatWasNeverDeliveredIsNotRecorded is the other side of the
// handshake: arming is not recording. An outcome whose delivery fails is dropped,
// so a card cannot end up claiming a decision the backend never received — which
// is the reason the outcome is not appended to the log up front.
func TestADecisionThatWasNeverDeliveredIsNotRecorded(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	line := decisionOutcomeLine(agent.DecisionDeny, "pipeline", time.Now())
	if !turn.armDecision(line) {
		t.Fatal("a live turn refused to arm a decision that was about to be delivered")
	}
	turn.disarmDecision() // the delivery failed
	turn.finish()

	final, ok := f.lark.lastClosedCard()
	if !ok {
		t.Fatal("the turn ended without closing its card")
	}
	if strings.Contains(final, larkcard.GlyphFailed+" 已拒绝") {
		t.Errorf("a decision that was never delivered is on the final card:\n%s", final)
	}
	// The question stays: nothing decided it.
	if !strings.Contains(final, decisionTitleAsking+"pipeline") {
		t.Errorf("the final card lost the question, which is still unanswered:\n%s", final)
	}
}

// TestDenyingADecisionRecordsWhatWasRefused is the counterweight to the test
// above: the log line is built from the decision that was delivered, not from the
// button's label, so a refusal cannot be recorded as an allow.
func TestDenyingADecisionRecordsWhatWasRefused(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	if _, err := f.biz.ProcessCard(context.Background(), decisionClick(permClickValue(PermDeny, map[string]interface{}{
		"run_id":       turn.runID,
		"req_id":       "req-1",
		"perm_label":   labelDenied,
		"perm_subject": "pipeline",
	}))); err != nil {
		t.Fatalf("the click failed: %v", err)
	}

	if got := f.llm.awaitApprovals(t, 1)[0].action; got != agent.DecisionDeny {
		t.Errorf("the backend was sent %q, want a deny", got)
	}
	log := f.lark.lastUpdate(decisionLogElementID)
	if !strings.Contains(log, larkcard.GlyphFailed+" 已拒绝：pipeline") {
		t.Errorf("the log does not record the refusal:\n%s", log)
	}
	if strings.Contains(log, "已允许") {
		t.Errorf("the log records a refusal as an allow:\n%s", log)
	}
}

// TestAnsweringAQuestionOnTheCardChoosesTheOption pins the question side of the
// same card: the options are buttons on the turn's card, and the answer the
// backend receives is the option's text, not a decision about permission.
func TestAnsweringAQuestionOnTheCardChoosesTheOption(t *testing.T) {
	f := newAIStreamFixture(t,
		questionInterruptEvent("req-1", "要发布到哪个环境？", "edu-onex", "prod"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请发布 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	if got := f.lark.buttonsUnder("decision_row_1"); len(got) != 2 || got[0] != "edu-onex" || got[1] != "prod" {
		t.Errorf("the question's options are %q, want one button per option", got)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	if _, err := f.biz.ProcessCard(context.Background(), decisionClick(permClickValue("askq:1", map[string]interface{}{
		"run_id":     turn.runID,
		"req_id":     "req-1",
		"askq_addr":  "ask:call-1",
		"askq_label": "edu-onex",
	}))); err != nil {
		t.Fatalf("the click failed: %v", err)
	}

	got := f.llm.awaitApprovals(t, 1)[0]
	if got.answers["ask:call-1"] != "edu-onex" {
		t.Errorf("the backend was sent answers %v, want the chosen option against the question's address", got.answers)
	}
	if log := f.lark.lastUpdate(decisionLogElementID); !strings.Contains(log, larkcard.GlyphDone+" 已选择：edu-onex") {
		t.Errorf("the log does not record the choice:\n%s", log)
	}
}

// TestSecondPauseExtendsTheSameLog is what makes a turn that asks twice readable:
// one log element, appended to, so the reader sees a timeline rather than two
// unrelated questions. The buttons are still one set per pause — the second set
// has an id of its own, because the first set's id was deleted with its buttons.
func TestSecondPauseExtendsTheSameLog(t *testing.T) {
	f := newAIStreamFixture(t,
		questionInterruptEvent("req-1", "要发布到哪个环境？", "edu-onex", "prod"),
		suspendedEvent(),
	)
	f.llm.approveEvents = []*llmv1.AgentStreamEvent{
		interruptedEvent("req-2", "pipeline"),
		suspendedEvent(),
	}

	if err := f.biz.runAI(context.Background(), f.dr, "请发布 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the first pause was not recorded against the conversation")
	}

	// Answer the question, which lets the run continue to its second pause.
	if _, err := f.biz.ProcessCard(context.Background(), decisionClick(permClickValue("askq:1", map[string]interface{}{
		"run_id":     turn.runID,
		"req_id":     "req-1",
		"askq_addr":  "ask:call-1",
		"askq_label": "edu-onex",
	}))); err != nil {
		t.Fatalf("the click failed: %v", err)
	}
	waitFor(t, "the run to pause again", func() bool { return f.lark.hasLiveElement("decision_row_2") })

	log := f.lark.lastUpdate(decisionLogElementID)
	lines := strings.Split(log, "\n")
	if len(lines) != 3 {
		t.Fatalf("the log holds %d lines, want the question, its answer and the approval:\n%s", len(lines), log)
	}
	if !strings.HasPrefix(lines[0], larkcard.GlyphPending) || !strings.Contains(lines[0], "要发布到哪个环境") {
		t.Errorf("line 1 = %q, want the question", lines[0])
	}
	if !strings.Contains(lines[1], "已选择：edu-onex") {
		t.Errorf("line 2 = %q, want the choice", lines[1])
	}
	if !strings.Contains(lines[2], "需要你的批准") || !strings.Contains(lines[2], "pipeline") {
		t.Errorf("line 3 = %q, want the second pause", lines[2])
	}

	// The log element is written, not re-inserted: the second pause must not put a
	// second log on the card, which would split the timeline in two.
	var logs int
	for _, ins := range f.lark.insertList() {
		if ins.ID == decisionLogElementID {
			logs++
		}
	}
	if logs != 1 {
		t.Errorf("the log element was inserted %d times, want once for the whole turn", logs)
	}
	if !f.lark.hasLiveElement("decision_grant_2") {
		t.Error("the second pause's session-grant button is missing; the second pause drew no buttons of its own")
	}
}

// TestAPauseThatIsSupersededWithdrawsItsButtons covers the pause nobody answered
// before another arrived: a backend that holds its stream open while it waits —
// the CLI, whose process owns the request — can interrupt twice without an answer
// in between. The first pause's buttons are the only ones that can still be
// clicked and cannot be delivered any more, so they go, and the log says what
// became of them.
func TestAPauseThatIsSupersededWithdrawsItsButtons(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		interruptedEvent("req-2", "delete_job"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	// The first pause's row is gone, and only the second pause's is left to click.
	if f.lark.hasLiveElement("decision_row_1") {
		t.Error("the superseded pause's buttons are still on the card")
	}
	if !f.lark.hasLiveElement("decision_row_2") {
		t.Fatal("the second pause drew no buttons")
	}
	var withdrew bool
	for _, id := range f.lark.deletedIDs() {
		if id == "decision_row_1" {
			withdrew = true
		}
	}
	if !withdrew {
		t.Errorf("deleted elements = %v, want the superseded row", f.lark.deletedIDs())
	}

	// The log reads as a timeline of what happened to each pause, in order.
	log := f.lark.lastUpdate(decisionLogElementID)
	lines := strings.Split(log, "\n")
	if len(lines) != 3 {
		t.Fatalf("the log holds %d lines, want the superseded pause, its fate and the new pause:\n%s", len(lines), log)
	}
	if !strings.Contains(lines[0], "pipeline") {
		t.Errorf("line 1 = %q, want the first pause", lines[0])
	}
	if !strings.HasPrefix(lines[1], larkcard.GlyphIdle) || !strings.Contains(lines[1], "已被新的确认替代") {
		t.Errorf("line 2 = %q, want the superseded line: nothing was decided, nothing ran", lines[1])
	}
	if !strings.Contains(lines[2], "delete_job") {
		t.Errorf("line 3 = %q, want the pause that replaced it", lines[2])
	}

	// The record a text answer would reach names the live pause, not the one that
	// was withdrawn.
	pending := f.sess.PendingPermission()
	if pending == nil || pending.RequestID != "req-2" {
		t.Errorf("the pending decision is %+v, want the pause that is still waiting", pending)
	}
}

// TestTakeoverCancelsTheParkedRun is the choice the user made about the second
// card in the transcript: a new message is answered, and the run the conversation
// was parked on is cancelled rather than left waiting — its gated tool must not
// run, and its card must stop offering buttons that would run it.
func TestTakeoverCancelsTheParkedRun(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	parked := f.biz.suspendedTurn(f.group)
	if parked == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	// The user changes their mind and says something else instead of answering.
	f.llm.events = []*llmv1.AgentStreamEvent{answerEvent("换个问题"), doneEvent("换个问题")}
	if err := f.biz.runAI(context.Background(), f.dr, "算了，先看看别的", nil); err != nil {
		t.Fatalf("the new message failed: %v", err)
	}

	if len(f.llm.stops) != 1 || f.llm.stops[0] != parked.runID {
		t.Errorf("the backend was asked to stop %v, want the parked run %q", f.llm.stops, parked.runID)
	}
	if f.sess.PendingPermission() != nil {
		t.Error("the taken-over pause is still the conversation's pending decision")
	}
	if f.biz.suspendedTurn(f.group) != nil {
		t.Error("the conversation is still parked after being taken over")
	}
	// The old card is closed, says why, and has no buttons left to press.
	if len(f.lark.closedCards()) == 0 {
		t.Fatal("the taken-over card was never closed")
	}
	old := f.lark.closedCards()[0]
	if !strings.Contains(old, decisionLineCancelled(reasonTakeover)) {
		t.Errorf("the taken-over card does not say it was cancelled:\n%s", old)
	}
	if f.lark.hasLiveElement("decision_row_1") {
		t.Error("the taken-over card still offers the decision its run can no longer honour")
	}
	// And the user's new message got a card of its own, which is allowed because
	// the old one is now finished rather than merely forgotten.
	if len(f.lark.cardRunIDs) != 2 {
		t.Errorf("the conversation created %d cards, want the parked turn's and the new message's",
			len(f.lark.cardRunIDs))
	}
}

// TestANewMessageDoesNotTakeOverARunningTurn is the other half of the takeover
// rule, and the reason the transcript's second card carried a bare 已停止 (stopped).
//
// A parked turn stays registered until it finishes — clearSuspendedTurn runs in
// finish — so between the answer and the last event the registry holds a turn
// that is running, not waiting. Taking that one over stops work the user has just
// approved and closes its card as if they had abandoned it. Only a turn still
// waiting on a decision may be taken over; a running one is left alone.
func TestANewMessageDoesNotTakeOverARunningTurn(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	f.triggerMessage("om_first")

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	parked := f.biz.suspendedTurn(f.group)
	if parked == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	// The person answers, and the run starts producing again. Driving the
	// transition rather than answering through the card keeps the running half
	// from racing to its own end inside this test; it is the same transition the
	// first event of the continuation performs.
	parked.resumeIfPaused()
	if parked.paused() {
		t.Fatal("the turn is still parked after being answered")
	}

	// A different message arrives while that half is running.
	f.triggerMessage("om_second")
	f.llm.events = []*llmv1.AgentStreamEvent{answerEvent("换个问题"), doneEvent("换个问题")}
	if err := f.biz.runAI(context.Background(), f.dr, "算了，先看看别的", nil); err != nil {
		t.Fatalf("the new message failed: %v", err)
	}

	if len(f.llm.stops) != 0 {
		t.Errorf("the backend was asked to stop %v; a run that is producing is not one to take over", f.llm.stops)
	}
	if f.biz.suspendedTurn(f.group) != parked {
		t.Error("the running turn lost its registration, so a later stop click cannot find it")
	}
	// Only one card was closed: the new message's. The running turn's is still
	// open, because the run behind it is still going.
	if f.lark.closedCount() != 1 {
		t.Errorf("closed %d cards, want 1: taking the running turn over would close its card too", f.lark.closedCount())
	}

	// What is deliberately not claimed here: the new message is not *queued*
	// behind the running half. pkg/agent/llm continues a parked run with
	// `go s.pump(context.WithoutCancel(ctx), ...)` so that a card click with a
	// three-second budget is not held open for the minutes a run may take, which
	// means the conversation's lock is free while a resumed run is producing —
	// queueing a message that arrives in that window would mean holding the lock
	// across the resumed half, which this fix does not do. What it does do is stop
	// the new message from killing the run the user just approved.
}

// TestAnExpiredWaitClosesTheCardAndRefusesLateClicks pins the state that did not
// exist before this rework. Nothing watches a parked turn's deadline, so a pause
// nobody answered used to stand in the conversation for ever with live buttons
// over a run the backend had already forgotten.
func TestAnExpiredWaitClosesTheCardAndRefusesLateClicks(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	// The wait is driven rather than waited out: the grace window is half an hour,
	// and what the test is about is what the timer does when it fires.
	turn.expireDecision()

	if !turn.expiredWait() {
		t.Fatal("the turn does not report its wait as expired")
	}
	log := f.lark.lastUpdate(decisionLogElementID)
	if !strings.Contains(log, decisionLineExpired()) {
		t.Errorf("the log does not record the expiry:\n%s", log)
	}
	if f.lark.hasLiveElement("decision_row_1") {
		t.Error("an expired decision kept its buttons")
	}
	if f.lark.closedCount() != 1 {
		t.Errorf("the card was closed %d times, want once", f.lark.closedCount())
	}
	if !strings.Contains(f.lark.closedCards()[0], decisionLineExpired()) {
		t.Errorf("the final card does not carry the expiry:\n%s", f.lark.closedCards()[0])
	}

	// A click that was already on its way is refused, and refused without being
	// delivered: the run is gone, and answering it would resume nothing.
	resp, err := f.biz.ProcessCard(context.Background(), decisionClick(permClickValue(PermAllow, map[string]interface{}{
		"run_id":       turn.runID,
		"req_id":       "req-1",
		"perm_label":   labelAllowed,
		"perm_subject": "pipeline",
	})))
	if err != nil {
		t.Fatalf("the late click failed: %v", err)
	}
	if len(f.llm.approvalsSeen()) != 0 {
		t.Errorf("a click on an expired decision reached the backend: %+v", f.llm.approvalsSeen())
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Content != decisionExpiredToast {
		t.Errorf("the late click answered %+v, want the toast %q", resp, decisionExpiredToast)
	}
}

// TestStoppingAParkedTurnRecordsWhyTheDecisionWentUnmade covers the /stop side of
// the same invariant the takeover test covers from the message side: every path
// that ends a parked run leaves its card saying that nothing was decided and
// nothing ran.
func TestStoppingAParkedTurnRecordsWhyTheDecisionWentUnmade(t *testing.T) {
	f := newAIStreamFixture(t,
		toolStepEvent("pipeline", larkcard.RichStepStatusPending),
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the pause was not recorded against the conversation")
	}

	f.biz.closeParkedTurn(context.Background(), f.group, turn.runID, reasonStopped)

	log := f.lark.lastUpdate(decisionLogElementID)
	if !strings.Contains(log, decisionLineCancelled(reasonStopped)) {
		t.Errorf("the log does not say why the decision went unmade:\n%s", log)
	}
	// The row that was waiting says the tool did not run, rather than leaving the
	// card claiming somebody can still answer for it.
	if !strings.Contains(f.lark.closedCards()[0], "未执行") {
		t.Errorf("the stopped card's tool row does not say the tool never ran:\n%s", f.lark.closedCards()[0])
	}
}

// TestADecisionTheCardRefusesIsStillAnswerableOnAMessageOfItsOwn is the fallback
// the whole design rests on: if the turn's card will not take the insert — an
// older client, a card finalized elsewhere, an API that says no — the pause goes
// out as the separate decision card this code used to send, and the pending
// decision is recorded either way, so the run is answerable however the card
// behaves.
func TestADecisionTheCardRefusesIsStillAnswerableOnAMessageOfItsOwn(t *testing.T) {
	f := newAIStreamFixture(t,
		interruptedEvent("req-1", "pipeline"),
		suspendedEvent(),
	)
	// The pause still sends a card, through the retrier, so it needs one pointed
	// at an endpoint that refuses: what is under test is that the fallback is
	// attempted at all, not what Feishu does with it.
	f.biz.retrier = imlark.NewRetrier(larkClientThatRefuses(t), "", "")
	f.lark.failInsert = true

	if err := f.biz.runAI(context.Background(), f.dr, "请构建 content 服务", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	if f.lark.hasLiveElement(decisionLogElementID) {
		t.Error("the card took an insert it reported as refused")
	}
	if f.sess.PendingPermission() == nil {
		t.Error("the degraded pause recorded no pending decision, so nothing can answer the run")
	}
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the degraded pause left the conversation with no turn to continue")
	}
	if turn.inlineDecisionOn() {
		t.Error("the turn believes its decision is on the card, so a click would try to rewrite one that never took it")
	}
	if f.lark.closedCount() != 0 {
		t.Error("a degraded pause finalized the card")
	}
}

// TestDecisionButtonsAreBuiltFromTheSameBlockBothPathsUse pins that the fallback
// and the in-card path describe one pause one way: the buttons differ only by the
// mark that tells a click which kind of card it came from.
func TestDecisionButtonsAreBuiltFromTheSameBlockBothPathsUse(t *testing.T) {
	f := newAIQueueFixture(t, 1, 1)
	blk := f.biz.approvalDecision(f.dr, "run-1", &agent.Interrupt{
		RequestID: "req-1",
		Requests: []agent.InterruptRequest{{
			Address:  "tool:call-1",
			ToolName: "pipeline",
			Question: "允许执行 pipeline 吗？",
		}},
	})
	if blk == nil {
		t.Fatal("the permission pause produced no decision block")
	}
	if got := blk.request; !strings.HasPrefix(got, decisionTitleAsking) {
		t.Errorf("the request line is %q, want it opening with the pending glyph and the ask", got)
	}
	if len(blk.buttons) != 2 || len(blk.secondary) != 1 {
		t.Fatalf("the block offers %d buttons and %d secondary, want 2 and 1", len(blk.buttons), len(blk.secondary))
	}
	for _, btn := range append(append([]larkcard.CardButton{}, blk.buttons...), blk.secondary...) {
		if btn.Extra["inline"] != inlineDecisionMark {
			t.Errorf("button %q does not carry the in-card mark", btn.Text)
		}
		if btn.Extra["run_id"] == "" || btn.Extra["req_id"] != "req-1" {
			t.Errorf("button %q names no run or pause: %v", btn.Text, btn.Extra)
		}
	}
	// The fallback's buttons are the same decision with the mark stripped, which is
	// what makes its click replace the message instead of rewriting a card.
	stripped := withoutInlineMark(blk.buttons[0].Extra)
	if _, ok := stripped["inline"]; ok {
		t.Error("the fallback button kept the mark that would make it rewrite a card")
	}
	if stripped["perm_subject"] != "pipeline" {
		t.Errorf("stripping the mark lost the subject: %v", stripped)
	}
}

// TestTheDecisionIDsStayInsideThePlatformsLimit pins the length of the ids a
// pause's buttons live under.
//
// Feishu caps an element_id at 20 characters, and cardkit enforces it at the one
// moment there is no way around it: when a component is added to a card that
// already exists. An id over the cap therefore does not fail visibly on the card —
// the pause falls back to a card of its own and the turn ends up with two, which
// is what happened on the machines this ran on: the row id grew a "_last" suffix
// and every approval arrived twice. The character class is checked on every insert
// by the transport double, which runs the same validator cardkit does.
func TestTheDecisionIDsStayInsideThePlatformsLimit(t *testing.T) {
	blk := &decisionBlock{
		buttons:   []larkcard.CardButton{approvalButton(buttonAllowText, "primary", PermAllow, nil)},
		secondary: []larkcard.CardButton{approvalButton(buttonAllowAllText, "default", PermAllowAll, nil)},
	}

	// The slots run past what any turn is likely to reach, because the ids have to
	// stay legal for however many times a pause comes back — that is the property,
	// not the two ids the common case happens to produce.
	for _, slot := range []int{1, 2, 9, 10, 99, 100, 999} {
		_, ids := decisionActionsElements(slot, blk, "session-key")
		if len(ids) != 2 {
			t.Fatalf("slot %d named %d components, want a row and a grant row: %v", slot, len(ids), ids)
		}
		for _, id := range ids {
			if len(id) > 20 {
				t.Errorf("slot %d: element id %q is %d characters, over the platform's 20", slot, id, len(id))
			}
			if !strings.HasPrefix(id, "decision_") {
				t.Errorf("slot %d: element id %q does not name its own group", slot, id)
			}
		}
		if ids[0] == ids[1] {
			t.Errorf("slot %d: both rows are under the id %q", slot, ids[0])
		}
	}
}
