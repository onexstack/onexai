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
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
)

// The inbound ladder.
//
// Every message the bot receives walks the same ordered rungs, and the first
// rung that has something to say decides the message's fate. Expressing it as a
// ladder rather than as a run of if-statements is what makes the order arguable,
// and two of the rungs earn their position:
//
//   - The pending-permission rung sits above both the command rung and the
//     prompt rung. An answer to a pending approval is usually a bare word
//     ("允许" = allow, "是" = yes), and a bare word is also a perfectly plausible prompt — so
//     whichever rung claims it first is what the user gets. Letting the prompt
//     rung claim it would start a second turn while the first one stays blocked
//     forever, waiting for an answer that has just been typed into a different
//     run.
//   - The command rung sits above the prompt rung because a message beginning
//     with "/" is addressed to the command tree, never to the model.
//
// Card actions are deliberately not a rung: they arrive through ProcessCard,
// whose response format is the card callback's own, and their action prefixes
// are unambiguous, so they cannot collide with anything here.
//
// No rung returns without counting itself. A drop that only logs is invisible
// from the outside, and "the bot ignored me" is precisely the report that logs
// cannot answer without already knowing which message to look for.
//
// No rung sends a chat message either. walk decides and reports how to act;
// handleInbound acts. The ladder is then readable, and testable, without a
// platform connection — and the one ordering question that matters most (does a
// paused run get its answer, or does the command tree get its command?) becomes a
// question about a returned value rather than about what arrived in a chat.
type funnelStep string

// The rungs, in the order they are tried. The values are metric labels and are
// part of the observable surface.
const (
	stepGuard     funnelStep = "guard"
	stepRecall    funnelStep = "recall"
	stepStale     funnelStep = "stale"
	stepAllowlist funnelStep = "allowlist"
	stepPerm      funnelStep = "pending_permission"
	stepContent   funnelStep = "content"
	stepCommand   funnelStep = "command"
	stepPrompt    funnelStep = "prompt"
)

// What became of a message at the rung that decided it. The step says where the
// message stopped; the outcome says whether anything was done about it.
const (
	outcomeDropped  = "dropped"
	outcomeHandled  = "handled"
	outcomeBuffered = "buffered"
	outcomeFailed   = "failed"
)

// mergeForwardType is Feishu's message type for a forwarded conversation
// ("合并转发", merge-forward). Its body is a reference to the constituent messages rather than
// the messages themselves, so it is expanded separately from ordinary content.
const mergeForwardType = "merge_forward"

// inbound is one message as the ladder sees it.
type inbound struct {
	// Event is the parsed platform event.
	Event *im.Event
	// Now is the arrival time. It is a field rather than a call to time.Now so
	// the staleness rung can be tested without sleeping.
	Now time.Time
	// Content decodes the message body, returning nil when the body holds
	// nothing the bot can act on. Rungs above the content rung may look at the
	// body to decide whether they claim the message — the permission rung does —
	// so the decode is memoized here rather than repeated by each of them.
	Content func() *MsgContent

	decoded bool
	body    *MsgContent

	// marks records when resolving the sender, conversation and roles finished.
	// They are stamped for every message but read only by the command rung,
	// which is where a commandTimings is built — see commandTimings.
	marks inboundMarks
}

// newInbound wraps a parsed message event as a ladder job. now is a parameter
// rather than a call inside so the staleness rung can be tested directly.
func newInbound(ev *im.Event, now time.Time) *inbound {
	in := &inbound{Event: ev, Now: now}
	in.Content = func() *MsgContent {
		return parseMsgContent(ev.Message.Content.Type, ev.Message.Content.Raw)
	}
	return in
}

// valid reports whether the event carries enough to act on. A message with no
// conversation cannot be answered, and reaching for the conversation ID of one
// is how this used to panic.
func (in *inbound) valid() bool {
	return in != nil && in.Event != nil && in.Event.Message != nil && in.Event.ConvID != ""
}

// content decodes the body at most once.
func (in *inbound) content() *MsgContent {
	if !in.decoded {
		in.body = in.Content()
		in.decoded = true
	}
	return in.body
}

// stale reports how late this message is and whether that is later than the
// bound. A message carrying no send time is never stale: an unparseable
// timestamp is a reason to answer someone, not a reason to ignore them.
func (in *inbound) stale(maxAge time.Duration) (time.Duration, bool) {
	if maxAge <= 0 {
		return 0, false
	}
	sent := in.Event.Message.CreateTime
	if sent.IsZero() {
		return 0, false
	}
	age := in.Now.Sub(sent)
	return age, age > maxAge
}

