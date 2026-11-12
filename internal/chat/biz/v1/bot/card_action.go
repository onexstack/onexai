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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// CardNavHandler renders the next card for a "nav:"/"act:" card action. It
// receives the raw action value and the receiver of the originating card, and
// returns the card to display in place (nil means "no update").
//
// The raw event travels alongside the receiver because some of what a card has
// to say lives only there: the operator's open_id is on it, and a card that
// reports what the conversation's declaration resolves to cannot answer without
// it. The receiver stays separate rather than being derived from the event, so a
// handler that does not care about the raw callback does not have to parse one.
type CardNavHandler func(ctx context.Context, event *callback.CardActionTriggerEvent, action string, recv *v1.Receiver) *larkcard.Card

// cardNavTimeout bounds a synchronous card navigation; past it the callback
// returns a loading toast and the result is applied asynchronously via a card
// patch.
const cardNavTimeout = 2500 * time.Millisecond

// stopRequestTimeout bounds the stop request issued from a card callback. The
// callback itself has a 3s budget, and an answer that arrives after it is
// dropped, so the stop has to either succeed inside this window or report that
// it did not.
const stopRequestTimeout = 2 * time.Second

// ProcessCard processes Lark card interaction events. It recognizes the
// following action prefixes carried in the button value (or select option):
//
//	nav:   — render the next card and update the original card in place
//	act:   — execute an action, then render and update the card in place
//	cmd:   — dispatch the remainder as a slash command
//
// The "askai_stop" action (the streaming-card stop button) stops the run in
// oai-agent-apiserver and drops anything queued behind it in this conversation.
func (b *botBiz) ProcessCard(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, nil
	}

	action := extractCardActionValue(event)
	if action == "" {
		// A click can land here with no action at all, meaning the button's
		// callback value did not survive the trip from the card. That is a
		// different failure from an action we do not recognize, and both used to
		// be silent — which is indistinguishable from a click that never arrived.
		slog.InfoContext(ctx, "card action carries no action value", cardActionValueAttrs(event.Event.Action)...)
		return nil, nil
	}

	recv := b.parseReceiverFromCardEvent(event)
	sessionKey := recv.Id
	messageID := recv.MessageId

	switch {
	case action == StopCardAction:
		return b.handleStopAction(ctx, event, sessionKey)
	case strings.HasPrefix(action, "perm:"):
		return b.handlePermAction(ctx, event, action, sessionKey)
	case strings.HasPrefix(action, customApprovePrefix):
		return b.handleCustomApproveAction(ctx, event, action, recv)
	case strings.HasPrefix(action, "askq:"):
		return b.handleAskQuestionAction(ctx, event, action, sessionKey)
	case strings.HasPrefix(action, "nav:"), strings.HasPrefix(action, "act:"):
		// Remember this card's message ID so an async navigation can refresh it.
		if messageID != "" {
			b.recordCardMessageID(sessionKey, messageID)
		}
		return b.handleCardNavigation(ctx, event, action, recv)
	case strings.HasPrefix(action, "cmd:"):
		return b.handleCardCommand(ctx, event, action, sessionKey)
	case action == cardCreateTicketAction || strings.HasPrefix(action, cardCreateTicketAction+":"):
		return b.handleCreateTicketAction(ctx, event, action, sessionKey)
	default:
		slog.InfoContext(ctx, "unrecognized card action",
			append([]any{"action", action, "conv_id", sessionKey}, cardActionValueAttrs(event.Event.Action)...)...)
		return nil, nil
	}
}

// cardActionValueAttrs describes the raw callback payload of a card action. It
// is logged whole rather than summarized because the shape is what goes wrong:
// a button whose value lands under a field this code does not read looks
// identical to a button that carries nothing.
func cardActionValueAttrs(action *callback.CallBackAction) []any {
	if action == nil {
		return []any{"action_value", nil}
	}
	return []any{
		"action_tag", action.Tag,
		"action_name", action.Name,
		"action_option", action.Option,
		"action_value", action.Value,
	}
}

