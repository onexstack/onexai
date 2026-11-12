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

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/media"
)

// aiTurnTimeout bounds a single AI turn so a stuck agent can't leave the
// goroutine (and its streaming card) hanging forever. It is the fallback when no
// session manager is configured; the configured turn timeout otherwise wins.
const aiTurnTimeout = 5 * time.Minute

// runAI admits one AI turn for the conversation behind dr, then runs it.
//
// Admission comes first, and the streaming card is created only once the turn is
// actually running. That ordering is the point: creating the card before asking
// whether the conversation is free (as this used to) means the second of two
// quickly-sent messages is answered with a card that turns red, with nothing to
// tell the user their message was understood and will be answered. A message
// that arrives mid-turn is queued and acknowledged instead.
func (b *botBiz) runAI(ctx context.Context, dr *runtime.DynamicRuntime, question string, images []media.Ref) error {
	if question == "" && len(images) == 0 {
		return nil
	}

	// One message, one turn, one card — claimed here because here is where a turn
	// begins, and a turn is the only thing that can draw a card. A duplicate
	// delivery of a message already answered stops on this line, before it can
	// take the conversation over from a running turn or take a queue slot behind
	// one. See claimTrigger for why the identity claimed is the message.
	sessionKey, messageID := "", ""
	if dr != nil && dr.Receiver != nil {
		sessionKey, messageID = dr.Address(), dr.MessageID()
	}
	if !b.claimTrigger(sessionKey, messageID) {
		slog.WarnContext(ctx, "duplicate trigger dropped: this message already produced a turn",
			"conv_id", sessionKey, "message_id", messageID, "question", question)
		metrics.M.RecordDuplicateTrigger(ctx)
		return nil
	}

	// Before this message is admitted, it takes the conversation over from a turn
	// that is parked on a decision. Here rather than in the funnel because a
	// coalesced image batch reaches this function without passing through it, and
	// a batch is a new request as much as a typed sentence is.
	b.takeOverParkedRun(ctx, dr)

	turn := func(turnCtx context.Context, _ uint64) error {
		return b.runAITurn(turnCtx, dr, question, images)
	}

	sess, err := b.conversation(dr.Address())
	if err != nil {
		return err
	}
	if sess == nil {
		// No session manager: run the turn inline, unserialised.
		return turn(ctx, 0)
	}

	queued, err := sess.Submit(ctx, turn)
	switch {
	case errors.Is(err, session.ErrQueueFull):
		slog.InfoContext(ctx, "AI turn rejected: turn queue is full", "conv_id", dr.Address())
		metrics.M.RecordAIRequestError(ctx, "queue_full")
		return b.sendNotBusyNotice(ctx, dr, "这个会话排队已满，请稍后再发。")
	case err != nil:
		return err
	case queued:
		slog.InfoContext(ctx, "AI turn queued behind a running turn",
			"conv_id", dr.Address(), "queue_depth", sess.QueueDepth())
		return b.sendNotBusyNotice(ctx, dr, "已收到，正在排队中，上一条消息处理完后回答你。")
	}
	return nil
}

// conversation returns the session for key, or nil when no session manager is
// configured — which is the case for callers that only exercise routing.
func (b *botBiz) conversation(key string) (*session.Session, error) {
	if b.sr.Sessions == nil {
		return nil, nil
	}
	return b.sr.Sessions.Session(key)
}

// liveConversation returns the conversation for key only if it is already
// loaded, or nil. Stopping a turn must not bring a conversation into existence.
func (b *botBiz) liveConversation(key string) *session.Session {
	if b.sr.Sessions == nil {
		return nil
	}
	sess, _ := b.sr.Sessions.Lookup(key)
	return sess
}

// turnTimeout returns how long a single turn may run.
func (b *botBiz) turnTimeout() time.Duration {
	if b.sr.Sessions != nil {
		if d := b.sr.Sessions.TurnTimeout(); d > 0 {
			return d
		}
	}
	return aiTurnTimeout
}

// sendNotBusyNotice tells the user their message arrived but is not being
// answered yet.
func (b *botBiz) sendNotBusyNotice(ctx context.Context, dr *runtime.DynamicRuntime, text string) error {
	b.notice(ctx, dr, text)
	return nil
}