// handleInbound runs one message down the ladder: it asks walk to decide, then
// does what the decision says.
//
// The counting is done here rather than inside the rungs so a rung added later
// cannot forget it: walk reports its verdict, and every message produces exactly
// one data point.
func (b *botBiz) handleInbound(ctx context.Context, in *inbound) error {
	step, outcome, act, err := b.walk(ctx, in)
	countStep(ctx, step, outcome)
	if err != nil || act == nil {
		return err
	}
	return act()
}

// walk is the ladder. It reports the rung that decided the message's fate, what
// that rung did with it, and — for the rungs that hand work off — how to hand it
// off. The last part is what keeps this a decision rather than a monolith: the
// caller carries the verdict out, so the ladder itself can be read and tested
// without a platform connection.
//
// It reports the drops too, which is the point: from the outside a message the
// bot declines and a message it answers slowly look identical, and only the
// counter tells them apart.
func (b *botBiz) walk(ctx context.Context, in *inbound) (funnelStep, string, func() error, error) {
	// GUARD.
	if !in.valid() {
		logDrop(ctx, in, stepGuard, "no_conversation")
		return stepGuard, outcomeDropped, nil, nil
	}
	message := in.Event.Message

	// RECALL. The sender withdrew the message; a duplicate delivery may still be
	// in flight behind the recall.
	if b.isMessageRecalled(message.ID) {
		logDrop(ctx, in, stepRecall, "recalled")
		return stepRecall, outcomeDropped, nil, nil
	}

	// STALENESS. A websocket reconnect hands over everything the platform
	// re-delivered while the bot was away, which is not a queue of questions
	// anyone is still waiting on.
	if age, stale := in.stale(b.maxEventAge()); stale {
		logDrop(ctx, in, stepStale, "age="+age.Truncate(time.Second).String())
		return stepStale, outcomeDropped, nil, nil
	}

	dr, err := b.buildInboundRuntime(ctx, in.Event, &in.marks)
	if err != nil {
		return stepGuard, outcomeFailed, nil, err
	}
	ctx = runtime.Inject(ctx, dr)

	// IDENTITY. A sender the platform reported no user_id for cannot be authorized
	// and cannot own a record. That is not a fault in itself: a person chatting
	// with the bot from another tenant has no user_id here at all, because Lark
	// scopes a user_id to its tenant, so what is worth noticing is *which* senders
	// those are rather than that something went wrong. The rung is deliberately not
	// a drop — the conversation is still addressed (a direct reply falls back to the
	// open_id), the identity-bound commands refuse with a readable error, and the
	// counter tells "we did not get it" apart from "the bot ignored me".
	if dr.Identity() == "" {
		slog.WarnContext(ctx, "inbound sender has no user_id in this tenant: outside this tenant, or the app may not read user IDs",
			"conv_id", dr.Address(), "sender_open_id", dr.Handle(), "message_id", in.Event.Message.ID)
		metrics.M.RecordUnresolvedIdentity(ctx, "lark")
	}

	// ALLOWLIST. Three rules, one rung: the message is not for this bot.
	//
	// A list here is an *identity* list: it names people by user_id, which is the
	// one id an outsider does not have. So an unresolved identity cannot be on it,
	// whatever the list says — and that is reported under its own reason, because
	// "the list exists and this sender is not in the id space it names" is a
	// different fact from "this sender is not on the list". Only the first one is
	// invisible to whoever reads the list and wonders why the bot went quiet.
	switch {
	case !b.sr.AllowsSender(dr.Identity()):
		reason := "sender"
		if dr.Identity() == "" {
			reason = "sender_unresolved"
		}
		logDrop(ctx, in, stepAllowlist, reason)
		return stepAllowlist, outcomeDropped, nil, nil
	case !b.sr.AllowsChat(dr.Address()):
		logDrop(ctx, in, stepAllowlist, "chat")
		return stepAllowlist, outcomeDropped, nil, nil
	case b.sr.GroupOnly && dr.IsP2P():
		logDrop(ctx, in, stepAllowlist, "group_only")
		return stepAllowlist, outcomeDropped, nil, nil
	}

	// PENDING PERMISSION.
	if claimed, outcome, act := b.answerPendingPermission(ctx, dr, in); claimed {
		return stepPerm, outcome, act, nil
	}

	// CONTENT.
	if message.Content.Type == mergeForwardType {
		return b.answerMergeForward(ctx, dr, in)
	}
	mc := in.content()
	if mc == nil {
		logDrop(ctx, in, stepContent, "undecodable")
		return stepContent, outcomeDropped, nil, nil
	}
	if len(mc.ImageKeys) > 0 {
		if !shouldRouteToAI(dr) {
			logDrop(ctx, in, stepPrompt, "not_addressed")
			return stepPrompt, outcomeDropped, nil, nil
		}
		// Images are coalesced before being acknowledged, so the turn that
		// eventually runs belongs to the batch rather than to this message.
		b.bufferImage(ctx, dr, mc)
		return stepPrompt, outcomeBuffered, nil, nil
	}

	// One normalisation for both rungs below, and for both conversation types.
	// cleanFeishuAutoLinks is folded in because it is the same job — removing
	// decoration the platform added — and because a link's brackets are also
	// enough to keep a leading command from looking like a command.
	text := normalizeQuestion(cleanFeishuAutoLinks(mc.Text), dr.Mentions)

	// COMMAND.
	if strings.HasPrefix(text, "/") {
		// The timing record is built here rather than for every message: only a
		// command is worth a latency line, and a plain question's cost is
		// dominated by the model anyway.
		timings := &commandTimings{
			received: in.Now,
			session:  in.marks.session,
			roles:    in.marks.roles,
			runtime:  in.marks.runtime,
		}
		return stepCommand, outcomeHandled, func() error {
			return b.handleCommandMessage(ctx, dr, text, timings)
		}, nil
	}

	// PROMPT.
	//
	// A reply with no words of its own is still a question — "this one, look at
	// this" — so the emptiness test asks whether there is anything to answer
	// with at all, and a quoted message is something. Whether that quote can
	// actually be read takes a platform call to know and is left to the turn;
	// this rung only decides that the message is worth one.
	quotedID := dr.ParentMessageID()
	if text == "" && quotedID == "" {
		logDrop(ctx, in, stepPrompt, "empty")
		return stepPrompt, outcomeDropped, nil, nil
	}
	if !shouldRouteToAI(dr) {
		logDrop(ctx, in, stepPrompt, "not_addressed")
		return stepPrompt, outcomeDropped, nil, nil
	}
	return stepPrompt, outcomeHandled, func() error {
		return b.runTurnWithQuote(ctx, dr, quotedID, text, nil)
	}, nil
}