// extractCardActionValue resolves the logical action from a card callback,
// tolerating the varying shapes the SDK may unmarshal: a button value's
// "action" key, a select_static's Option, or a form submit button's Name.
func extractCardActionValue(event *callback.CardActionTriggerEvent) string {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return ""
	}
	if action, _ := event.Event.Action.Value["action"].(string); action != "" {
		return action
	}
	// select_static callbacks put the chosen value in Option.
	if event.Event.Action.Option != "" {
		return event.Event.Action.Option
	}
	// Delete-mode form submit/cancel buttons carry their action in Name.
	switch event.Event.Action.Name {
	case "delete_mode_submit":
		return "act:/delete-mode form-submit"
	case "delete_mode_cancel":
		return "act:/delete-mode cancel"
	}
	return ""
}

// handleCardNavigation drives "nav:"/"act:" actions: it asks the (pluggable)
// CardNavHandler for the next card and returns it as an in-place update. When
// the handler does not answer within cardNavTimeout, a loading toast is
// returned and the eventual card is applied asynchronously.
func (b *botBiz) handleCardNavigation(ctx context.Context, event *callback.CardActionTriggerEvent, action string, recv *v1.Receiver) (*callback.CardActionTriggerResponse, error) {
	if b.cardNav == nil {
		return nil, nil
	}
	sessionKey := recv.Id

	done := make(chan *larkcard.Card, 1)
	go func() { done <- b.cardNav(ctx, event, action, recv) }()

	select {
	case card := <-done:
		if card == nil {
			return nil, nil
		}
		return &callback.CardActionTriggerResponse{
			Card: &callback.Card{Type: "raw", Data: larkcard.RenderCardMap(card, sessionKey)},
		}, nil
	case <-time.After(cardNavTimeout):
		go func() {
			card := <-done
			if card == nil {
				return
			}
			if msgID := b.cardMessageID(sessionKey); msgID != "" {
				if err := b.PatchCardMessage(context.Background(), msgID, sessionKey, card); err != nil {
					slog.Warn("async card refresh failed", "action", action, "error", err)
				}
			}
		}()
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "info", Content: "Loading... / 加载中..."},
		}, nil
	}
}

// handleCardCommand dispatches a "cmd:" action as a slash command. The command
// runs asynchronously so the card callback returns immediately; command output
// is delivered through the responder, which patches the card in place when the
// receiver carries a message ID (existing behavior of the Lark responder).
//
// When the button carries an "after_click" map, it returns an in-place card
// replacement (title/color/markdown/link) instead of nil, mirroring cc-connect's
// after-click confirmation card.
func (b *botBiz) handleCardCommand(ctx context.Context, event *callback.CardActionTriggerEvent, action, sessionKey string) (*callback.CardActionTriggerResponse, error) {
	cmdText := strings.TrimSpace(strings.TrimPrefix(action, "cmd:"))
	if cmdText == "" {
		return nil, nil
	}

	runtime.GoSafe(ctx, "lark.card_command.dispatch", func() {
		dr, err := b.BuildCommandContext(ctx, event)
		if err != nil {
			return
		}
		cctx := runtime.Inject(ctx, dr)
		if err := b.handleCommandMessage(cctx, dr, "/"+cmdText, nil); err != nil {
			slog.WarnContext(cctx, "card command execution failed", "cmd", cmdText, "error", err)
		}
	})

	ac, ok := event.Event.Action.Value["after_click"].(map[string]any)
	if !ok {
		return nil, nil
	}
	title, _ := ac["title"].(string)
	if title == "" {
		return nil, nil
	}
	color, _ := ac["color"].(string)
	if color == "" {
		color = "green"
	}
	cb := larkcard.NewCard().Title(title, color)
	if md, _ := ac["markdown"].(string); md != "" {
		cb.Markdown(md)
	}
	if linkText, _ := ac["link_text"].(string); linkText != "" {
		if linkURL, _ := ac["link_url"].(string); linkURL != "" {
			cb.Divider().Markdown("[" + linkText + "](" + linkURL + ")")
		}
	}
	return &callback.CardActionTriggerResponse{
		Card: &callback.Card{Type: "raw", Data: larkcard.RenderCardMap(cb.Build(), sessionKey)},
	}, nil
}