// notice tells the user something about their own message that they cannot see
// from the outside. A failure to deliver it is logged rather than returned: the
// message itself was handled, and a lost courtesy notice is not a failed request.
func (b *botBiz) notice(ctx context.Context, dr *runtime.DynamicRuntime, text string) {
	if dr == nil || dr.LarkServices == nil {
		return
	}
	if err := dr.LarkServices.SendText(ctx, dr.Receiver, text); err != nil {
		slog.WarnContext(ctx, "failed to send notice", "conv_id", dr.Address(), "error", err)
	}
}

// runAITurn drives one AI turn via the oai-agent-apiserver agent stream, rendering a
// rich card: reasoning panel, tool panel, streaming answer, and a status footer.
// The card header turns green on success and red on failure. images are
// multimodal image inputs (data URLs).
//
// The turn's rendering and its ending are the aiTurn's business, including the
// case where it does not end here at all: a turn that pauses for a decision is
// continued by a later request, and this function returns while its card is still
// standing. What is left here is the setup and the reading of the stream.
func (b *botBiz) runAITurn(ctx context.Context, dr *runtime.DynamicRuntime, question string, images []media.Ref) error {
	// What this turn runs as, and who it runs as. Both are settled here, before
	// anything is created — a conversation that resolves to no agent must not
	// leave a "thinking" card standing, because that card is a promise that an
	// answer is coming, and there is nothing to answer with.
	sched, err := b.schedule(ctx, dr)
	if err != nil {
		return err
	}
	if len(sched.Agents) == 0 {
		b.notice(ctx, dr, sched.Hint.Notice())
		return nil
	}

	// Record AI request usage and duration.
	chatType := chat.ChatTypeP2P
	if dr.IsGroup() {
		chatType = chat.ChatTypeGroup
	}
	userName := dr.Identity()
	// Empty only when the scheduler had nothing to normalize with either — an
	// unbound speaker outside the tenant — and then the fallback is the same
	// empty value, so this is a safety net rather than a second policy.
	senderID := sched.Subject
	if senderID == "" {
		senderID = userName
	}
	aiStart := time.Now()
	metrics.M.RecordAIRequest(ctx, chatType, userName)

	// Resolve the backend before the card exists. A conversation whose backend
	// cannot be reached must fail without leaving a "thinking" card behind —
	// that card is a promise that an answer is coming.
	conv, backend, err := b.agentSession(ctx, dr.Address())
	if err != nil {
		slog.ErrorContext(ctx, "no agent backend for this conversation",
			"conv_id", dr.Address(), "error", err)
		metrics.M.RecordAIRequestError(ctx, "no_backend")
		// A backend that is at capacity is not a backend that is broken: the
		// message would have been answered, just later, and being told so is the
		// difference between a queue and a bug.
		if errors.Is(err, agent.ErrTooManySessions) {
			b.notice(ctx, dr, "本机 agent 会话已满，请稍后再试。")
		}
		return err
	}

	runID := uuid.NewString()
	// Tell the conversation which run it is on, before the card that carries its
	// stop button exists. That is what lets a stop click tell "this card's run is
	// the one in flight" from "this card is a leftover and the run it names is
	// gone" — a question no backend can answer, because stopping a run id nobody
	// holds still looks like a successful request from here.
	if sess := b.liveConversation(dr.Address()); sess != nil {
		sess.SetCurrentRun(runID)
	}
	// One answer to "does this conversation want reasoning" decides both what the
	// backend is asked for and whether the card carries a panel to show it in.
	// Reading the preference twice is how a card comes to offer a panel the backend
	// was never asked to fill — which is what happened while this argument was
	// hardcoded true and the request below read the toggle properly.
	showReasoning := b.reasoningEnabled(dr.Address())
	agents := sched.Agents

	// What this turn is about to run as. Every fact here was already known a few
	// lines up and was thrown away: the same sentence answered differently in a
	// private chat and a group was traced by reading the routing code, because
	// nothing recorded which agent and which backend each conversation had
	// actually resolved to. run_id is the join key to the backend's own
	// run-start log and to the record it writes when the turn ends.
	//
	// The question is measured, never quoted. It is the user's message, it can
	// run to a forwarded transcript, and no log line here is worth leaking one —
	// which is the same reason no other field below is a message body.
	slog.InfoContext(ctx, "AI turn started",
		"run_id", runID,
		"conv_id", dr.Address(),
		"chat_type", chatType,
		"agents", agents,
		"backend", backend,
		"question_bytes", len(question),
		"images", len(images),
		"reasoning", showReasoning)

	// No title: the card carries the bot's own header colour, not its name. An
	// empty title leaves buildStreamingCardJSON's header off entirely.
	cardID, err := dr.LarkServices.CreateStreamingCard(ctx, dr.Receiver, "", "正在思考中，请稍候 ……", showReasoning, runID)
	if err != nil {
		return err
	}

	// The turn's own context, detached from this request's cancellation. The
	// transport layer already detached what it handed over (context.WithoutCancel),
	// so this adds the deadline: a stuck backend must not leave the goroutine and
	// its card hanging for ever. It is the turn's deadline rather than the stream's
	// because a turn can pause, and a run parked on a human is not stuck.
	turn := newAITurn(b, context.WithoutCancel(ctx), aiTurnSetup{
		dr:            dr,
		conv:          conv,
		runID:         runID,
		cardID:        cardID,
		chatType:      chatType,
		userName:      userName,
		started:       aiStart,
		showReasoning: showReasoning,
	})

	streamErr := conv.AskStream(turn.context(), agent.AskRequest{
		Question:  question,
		Images:    images,
		Reasoning: showReasoning,
		Agents:    agents,
		// A skill command names the skill it is; a sentence does not, and an
		// empty list is the ordinary case.
		InvokedSkills: dr.InvokedSkills,
		// The asker's ownership key, not a display name: the backend hands it
		// to the MCP tools it calls, and those scope job/cronjob rows by it.
		// Same value ResolveUsername would produce, so a job created here is
		// the one the same user sees in Lark.
		//
		// It is the scheduler's subject rather than the raw sender: for a speaker
		// with a OneX binding that is the identity OneX's rules are keyed by, so
		// the same person authorizes identically whether the request arrives from
		// here or from ox-agent-apiserver. It also means quota and audit rows land
		// on one identity instead of two.
		SenderID: senderID,
		RunID:    runID,
		// The conversation's own record of where the backend left off, if it has
		// one. A backend that owns its conversations ignores it and reports no id
		// of its own, so this stays empty for them.
		Resume: b.boundAgentSession(dr.Address()),
		// Where /dir pointed this conversation, if anyone did. A backend that
		// holds no directory ignores it, which is why this is a plain field rather
		// than a branch on which backend is in use.
		WorkDir: b.boundWorkDir(dr.Address()),
	}, turn.observe)

	if streamErr != nil {
		if errors.Is(streamErr, agent.ErrBusy) {
			// The backend is still answering this conversation's previous turn, so
			// nothing failed — but nothing was answered either. Marking the card as
			// an error would report a broken agent to someone who only needs to
			// send the message again, and the header colour is the only thing they
			// would read.
			turn.setNotice("上一条消息还在处理中，请稍候再试。")
			slog.InfoContext(ctx, "AI turn refused: the backend is still on the previous turn",
				"conv_id", dr.Address(), "error", streamErr)
			metrics.M.RecordAIRequestError(ctx, "backend_busy")
			turn.finish()
			return nil
		}
		if _, ok := classifyPermission(streamErr); ok {
			// The ask path meets the same two refusals an approval meets through
			// the observe loop. On this path a refusal means no stream was ever
			// opened, so it is handled here rather than as an event.
			//
			// The error is not returned: the session that ran this turn discards
			// what a turn returns, so the signal that outlives it is what
			// refusePermission records — the metric and, for the credential
			// failure that is ours to fix, an Error-level log.
			turn.refusePermission(streamErr)
			return nil
		}
		slog.ErrorContext(ctx, "AI request failed", "conv_id", dr.Address(), "error", streamErr)
		metrics.M.RecordAIRequestError(ctx, "stream_error")
		turn.failed(streamErr)
		return streamErr
	}

	// The stream ended without saying the turn did. That is what a pause looks
	// like once it has been presented: the run is parked on the backend and the
	// person who can continue it has not answered yet, so the card is left open
	// and this run's goroutine is done. Nothing of this function survives the
	// pause — the answer continues the turn through the aiTurn, not through here.
	if turn.paused() {
		return nil
	}
	turn.finish()
	return nil
}