// shouldRouteToAI reports whether a message should reach the AI pipeline: a P2P
// conversation always replies, a group conversation only when the bot is
// @-mentioned. It is a leaf predicate at the prompt rung, not a gate on the
// whole pipeline — a "/command" from a group that never mentioned the bot is
// still a command.
func shouldRouteToAI(dr *runtime.DynamicRuntime) bool {
	return dr.IsP2P() || isMentioningHelper(dr)
}

// answerMergeForward expands a merged-forward message into text and answers it.
// The expansion fetches the constituent messages from the platform, so it
// happens only once the message has cleared every rung above.
func (b *botBiz) answerMergeForward(ctx context.Context, dr *runtime.DynamicRuntime, in *inbound) (funnelStep, string, func() error, error) {
	// nil mentions, deliberately. The expanded transcript is content the user
	// chose to forward: it belongs to other conversations, and a bot name inside
	// it is something somebody said, not a way of addressing this bot. There is
	// no mention metadata for it either, so the alternative would be matching the
	// configured bot name against the text — the one guess this file exists to
	// avoid. Passing the triggering message's mentions would not help: their
	// placeholders are not in the transcript.
	text := normalizeQuestion(b.formatMergeForward(ctx, in.Event.Message.ID), nil)
	if text == "" {
		logDrop(ctx, in, stepContent, "empty_merge_forward")
		return stepContent, outcomeDropped, nil, nil
	}
	if !shouldRouteToAI(dr) {
		logDrop(ctx, in, stepPrompt, "not_addressed")
		return stepPrompt, outcomeDropped, nil, nil
	}
	// A forwarded conversation that is itself a reply carries both: the
	// transcript was fetched to be answered, and the message it quotes is
	// context for it. Short-circuits when there is no quote to fetch.
	return stepPrompt, outcomeHandled, func() error {
		return b.runTurnWithQuote(ctx, dr, dr.ParentMessageID(), text, nil)
	}, nil
}