// handleStopAction handles the streaming card's stop button: it cancels the run
// this conversation is executing and drops whatever was queued behind it.
//
// The card is finalized by the runAI goroutine's CloseStreamingCard once the run
// terminates, so no card response is rendered here, only a toast. What that
// toast says is the point of the whole function: a receipt that always claims
// success tells someone whose answer is still streaming that it stopped, and
// they stop looking for the real failure.
func (b *botBiz) handleStopAction(ctx context.Context, event *callback.CardActionTriggerEvent, sessionKey string) (*callback.CardActionTriggerResponse, error) {
	value := event.Event.Action.Value
	runID, _ := value["run_id"].(string)
	declaredKey, _ := value["session_key"].(string)
	chatType, _ := value["chat_type"].(string)

	logAttrs := []any{
		"run_id", runID,
		"conv_id", sessionKey,
		"declared_session_key", declaredKey,
		"chat_type", chatType,
	}

	if runID == "" {
		logAttrs = append(logAttrs, "stop_result", "no_run_id")
		slog.InfoContext(ctx, "stop ignored: the callback value carries no run id", logAttrs...)
		return stopToast("warning", "这张卡片没有正在进行的任务"), nil
	}

	// A card outlives the run it was rendered for: the button stays clickable
	// long after the turn that put it there has ended. Refusing when the
	// conversation is *known* to be idle keeps a leftover card from cancelling
	// the next turn, and is a truer answer for a run that has already finished.
	//
	// The check is skipped when this process simply does not hold the
	// conversation — no session manager, or one that never saw it — because "I do
	// not know" is not "nothing is running", and refusing there would break stop
	// on those deployments rather than make it honest.
	if sess := b.liveConversation(sessionKey); sess != nil {
		if current := sess.CurrentRun(); current != runID {
			logAttrs = append(logAttrs, "stop_result", "stale_card", "current_run_id", current)
			slog.InfoContext(ctx, "stop ignored: the clicked card belongs to an earlier run", logAttrs...)
			return stopToast("warning", "当前没有正在生成的回答"), nil
		}
	}

	// The callback budget is 3s, so the stop gets a 2s slice of it and runs on a
	// detached context: if Lark has already given up on this response, the stop
	// the user asked for should still be delivered.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopRequestTimeout)
	defer cancel()

	stopResult := "accepted"
	var stopErr error
	if stopErr = b.stopRun(stopCtx, sessionKey, runID); stopErr != nil {
		stopResult = "failed"
		if errors.Is(stopErr, errStopNoSession) {
			stopResult = "no_session"
		}
	}

	// A turn parked on a decision has no stream left to end in, so its card would
	// stand there offering buttons for a run that is now gone unless it is closed
	// here. This is the one ending no event carries, and the stop click is the only
	// place that can see it.
	//
	// Only once the stop was accepted: a request that never reached the run leaves
	// the pause standing, and closing the card would take away the only buttons
	// that can still answer it.
	if stopResult == "accepted" {
		b.closeParkedTurn(stopCtx, sessionKey, runID, reasonStopped)
	}

	// "停止" (stop) means stop this conversation, not just the run in flight: the
	// messages queued behind it were sent before the user asked for silence, and
	// answering them now would be answering a question they have withdrawn.
	// Breaking the lock is what tells the running turn's drain loop to drop them.
	queueDepth := 0
	if sess := b.liveConversation(sessionKey); sess != nil {
		if queueDepth = sess.QueueDepth(); queueDepth > 0 {
			slog.InfoContext(ctx, "discarding queued turns on stop", "conv_id", sessionKey, "discarded", queueDepth)
		}
		sess.Lock().ForceUnlock()
	}

	logAttrs = append(logAttrs, "stop_result", stopResult, "queue_depth", queueDepth, "error", stopErr)
	if stopResult == "accepted" {
		slog.InfoContext(ctx, "stop accepted", logAttrs...)
		return stopToast("info", "已停止生成"), nil
	}
	// The run is still going, so the user has to be told rather than left to
	// assume it heard them.
	slog.WarnContext(ctx, "stop run failed", logAttrs...)
	return stopToast("error", "停止请求未送达，请重试"), nil
}