// agentSession returns the backend session this conversation talks to, together
// with the name of the backend it was opened on.
//
// The backend is resolved per turn rather than pinned when the conversation
// starts: a conversation that was switched to another backend must use it from
// the next turn on, and one that has not been switched must keep the backend it
// already has. Nothing is recorded here — the binding lives in the conversation
// snapshot, and this only reads it.
//
// The backend name is returned rather than left to the caller to re-resolve,
// because the caller logs which backend a turn is about to run on. A name
// resolved a second time is a name that can be wrong in precisely the case the
// log exists to explain — a turn that ran somewhere nobody expected.
func (b *botBiz) agentSession(ctx context.Context, sessionKey string) (agent.Session, string, error) {
	if b.sr.Agents == nil {
		return nil, "", errors.New("no agent backend is configured")
	}
	backend := agent.ResolveBackend("", b.boundBackend(sessionKey), b.sr.Agents.Fallback())
	a, ok := b.sr.Agents.Get(backend)
	if !ok {
		return nil, backend, fmt.Errorf("%w: %q", agent.ErrUnknownBackend, backend)
	}
	sess, err := a.Session(ctx, sessionKey)
	return sess, backend, err
}

// boundBackend returns the backend this conversation is already bound to, or ""
// when it has no binding yet.
func (b *botBiz) boundBackend(sessionKey string) string {
	if st := b.boundState(sessionKey); st != nil {
		return st.Backend
	}
	return ""
}