// approvalDecisions maps what a user types to the decision it stands for, in
// both languages the cards are written in. It is a closed vocabulary on purpose:
// an approval is a security boundary, so text that is not recognisably a
// decision is never guessed at. Guessing "allow" would run a tool nobody
// approved; guessing "deny" would refuse one somebody did.
var approvalDecisions = map[string]string{
	"allow": "allow", "允许": "allow", "同意": "allow", "可以": "allow", "是": "allow",
	"批准": "allow", "准许": "allow", "行": "allow", "好的": "allow", "好": "allow",
	"yes": "allow", "y": "allow", "ok": "allow", "sure": "allow",
	"deny": "deny", "拒绝": "deny", "不行": "deny", "不可以": "deny", "否": "deny",
	"不允许": "deny", "不要": "deny", "不准": "deny", "驳回": "deny", "别": "deny",
	"no": "deny", "n": "deny",
	"allow_all": "allow_all", "always": "allow_all", "总是允许": "allow_all", "全部允许": "allow_all",
	"以后都允许": "allow_all", "一直允许": "allow_all",
}

// cancelReasons are the words that withdraw a pending decision rather than answer
// it. They are matched whole, like the decisions above, and they are separate
// from that vocabulary because they mean the opposite of an answer: nothing is
// decided and the run does not continue.
//
// "停止" (stop) is deliberately not here. It is a command — /stop — and a word that
// stops the whole conversation should not also be something a run can read as an
// answer to its own question.
var cancelReasons = map[string]string{
	"取消": reasonCancelled, "算了": reasonCancelled, "不用了": reasonCancelled,
	"cancel": reasonCancelled, "withdraw": reasonCancelled,
}

// answerPendingPermission delivers a bare-text reply to the approval this
// conversation is waiting on. It reports whether it claimed the message, what
// became of it, and — for the two outcomes that have something to say back — how
// to say it.
//
// It claims only plain text that does not begin with "/". That guard is what
// keeps /stop, /help and every other command reachable while a run is paused —
// without it, the one moment a user most wants /stop is the moment it would be
// typed into the agent as the literal answer "stop".
func (b *botBiz) answerPendingPermission(ctx context.Context, dr *runtime.DynamicRuntime, in *inbound) (bool, string, func() error) {
	pending := b.pendingPermission(dr)
	if pending == nil {
		return false, "", nil
	}

	mc := in.content()
	if mc == nil || len(mc.ImageKeys) > 0 {
		return false, "", nil
	}
	answer := strings.TrimSpace(cleanFeishuAutoLinks(mc.Text))
	if answer == "" || strings.HasPrefix(answer, "/") {
		return false, "", nil
	}

	// A reply typed while a run is parked means one of three things, and which one
	// is decided here rather than guessed at downstream:
	//
	//   - a word that withdraws the decision cancels it and stops the run;
	//   - a recognised answer is delivered to the pause;
	//   - anything else is not an answer at all, and falls through to the prompt
	//     rung, where it takes the conversation over from the parked run.
	//
	// The third case is the one that used to be a re-prompt, and it is better for
	// it: a user who types something else has changed their mind about what they
	// want, and answering them with "没看懂这条回复" (didn't understand this reply) made them type it twice.
	if reason, ok := cancelReasons[strings.ToLower(answer)]; ok {
		return b.cancelPendingDecision(ctx, dr, pending, reason)
	}

	// A typed reply means one of two things, and which one is decided by what the
	// run is waiting on. A question takes the text as-is: the answer is both the
	// decision to carry on and the payload to carry on with, and only the agent
	// can say what an answer to its own question means. An approval is a closed
	// vocabulary — the engine compares it against allow, deny and allow_all — so
	// anything else is treated as a new request rather than guessed at, because
	// reading a decision of "allow" out of a "拒绝" (deny) would run the tool the user
	// refused.
	req := agent.AnswerRequest{
		RunID:     pending.RunID,
		RequestID: pending.RequestID,
		Decision:  agent.DecisionAllow,
	}
	line := ""
	if pending.Kind == agent.InterruptKindQuestion {
		// An @-mention is how this bot is addressed, so a mention while a question
		// is pending is someone asking something new rather than answering. Without
		// one the text is the answer: typing it is how a multi-select question
		// ("回复选项编号", reply with the option number) is answered at all, and how anyone answers from a phone
		// rather than aiming at a button.
		if isMentioningHelper(dr) {
			return false, "", nil
		}
		// Keyed by the interrupt point the question came from. A backend that
		// reports none keeps the answer under the empty key: the key is what an
		// addressing backend looks up, and one that does not address its pauses
		// reads the answer as text.
		req.Answers = map[string]string{pending.Address: answer}
		line = decisionLineChosen(answer, time.Now())
	} else {
		decision, ok := approvalDecisions[strings.ToLower(answer)]
		if !ok {
			// Not an answer: it is a new instruction, and runAI takes the
			// conversation over from the parked run before it starts it.
			return false, "", nil
		}
		req.Decision = decision
		line = decisionOutcomeLine(decision, pending.ToolName, time.Now())
	}

	// Claimed and armed before the answer goes out: the answer resumes the run on a
	// goroutine of its own, which can end the turn before this one gets to write
	// anything (see parkedDecision). Typed answers reach the same card as clicked
	// ones do.
	parked := b.parkedDecision(dr.Address(), pending.RunID)
	armed := parked != nil && parked.armDecision(line)

	if err := b.answerRun(ctx, dr.Address(), req); err != nil {
		if armed {
			parked.disarmDecision()
		}
		// The run is gone, or the answer could not be delivered. Say so rather
		// than falling through to the prompt rung: the text was a reply to a
		// question, and answering it as a fresh prompt would answer something the
		// user did not ask. The approval stays pending so they can try again when
		// the failure is one that can be retried.
		slog.WarnContext(ctx, "failed to deliver an approval answer typed as text",
			"conv_id", dr.Address(), "run_id", pending.RunID, "error", err)
		return true, outcomeFailed, func() error {
			b.notice(ctx, dr, approvalFailureToast(err))
			return nil
		}
	}

	// Cleared by run ID: an answer to a question that has since been superseded
	// must not erase the question that replaced it.
	b.clearPendingPermission(dr.Address(), pending.RunID)
	// Only now that the answer has been delivered: the buttons that were on the
	// card were the only way to answer the pause, and a card that lost them to a
	// delivery that failed would strand the run.
	if armed {
		parked.resolveDecision(ctx)
	}
	return true, outcomeHandled, nil
}