// stopToast builds the toast a stop click answers with.
func stopToast(typ, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: typ, Content: content},
	}
}

// errStopNoSession marks a stop that had no conversation to deliver to, as
// opposed to one the backend refused. The two are worth telling apart in the
// log: the first is a deployment without a session manager, the second is a run
// that would not stop.
var errStopNoSession = errors.New("no agent session for this conversation")

// stopRun asks the backend to stop a run. It is the one place a stop is issued,
// so the stop button, the cancel words and the takeover of a parked turn cannot
// come to mean different things.
func (b *botBiz) stopRun(ctx context.Context, sessionKey, runID string) error {
	conv, _, err := b.agentSession(ctx, sessionKey)
	if err != nil {
		return fmt.Errorf("%w: %w", errStopNoSession, err)
	}
	return conv.Stop(ctx, runID)
}

// closeParkedTurn closes the card of a run this conversation is parked on, after
// the stop has been accepted. reason is what its decision log says became of the
// decision nobody made; an empty one records nothing, for a caller that has
// already written its own line.
//
// A turn parked on a decision has no stream left to end in, so without this its
// card would stand there offering buttons for a run that is gone. Every path that
// stops such a run comes through here, which is what makes that one behaviour
// rather than four.
func (b *botBiz) closeParkedTurn(ctx context.Context, sessionKey, runID, reason string) {
	parked := b.suspendedTurn(sessionKey)
	if parked == nil || parked.runID != runID {
		return
	}
	if reason != "" {
		parked.cancelDecision(ctx, reason)
	}
	parked.stoppedByUser(reason)
}

// takeOverParkedRun cancels the turn this conversation is parked on, because a
// new message is arriving and the conversation belongs to it now.
//
// A parked run is not left waiting while the conversation moves on. Two reasons,
// and the second is the one that decides it: its continuation would render into a
// card the conversation has moved past, and the tool it is parked on is a tool
// nobody has approved — Stop deletes the parked checkpoint, which is what makes
// "the gated tool does not run" true rather than merely intended.
//
// Only a run that is still parked can be taken over. A turn that was parked and
// has since been answered is running again, and a turn that is running is not
// this function's to end: ending it abandons work the user has just approved and
// closes its card as though they had walked away from it. The distinction has to
// be made here because the registry of parked turns is only cleared when a turn
// finishes, so a resumed turn stays reachable by conversation for the rest of its
// life — long enough for the old code to stop it, close its card with a bare
// "已停止" (stopped) and start a fresh card beside it.
//
// A message that arrives during a resumed half is a separate matter, and this
// gate neither queues it nor claims to: the continuation runs detached from the
// conversation's lock (see the answer path in pkg/agent), so it is admitted
// beside the running half rather than behind it. Queuing it would mean holding
// the lock across the resumed half, which nothing does today.
//
// It runs before the new turn is admitted, so that the old turn's release of the
// conversation cannot race the new turn's claim on it.
func (b *botBiz) takeOverParkedRun(ctx context.Context, dr *runtime.DynamicRuntime) {
	if dr == nil || dr.Receiver == nil {
		return
	}
	parked := b.suspendedTurn(dr.Address())
	if parked == nil {
		return
	}
	if !parked.paused() {
		return
	}
	runID := parked.runID
	slog.WarnContext(ctx, "taking over a run parked on a decision",
		"conv_id", dr.Address(), "run_id", runID, "message_id", dr.MessageID())

	// The new message is about to be answered, so this is a courtesy rather than a
	// question: if the stop cannot be delivered the takeover still goes ahead —
	// the alternative is refusing to answer the user at all — and what is left is
	// a run whose continuation will find the card closed.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionTimeout)
	defer cancel()
	if err := b.stopRun(stopCtx, dr.Address(), runID); err != nil {
		slog.WarnContext(ctx, "taking over a parked run failed to stop it",
			"conv_id", dr.Address(), "run_id", runID, "error", err)
	}
	b.clearPendingPermission(dr.Address(), runID)
	b.closeParkedTurn(stopCtx, dr.Address(), runID, reasonTakeover)
}