// boundAgentSession returns the backend-side session id this conversation last
// recorded, or "" when it has none — which is the case for every conversation on
// a backend that keeps its own history.
func (b *botBiz) boundAgentSession(sessionKey string) string {
	if st := b.boundState(sessionKey); st != nil {
		return st.BackendSessionID
	}
	return ""
}

// boundWorkDir is the directory the conversation asked its turns to run in, or
// "" for the backend's own choice.
//
// It is read from the snapshot rather than kept here so it survives a restart
// along with the rest of the binding: a conversation pointed at a checkout should
// still be pointed at it after the server is restarted, and a /dir whose effect
// lasted until the next deploy would be a trap rather than a setting.
func (b *botBiz) boundWorkDir(sessionKey string) string {
	if st := b.boundState(sessionKey); st != nil {
		return st.WorkDir
	}
	return ""
}

// boundState returns the state the conversation has recorded, or nil when it has
// no binding yet. Lookup, not Session: reading what a conversation is bound to
// must not bring one into existence.
func (b *botBiz) boundState(sessionKey string) *session.State {
	if b.sr.Sessions == nil {
		return nil
	}
	sess, ok := b.sr.Sessions.Lookup(sessionKey)
	if !ok {
		return nil
	}
	st := sess.State()
	return &st
}

// recordAgentSession stores the backend's own session id on the conversation. It
// is what lets a conversation outlive this process and be resumed rather than
// restarted, and an empty id — which is what a backend that owns its
// conversations reports — is not a reason to write anything.
func (b *botBiz) recordAgentSession(ctx context.Context, sessionKey, id string) {
	if id == "" {
		return
	}
	if b.sr.Sessions == nil {
		return
	}
	sess, ok := b.sr.Sessions.Lookup(sessionKey)
	if !ok {
		return
	}
	if err := sess.Update(func(st *session.State) { st.BackendSessionID = id }); err != nil {
		// The turn itself is fine; what is lost is the ability to resume it after
		// a restart, which is worth a warning and not a failure.
		slog.WarnContext(ctx, "failed to record the agent session id",
			"conv_id", sessionKey, "error", err)
	}
}

// closeStreamingCard finalizes a turn's card, and falls back to delivering the
// answer as a fresh message when the card can no longer be written.
//
// The fallback is the whole point of the preview's failure policy: a transport
// failure may cost the live rendering, but never the content. Someone who waited
// two minutes for an answer must not be told it failed because a card update did.
func (b *botBiz) closeStreamingCard(
	ctx context.Context,
	dr *runtime.DynamicRuntime,
	cardID, finalCard, answer string,
	status larkcard.RichCardStatus,
	seq *stream.Sequence,
) error {
	err := dr.LarkServices.CloseStreamingCard(ctx, cardID, finalCard, seq)
	if err == nil {
		return nil
	}
	slog.WarnContext(ctx, "failed to finalize the streaming card; sending the answer as a fresh message",
		"conv_id", dr.Address(), "error", err)
	if answer == "" {
		return err
	}

	color := "green"
	if status == larkcard.RichCardStatusError {
		color = "red"
	}
	if sendErr := dr.LarkServices.SendMarkdownCard(ctx, dr.Receiver, "", answer, color); sendErr != nil {
		slog.ErrorContext(ctx, "failed to deliver the answer after the card could not be written",
			"conv_id", dr.Address(), "error", sendErr)
		return sendErr
	}
	return nil
}