// cancelPendingDecision withdraws the decision a run is parked on, because the
// user said so in words. The run is stopped rather than answered, which is what
// makes the guarantee hold: the tool it was waiting on is not executed.
func (b *botBiz) cancelPendingDecision(
	ctx context.Context, dr *runtime.DynamicRuntime, pending *session.PendingPermission, reason string,
) (bool, string, func() error) {
	return true, outcomeHandled, func() error {
		// The callback is not what is being answered here — a text message is —
		// but the writes that follow it outlive this handler, so they get a
		// context of their own for the same reason a stop click does.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionTimeout)
		defer cancel()

		if err := b.stopRun(cctx, dr.Address(), pending.RunID); err != nil {
			slog.WarnContext(ctx, "failed to stop a parked run the user cancelled",
				"conv_id", dr.Address(), "run_id", pending.RunID, "error", err)
			b.notice(ctx, dr, approvalFailureToast(err))
			return nil
		}
		b.clearPendingPermission(dr.Address(), pending.RunID)
		b.closeParkedTurn(cctx, dr.Address(), pending.RunID, reason)
		b.notice(ctx, dr, "已取消，本次执行未继续。")
		return nil
	}
}

// pendingPermission returns the approval this conversation is waiting on, or
// nil. It looks up an already-loaded conversation: reading what the bot is
// waiting for must not bring a conversation into existence.
func (b *botBiz) pendingPermission(dr *runtime.DynamicRuntime) *session.PendingPermission {
	sess := b.liveConversation(dr.Address())
	if sess == nil {
		return nil
	}
	return sess.PendingPermission()
}

// maxEventAge returns the staleness bound, or zero when none is configured.
func (b *botBiz) maxEventAge() time.Duration {
	return b.sr.MaxEventAge
}

// logDrop records the rungs that end a message without a reply. Dropping is not
// a failure — the bot was not addressed, or the message is not for it — so
// nothing is returned and nothing is told to the user. That silence is exactly
// why it is logged and counted: from the outside it is indistinguishable from a
// bot that is broken.
func logDrop(ctx context.Context, in *inbound, step funnelStep, reason string) {
	attrs := []any{"step", string(step), "reason", reason}
	if in != nil && in.Event != nil {
		attrs = append(attrs, "conv_id", in.Event.ConvID)
		if in.Event.Message != nil {
			attrs = append(attrs, "message_id", in.Event.Message.ID)
		}
	}
	slog.InfoContext(ctx, "inbound message dropped", attrs...)
}

// countStep records the rung that decided a message's fate.
func countStep(ctx context.Context, step funnelStep, outcome string) {
	metrics.M.RecordFunnelStep(ctx, string(step), outcome)
}