// handlePermAction processes a "perm:" approval button click: it answers the
// paused run through the backend port, and then records on the card what the
// decision was.
//
// Two card shapes answer to this. A decision drawn on the turn's own card is
// rewritten in place — the buttons are deleted and the outcome is appended to
// the log above them — and the run's answer keeps streaming into that same card.
// A decision sent as a card of its own (the fallback, when the live card would
// not take the insert) is a message with nothing else in it, so the click
// replaces it with a confirmation, as this always did.
func (b *botBiz) handlePermAction(
	ctx context.Context, event *callback.CardActionTriggerEvent, action, sessionKey string,
) (*callback.CardActionTriggerResponse, error) {
	value := event.Event.Action.Value
	runID, _ := value["run_id"].(string)
	requestID, _ := value["req_id"].(string)
	label, _ := value["perm_label"].(string)
	subject, _ := value["perm_subject"].(string)
	color, _ := value["perm_color"].(string)
	body, _ := value["perm_body"].(string)
	inline, _ := value["inline"].(string)

	// The decision words are the backend's vocabulary, carried through the port
	// unchanged. An action that is none of these is not an approval at all, so
	// nothing is sent and nothing is claimed.
	decision, ok := permDecisions[action]
	if !ok {
		slog.WarnContext(ctx, "approval click carries a decision this build does not know",
			"run_id", runID, "action", action)
		return nil, nil
	}
	if !b.answererAllowed(sessionKey, clickerID(event)) {
		slog.InfoContext(ctx, "approval click from someone other than the asker",
			"run_id", runID, "clicker", clickerID(event))
		return stopToast("warning", "只有发起本次对话的人（或 SRE）可以批准"), nil
	}
	if b.decisionExpired(sessionKey, runID) {
		slog.InfoContext(ctx, "approval click arrived after the wait ran out", "run_id", runID, "action", action)
		return stopToast("warning", decisionExpiredToast), nil
	}

	// The card this decision is drawn on is claimed before the answer goes out, and
	// the outcome is armed with it: the answer resumes the run on a goroutine of
	// its own, so a click that waited until afterwards to look for its card could
	// find the run already over and write nothing anywhere. Armed is not drawn —
	// the card is untouched until the delivery succeeds.
	parked := b.parkedDecision(sessionKey, runID)
	armed := parked != nil && parked.armDecision(decisionOutcomeLine(decision, subject, time.Now()))

	if err := b.answerRun(ctx, sessionKey, agent.AnswerRequest{
		RunID:     runID,
		RequestID: requestID,
		Decision:  decision,
	}); err != nil {
		// The run is not resumed, so it is still waiting: the question stays
		// pending and the user can answer it again. The card is left as it was —
		// writing a decision onto it, or replacing it with a green confirmation,
		// would report a decision that was never delivered, on the one card that
		// still has working buttons. The armed outcome goes with it, so the log
		// cannot end up claiming a decision the backend never received.
		if armed {
			parked.disarmDecision()
		}
		slog.WarnContext(ctx, "approval action failed", "run_id", runID, "action", action, "error", err)
		return stopToast("error", approvalFailureToast(err)), nil
	}
	b.clearPendingPermission(sessionKey, runID)

	if inline == inlineDecisionMark {
		// Recorded only now that the answer has been delivered. A click may land on
		// a process that is not holding the turn — the two are not the same thing —
		// or on one whose turn ended while the answer was in flight, in which case
		// the answer stands on its own and there is no card of ours to write to.
		if armed {
			parked.resolveDecision(ctx)
		}
		return stopToast("info", decisionToast(decision, label)), nil
	}

	card := renderApprovalResultCard(label, color, body)
	return &callback.CardActionTriggerResponse{
		Card: &callback.Card{Type: "raw", Data: larkcard.RenderCardMap(card, sessionKey)},
	}, nil
}