// appendSteps folds the agent's tool steps into the card's step list. The card's
// step type is a rich-card concept and the incoming one is the port's, so this
// is where a tool call becomes a tool-lane entry.
//
// A step whose tool-call ID is already in the list updates that entry rather
// than adding another: the backend reports an invocation when it starts, again
// when it ends, and once more when the following model call reveals how many
// tokens its result cost the context. All three describe one row, and the card
// would otherwise grow a duplicate every time a tool finished. Steps with no ID
// — a backend that does not report one — can only be appended.
//
// The list is copied before anything is written to it. An update rewrites an
// element in place, and doing that in the caller's own backing array would
// change a slice they still hold.
func appendSteps(steps []larkcard.RichStep, reported []agent.ToolStep) []larkcard.RichStep {
	if len(reported) == 0 {
		return steps
	}
	out := make([]larkcard.RichStep, len(steps), len(steps)+len(reported))
	copy(out, steps)

	for _, st := range reported {
		step := larkcard.RichStep{
			Kind:       larkcard.RichStepKindTool,
			ID:         st.ID,
			Name:       st.Name,
			Status:     st.Status,
			DurationMs: st.Duration.Milliseconds(),
			Tokens:     st.Tokens,
			// Redacted here rather than where it is rendered, because this is the
			// only place a tool step enters the card's types: every path that
			// displays a result displays one that passed through this line, and a
			// renderer added later cannot forget to sanitize. The tool output is
			// free-form text from a command that had access to the process's
			// configuration, so it is treated as untrusted for display even though
			// the model is trusted to read it.
			Result: larkcard.RedactInlineSecrets(st.Result),
		}

		i := -1
		if st.ID != "" {
			i = indexRichStepByID(out, st.ID)
		}
		if i < 0 {
			out = append(out, step)
			continue
		}

		// A later report supersedes the earlier one, except where it is silent:
		// a pending report carries neither a duration nor tokens, and neither
		// does the report that ends an invocation before a following model call
		// has priced its result. Writing those zeros over the row would erase
		// figures that are already on the card and cannot be recomputed here.
		if step.DurationMs == 0 {
			step.DurationMs = out[i].DurationMs
		}
		if step.Tokens == 0 {
			step.Tokens = out[i].Tokens
		}
		// The same rule for the output, which arrives only with the report that
		// ends the call: a later report carrying none is the token-attribution
		// pass, and it must not blank the result already on screen.
		if step.Result == "" {
			step.Result = out[i].Result
		}
		out[i] = step
	}
	return out
}