// inlineDecisionMark is what a decision button carries to say it is drawn on the
// turn's own card. Without it the click cannot tell whether the message it came
// from is the whole decision or part of a card that is still streaming.
const inlineDecisionMark = "1"

// decisionToast is what a click on an in-card decision is told. The card itself
// carries the outcome, so the toast is one short confirmation.
func decisionToast(decision, label string) string {
	if label != "" {
		return larkcard.GlyphDone + " " + label
	}
	if decision == agent.DecisionDeny {
		return buttonDenyText
	}
	return buttonAllowText
}

// decisionExpired reports whether the run this click answers is a pause whose
// wait has already run out. Its card has been closed and its buttons taken away;
// a click that arrives anyway must not bring the turn back, because the context
// it would write into is dead and the run's checkpoint is gone.
//
// Two records answer it, and both are needed. A turn still registered as parked
// answers for the moment between its wait running out and its card being closed —
// the click that races the expiry. The conversation's note of the pause it let
// expire answers afterwards, when the turn is gone. What neither covers — a click
// for a run this process never held — is deliberately not treated as expired: on a
// deployment with several instances the click can land on one that is not holding
// the turn, and the answer it carries is still good, so it is delivered and the
// backend decides.
func (b *botBiz) decisionExpired(sessionKey, runID string) bool {
	if runID == "" {
		return false
	}
	if parked := b.suspendedTurn(sessionKey); parked != nil && parked.runID == runID && parked.expiredWait() {
		return true
	}
	return b.pauseExpired(sessionKey, runID)
}

// parkedDecision returns the turn a click's answer belongs to, or nil when this
// process is not holding it.
//
// It is read before the answer is delivered rather than after, and that order is
// the point: the answer resumes the run on a goroutine of its own, and a run that
// finishes quickly ends the turn — clearing the entry this would have looked up —
// before the click's goroutine gets another turn. Looking it up afterwards made
// the card write a race the click could lose, which left the reader holding
// buttons for a decision that had already been made.
func (b *botBiz) parkedDecision(sessionKey, runID string) *aiTurn {
	parked := b.suspendedTurn(sessionKey)
	if parked == nil || parked.runID != runID || !parked.inlineDecisionOn() {
		return nil
	}
	return parked
}

// handleAskQuestionAction answers a run paused on an AskUserQuestion interrupt
// with the selected option. Like an approval, the card it came from decides what
// happens to the card: a decision drawn on the turn's own card is recorded there,
// and a question sent as a card of its own is replaced by a confirmation.
func (b *botBiz) handleAskQuestionAction(
	ctx context.Context, event *callback.CardActionTriggerEvent, action, sessionKey string,
) (*callback.CardActionTriggerResponse, error) {
	value := event.Event.Action.Value
	label, _ := value["askq_label"].(string)
	if label == "" {
		label = strings.TrimPrefix(action, "askq:")
	}
	question, _ := value["askq_question"].(string)
	runID, _ := value["run_id"].(string)
	requestID, _ := value["req_id"].(string)
	inline, _ := value["inline"].(string)
	// The interrupt point the answer belongs to. Without it the answer reaches
	// the pause but names no question, and the run pauses again on the same
	// question for ever.
	addr, _ := value["askq_addr"].(string)

	if runID == "" {
		return nil, nil
	}
	if b.decisionExpired(sessionKey, runID) {
		slog.InfoContext(ctx, "answer arrived after the wait ran out", "run_id", runID, "answer", label)
		return stopToast("warning", decisionExpiredToast), nil
	}

	// Claimed and armed before the answer goes out, for the same reason as an
	// approval: the resumed run starts immediately and can end the turn before
	// this goroutine gets to write anything (see parkedDecision).
	parked := b.parkedDecision(sessionKey, runID)
	armed := parked != nil && parked.armDecision(decisionLineChosen(label, time.Now()))

	// The decision is "allow" because the answer is the payload, not a security
	// decision: a question interrupt asks what to do, not whether it may.
	// Answers is keyed by the address, which is what the backend resumes by.
	err := b.answerRun(ctx, sessionKey, agent.AnswerRequest{
		RunID:     runID,
		RequestID: requestID,
		Decision:  agent.DecisionAllow,
		Answers:   map[string]string{addr: label},
	})
	if err != nil {
		if armed {
			parked.disarmDecision()
		}
		// Unlike a permission, the answer is not recoverable by asking again:
		// the card's options are the answer, and the run has moved on. Say the
		// delivery failed rather than leaving the user to guess.
		slog.WarnContext(ctx, "resume run with answer failed", "run_id", runID, "error", err)
		return stopToast("error", approvalFailureToast(err)), nil
	}
	b.clearPendingPermission(sessionKey, runID)

	if inline == inlineDecisionMark {
		if armed {
			parked.resolveDecision(ctx)
		}
		return stopToast("info", larkcard.GlyphDone+" 已选择："+label), nil
	}

	cb := larkcard.NewCard().Title(larkcard.GlyphDone+" "+label, "green")
	if question != "" {
		cb.Markdown(question)
	}
	cb.Markdown("**→ " + label + "**")
	return &callback.CardActionTriggerResponse{
		Card: &callback.Card{Type: "raw", Data: larkcard.RenderCardMap(cb.Build(), sessionKey)},
	}, nil
}

// answerRun delivers an answer to whatever run this conversation has paused.
func (b *botBiz) answerRun(ctx context.Context, sessionKey string, req agent.AnswerRequest) error {
	conv, _, err := b.agentSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	return conv.AnswerPermission(ctx, req)
}

// clickerID is the open_id of whoever clicked a card button.
func clickerID(event *callback.CardActionTriggerEvent) string {
	if event == nil || event.Event == nil || event.Event.Operator == nil {
		return ""
	}
	return event.Event.Operator.OpenID
}

// approvalFailureToast explains why an answer did not reach the run, in the terms
// the person who clicked can act on. A pause that is no longer waiting is the
// common case — a card outlives the pause it was rendered for — and it is not a
// failure of theirs, so it does not read like one.
func approvalFailureToast(err error) string {
	if errors.Is(err, agent.ErrNotPending) {
		return "该请求已处理或已失效，请重新发起"
	}
	if errors.Is(err, agent.ErrBusy) {
		return "上一条消息还在处理中，请稍候再试"
	}
	return "审批未能送达，请重试"
}

// decisionExpiredToast is what a click on an expired decision is told. It is not
// a failure of theirs — they simply came back after the wait had run out — so it
// says what to do rather than what went wrong.
const decisionExpiredToast = "这次确认已失效，请重新发起"

// recordCardMessageID tracks the most recent card message ID for a session key.
func (b *botBiz) recordCardMessageID(sessionKey, messageID string) {
	if sessionKey == "" || messageID == "" {
		return
	}
	b.cardMsgMu.Lock()
	b.cardMsgIDs[sessionKey] = messageID
	b.cardMsgMu.Unlock()
}

// cardMessageID returns the tracked card message ID for a session key.
func (b *botBiz) cardMessageID(sessionKey string) string {
	b.cardMsgMu.Lock()
	defer b.cardMsgMu.Unlock()
	return b.cardMsgIDs[sessionKey]
}