// indexRichStepByID returns the position of the step carrying id, or -1.
func indexRichStepByID(steps []larkcard.RichStep, id string) int {
	for i, s := range steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// noteStepStarts records when each invocation was first reported, keyed by
// tool-call ID. Only the first sighting counts: the backend reports the same
// invocation again when it ends, and that later report must not reset the clock
// a pending row is counting from.
func noteStepStarts(started map[string]time.Time, reported []agent.ToolStep, now time.Time) {
	for _, st := range reported {
		if st.ID == "" {
			continue
		}
		if _, seen := started[st.ID]; !seen {
			started[st.ID] = now
		}
	}
}

// renderToolsProgress renders the streaming card's live tool panel.
//
// Each row is larkcard.ToolStepLine, the same function the final card's tool-call (工具调用)
// panel renders with, so a record reads the same while it runs and after the turn
// ends. Nothing here formats a figure: a second formatter is how the two lists came
// to disagree in the first place.
//
// now is passed in rather than read here so a pending row's elapsed time can be
// computed from the moment the step arrived: the backend reports a duration only
// when the invocation ends, and until then the only honest figure is how long
// the client has been waiting for it.
func renderToolsProgress(steps []larkcard.RichStep, started map[string]time.Time, now time.Time) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range steps {
		elapsed := s.DurationMs
		if s.Status == "pending" && s.ID != "" {
			if began, ok := started[s.ID]; ok {
				elapsed = now.Sub(began).Milliseconds()
			}
		}
		b.WriteString(larkcard.ToolStepLine(s, elapsed))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// composeStatusFooter builds the dim status footer shown under the final answer.
// It reports elapsed time, tool-step count and the run's token total, and
// appends a terminal marker on error/stop.
//
// reason, when the turn knew one, is what the card says became of the run — a
// stop, a takeover, a cancelled decision — rather than the bare "已停止" (stopped) that
// leaves the reader to guess which of them happened. The second card of a
// duplicated turn used to end exactly that way, with nothing on it to say what
// had killed the first one, and telling that story took reading the whole run's
// logs.
//
// tokens is the run's whole consumption, every model call included, which is why
// it exceeds the sum of the per-tool figures in the panel: those describe how
// much each tool added to the context once, this counts the context again on
// every round. A run that reported no usage passes 0 and gets no token segment.
func composeStatusFooter(
	elapsed time.Duration, stepCount, tokens int, status larkcard.RichCardStatus, stopped bool, reason string,
) string {
	var parts []string
	if elapsed > 0 {
		parts = append(parts, "⏱ 用时 "+elapsed.Round(time.Millisecond).String())
	}
	if stepCount > 0 {
		parts = append(parts, fmt.Sprintf("🧩 %d 步", stepCount))
	}
	if tokens > 0 {
		parts = append(parts, "✨ "+larkcard.FormatTokens(tokens)+" tokens")
	}
	if status == larkcard.RichCardStatusError {
		parts = append(parts, stopMarker(stopped, reason))
	}
	return strings.Join(parts, " · ")
}

// stopMarker names why a run that did not finish cleanly ended. A reason the turn
// recorded is preferred over the generic marker; the fallback distinguishes a
// stop from a failure, which is all that is known when nothing recorded why.
func stopMarker(stopped bool, reason string) string {
	switch reason {
	case reasonTakeover:
		return "已被新消息接管"
	case reasonCancelled:
		return "已取消"
	case reasonStopped:
		return "你停止了本次运行"
	case reasonPermission:
		// A refusal is neither a stop nor a failure, and saying "失败" (failed) would send
		// the user looking for something broken when the card above already told
		// them the one thing they can act on.
		return reasonPermission
	case reasonUnauthorized:
		return reasonUnauthorized
	case reasonQuotaExceeded:
		// Same reasoning as reasonPermission above: an allowance refusal is
		// neither a stop nor a failure, and "失败" (failed) would send the user looking
		// for something broken rather than at the sentence the card already
		// carries.
		return reasonQuotaExceeded
	}
	if stopped {
		return "已停止"
	}
	return "失败"
}

// RunSkill implements runtime.SkillRunner: a skill command becomes a turn.
//
// The turn is an ordinary one — the same admission, streaming card, approval
// pause, stop button and history as a typed message — because a skill command
// is a way to ask for a turn, not a different kind of work. What makes it a
// skill command is the one field it carries: the backend refuses a name the
// conversation's agents do not declare, so a menu built from what they expose
// cannot offer a command the run will not honour.
func (b *botBiz) RunSkill(ctx context.Context, dr *runtime.DynamicRuntime, skill runtime.SkillCommand, args string) error {
	if dr == nil || !dr.HasLarkContext() {
		// The turn draws a card, and only the Lark surface has somewhere to
		// draw it. The command is hidden elsewhere, so reaching this means a
		// caller that went around the menu.
		return runtime.NewUserError("Skill commands are only available in Lark conversations.")
	}

	question := args
	if strings.TrimSpace(question) == "" {
		// A skill invoked with nothing after it is still an instruction to run
		// it; the skill's own text is what says what that means.
		question = "请执行该技能。"
	}

	// A copy, so the invocation does not outlive this call on a runtime the
	// caller may reuse.
	turn := ptr.Clone(dr)
	turn.InvokedSkills = []string{skill.Name}
	return b.runAI(ctx, turn, question, nil)
}
