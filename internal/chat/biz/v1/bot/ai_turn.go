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
	"log/slog"
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
	"github.com/onexstack/onexai/pkg/media"
)

// approvalWaitGrace is how much longer than the configured turn timeout a turn
// may take when it pauses for a decision.
//
// The configured timeout bounds a turn that ends on its own, and it is what
// keeps a stuck backend from holding a conversation's turn slot forever. A turn
// that pauses is not stuck — it is waiting on a person, and how long that takes
// is not something this service can put a bound on. So the bound is extended
// rather than replaced: a turn may take its configured timeout, plus up to this
// long with a decision outstanding.
//
// It is also how long a pause may sit unanswered before its card gives up: at
// the end of it the decision area says the wait ran out and the card is closed,
// so that a pause nobody answered does not stand in the conversation offering
// buttons that can no longer do anything. See aiTurn.expireDecision.
const approvalWaitGrace = 30 * time.Minute

// aiTurnSetup is everything the caller knows about a turn before it exists.
type aiTurnSetup struct {
	dr            *runtime.DynamicRuntime
	conv          agent.Session
	runID         string
	cardID        string
	chatType      string
	userName      string
	started       time.Time
	showReasoning bool
}

// aiTurn renders one logical AI turn: the streaming card it draws into, the
// panels it keeps, and where it got to.
//
// It is a struct rather than a set of locals in the function that started the run
// because a turn can outlive that call. A turn that pauses for a decision is not
// waiting here: the run is parked on the backend, the card is left standing, and
// the person answers on a later request, with none of that function's stack left.
// Everything the continuation needs to carry on rendering the same card is held
// here, and everything here is guarded, because from a pause onwards two
// goroutines touch it — the one reading the stream, and the one a card click
// arrives on.
type aiTurn struct {
	b     *botBiz
	dr    *runtime.DynamicRuntime
	conv  agent.Session
	runID string
	// chatType and userName are the turn's metric labels, read once at the start:
	// neither can change while the turn runs.
	chatType, userName string
	// started is when the user's message began being answered, which is what the
	// footer's elapsed time and the duration metric measure. It deliberately spans
	// a pause: the figure answers "how long did this take", and the person was
	// part of that.
	started time.Time
	// showReasoning is what this conversation asked the backend for. It decides
	// whether the final card carries the panel, and it is kept here so a resumed
	// turn builds the same card the first half did.
	showReasoning bool

	// cardID names the card every lane renders into, and seq the card's sequence
	// number — one sequence for the whole turn, so the continuation's updates
	// order after the first half's instead of restarting from zero.
	cardID   string
	seq      *stream.Sequence
	preview  *stream.Preview
	progress *toolProgress

	// base is the caller's context with its cancellation detached: a card click
	// answers on a different request, and a turn that is parked must not be
	// cancelled by the request that started it returning.
	base context.Context

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	isPaused bool
	ended    bool
	status   larkcard.RichCardStatus
	stopped  bool
	// endReason is why the turn ended, when the ending was not the run's own: a
	// stop, a takeover, a cancelled decision. It is what the footer says instead
	// of a bare "已停止" (stopped), which does not tell the reader whether the run was
	// stopped by them, abandoned for a newer message, or something else again.
	endReason string
	notice    string
	failErr   error

	// The decision the turn is parked on, if it is parked: what it has asked and
	// decided, the buttons still on the card, and the clock that ends the wait.
	decision *decisionLog
	// decisionSlot numbers this turn's pauses, and inlineDecision says whether the
	// pause on screen is drawn on this card or was sent as a message of its own.
	decisionSlot   int
	inlineDecision bool
	pendingActions []string
	expiry         *time.Timer
	expired        bool
	// armedDecision is the outcome of a decision that has been handed to the
	// backend but not yet drawn on the card. It is held here, under mu, between
	// "the answer was delivered" and "the card says so" because those are two
	// different goroutines: the answer resumes the run on a goroutine of its own,
	// and the run can reach the end of the turn — and finalize the card from the
	// log — before the click that delivered it gets another turn. Settling it is
	// what makes the two the same event (see settleDecision).
	armedDecision string

	// refreshCancel stops the ticker that redraws the tool panel. It is guarded
	// separately from mu because the ticker starts and stops on paths that hold no
	// other lock.
	refreshMu     sync.Mutex
	refreshCancel context.CancelFunc
}

// newAITurn builds the turn and starts rendering. The caller has already created
// the card, so a turn that exists is a turn that has something to draw into.
func newAITurn(b *botBiz, base context.Context, s aiTurnSetup) *aiTurn {
	t := &aiTurn{
		b:             b,
		dr:            s.dr,
		conv:          s.conv,
		runID:         s.runID,
		cardID:        s.cardID,
		chatType:      s.chatType,
		userName:      s.userName,
		showReasoning: s.showReasoning,
		started:       s.started,
		seq:           stream.NewSequence(),
		progress:      newToolProgress(),
		base:          base,
		status:        larkcard.RichCardStatusDone,
	}
	t.preview = stream.New(b.streamSink(s.dr, s.cardID), t.seq, b.streamConfig())
	t.rearm()
	t.startRefresh()
	return t
}

// deadline returns how long a stretch of this turn may run without a decision
// outstanding: the configured turn timeout, extended by the grace window that
// covers the wait itself.
func (b *botBiz) deadline() time.Duration {
	return b.turnTimeout() + approvalWaitGrace
}

// rearm gives the turn a fresh deadline and a fresh cancellation. It is called
// when the turn is built and again when a paused turn is answered: a run that was
// answered after its deadline lapsed still has somewhere to put its answer, and
// writing the second half of a turn into a dead context would lose it silently.
func (t *aiTurn) rearm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithTimeout(t.base, t.b.deadline())
	t.ctx, t.cancel = ctx, cancel
}

// context returns the context the turn's I/O runs under, which changes when a
// paused turn is re-armed. Read under the lock: the answer path may be re-arming
// it while the event path is using it.
func (t *aiTurn) context() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ctx
}

// paused reports whether the turn is parked waiting for a human. It stays true
// until an event from the continuation says the run is moving again.
func (t *aiTurn) paused() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isPaused
}

// observe is the turn's event sink: it feeds the three lanes and decides what the
// events mean for the card's life. It returns an error only to abort the stream,
// which is what a failure event does — there is nothing to keep reading.
// deliverImages sends the images a run produced to the conversation.
//
// # Why they are a message rather than part of the card
//
// Lark's rich card cannot carry a conversation image — a card may embed an
// `img` element, but its source is a static key uploaded for that card, not the
// generated picture, and there is no field in which a picture the run just made
// could be rendered. So each image goes out as its own message, after the
// answer text has been written to the card.
//
// A failure to deliver one is logged and does not fail the turn: the answer
// itself was delivered, and reporting the whole run as failed over a picture
// would send the user looking for a problem with the text.
func (t *aiTurn) deliverImages(ctx context.Context, refs []media.Ref) {
	b, dr := t.b, t.dr
	if len(refs) == 0 {
		return
	}
	// The mirror is told once for the whole batch rather than per image: it is
	// one message with several pictures, and a frame per picture would render
	// as several.
	defer b.mirrorImagesToTUI(dr.Receiver, refs)

	// A turn with no Lark surface — a console-only caller — has nowhere to send
	// them, and the mirror above is the one place they do go. Reaching the
	// concrete sender instead would have failed per image for a reason that is
	// not a failure.
	if dr.LarkServices == nil {
		return
	}
	if b.sr.Clientset == nil || b.sr.Clientset.Agent() == nil {
		slog.WarnContext(ctx, "no agent backend to read a generated image from", "conv_id", dr.Address())
		return
	}
	for _, ref := range refs {
		data, _, err := b.sr.Clientset.Agent().FetchAssetContent(ctx, ref.AssetID)
		if err != nil {
			slog.WarnContext(ctx, "could not read a generated image for delivery",
				"conv_id", dr.Address(), "asset_id", ref.AssetID, "error", err)
			continue
		}
		// Through the port rather than the concrete sender: it is the same call
		// in production, and it is the seam a test can answer.
		if err := dr.LarkServices.SendImage(ctx, dr.Receiver, data); err != nil {
			slog.WarnContext(ctx, "could not send a generated image to the conversation",
				"conv_id", dr.Address(), "asset_id", ref.AssetID, "error", err)
		}
	}
}

func (t *aiTurn) observe(ev agent.Event) error {
	ctx := t.context()
	dr, b := t.dr, t.b

	// The two events that describe a pause are not the run producing something,
	// so they are handled before the resumption below: neither may be read as
	// "the human answered".
	switch ev.Type {
	case agent.EventInterrupted:
		t.interrupted(ctx, ev.Interrupt)
		return nil
	case agent.EventSuspended:
		// The stream that was rendering this turn is over, but the turn is not:
		// the run is parked on the backend and continues when it is answered. The
		// card is deliberately left as it stands — finalizing it here would write
		// the Done footer under an answer that has not finished arriving.
		//
		// The TUI mirror does see an end of stream, because that is what it shows:
		// one streamed block, and the continuation is the next one.
		b.mirrorStreamDoneToTUI(dr.Receiver, nil)
		return nil
	}

	// Everything else came from a run that is producing again. For the LLM backend
	// that means the answer was accepted and its stream is feeding this same sink;
	// for the CLI backend it is the turn's own stream, which never stopped.
	t.resumeIfPaused()

	switch ev.Type {
	case agent.EventReasoning:
		if ev.Delta != "" {
			t.preview.UpdateReasoning(ctx, ev.Delta)
			b.mirrorStreamChunkToTUI(dr.Receiver, streamKindReasoning, ev.Delta)
		}
	case agent.EventProgress, agent.EventToolStep:
		t.progress.observe(ev.Steps, time.Now())
		panel := t.progress.render(time.Now())
		t.preview.SetProgress(ctx, panel)
		b.mirrorStreamChunkToTUI(dr.Receiver, streamKindProgress, panel)
	case agent.EventUsage:
		// Redrawing here is what makes the token figure appear while the answer is
		// still streaming. A report that lowers the total is ignored rather than
		// drawn, because it can only be a stale one.
		if ev.Usage != nil && t.progress.setTokens(ev.Usage.TotalTokens) {
			t.preview.SetProgress(ctx, t.progress.render(time.Now()))
		}
	case agent.EventAnswer:
		if ev.Delta != "" {
			t.preview.UpdateText(ctx, ev.Delta)
			b.mirrorStreamChunkToTUI(dr.Receiver, "", ev.Delta)
		}
	case agent.EventImage:
		t.deliverImages(ctx, ev.Images)
	case agent.EventFailed:
		// A permission refusal arrives here too, on the approval path: a resumed
		// run re-runs the same gate, so an answer can be refused exactly like the
		// first request was. It ends the turn without being a failure, and gets a
		// card that says which refusal it was instead of the generic one.
		if _, ok := classifyPermission(ev.Err); ok {
			t.refusePermission(ev.Err)
		} else {
			t.failed(ev.Err)
		}
		// Reported to the sink's caller as well as drawn: the caller is the one
		// reading the stream, and its job is to stop reading.
		return ev.Err
	case agent.EventStopped:
		// The run was stopped by the person who asked for it, arriving as an event
		// from the backend rather than as a click (that is a running turn's stop).
		// Naming them in the footer is the difference between a card that explains
		// itself and one that reads as an unexplained failure.
		t.setEndReason(reasonStopped)
		t.setStatus(larkcard.RichCardStatusError)
		t.setStopped(true)
		t.finish()
	case agent.EventDone:
		// The run finished cleanly. The answer has been streaming into the preview
		// all along, and the card is closed from it, so the full text here is not
		// needed. Its usage total is the authoritative one and the last the run
		// will state, so it is taken even when no usage event preceded it.
		if ev.Usage != nil {
			t.progress.setTokens(ev.Usage.TotalTokens)
		}
		t.finish()
	}
	return nil
}

// interrupted presents a pause to the human and parks the turn on it.
//
// A pause that carries both an approval and a question is presented as an
// approval, and the question re-interrupts after it. The two are not separable on
// one card — an approval is a closed vocabulary and an answer is free text — and
// the security decision is the one that cannot wait, so it goes first. The
// alternative, two live cards either of which resumes the run, would let an
// approval ride in on an answer to an unrelated question.
func (t *aiTurn) interrupted(ctx context.Context, it *agent.Interrupt) {
	t.setPaused(true)
	// Nothing is running while a human decides, so the panel must stop counting:
	// the row that was pending is waiting on the person, not on the tool.
	t.stopRefresh()
	// Registered before the decision is rendered: a stop clicked while it is still
	// in flight has to find the turn it is meant to end.
	t.b.setSuspendedTurn(t.dr.Address(), t)

	t.pause(ctx, it)
}

// resumeIfPaused restarts what the pause stopped, on the first event of the
// continuation. It is where a turn that was parked becomes a turn that is running
// again, and it is deliberately driven by the events rather than by the answer:
// an answer only says the request was accepted, while the events say the run is
// producing — which is what the card is waiting for.
func (t *aiTurn) resumeIfPaused() {
	t.mu.Lock()
	if !t.isPaused {
		t.mu.Unlock()
		return
	}
	t.isPaused = false
	dead := t.ctx.Err() != nil
	t.mu.Unlock()

	// Someone answered, so there is no wait left to time out. Answering already
	// stops the clock; this covers the resume that did not come from an answer —
	// a backend that continues on its own.
	t.stopDecisionExpiry()

	if dead {
		t.rearm()
	}
	// The pending rows have not been running while the human decided. Counting
	// their wait as the tool's time would report the decision's latency as the
	// tool's, so the clock starts again from here.
	t.progress.resetPendingStarted(time.Now())
	t.startRefresh()
}

// setPaused records that the turn is parked on a decision.
func (t *aiTurn) setPaused(paused bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.isPaused = paused
}

// setStatus sets the status the final card is built with.
func (t *aiTurn) setStatus(status larkcard.RichCardStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
}

// setStopped records that the turn ended because someone stopped it.
func (t *aiTurn) setStopped(stopped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = stopped
}

// setNotice replaces the answer in the final card. It is how the backend-busy
// case says something without reporting an error.
func (t *aiTurn) setNotice(notice string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.notice = notice
}

// failed ends the turn on an error.
func (t *aiTurn) failed(err error) {
	t.mu.Lock()
	t.status, t.failErr = larkcard.RichCardStatusError, err
	t.mu.Unlock()
	t.finish()
}

// refusePermission ends a turn the backend refused for this server's
// credentials or for the user's authorization. Neither is a failure: nothing
// ran, and retrying changes nothing. So the card does not fall through to the
// generic "失败" (failed) footer — it says which of the two happened and, for the one
// the user can act on, what to do about it.
//
// Both the first ask and a resumed approval land here, because resuming a
// paused run re-runs the same gate.
func (t *aiTurn) refusePermission(err error) {
	notice, ok := classifyPermission(err)
	if !ok {
		return
	}

	t.mu.Lock()
	t.status = larkcard.RichCardStatusError
	t.notice = notice.body
	t.endReason = notice.reason
	// failErr reaches only the TUI mirror, so it carries the short sentence a
	// person should read there rather than the backend's own message, which
	// names an internal subject id and belongs in the log below.
	t.failErr = errors.New(notice.plain)
	t.mu.Unlock()

	ctx, dr := t.context(), t.dr
	// Different levels because these are different problems: wrong credentials
	// are ours to fix and worth an alert, while an authorization refusal is a
	// user who was told no.
	attrs := []any{
		"conv_id", dr.Address(),
		"run_id", t.runID,
		"backend_message", agentclient.HTTPErrorMessage(err),
		"error", err,
	}
	if errors.Is(err, agent.ErrUnauthorized) {
		slog.ErrorContext(ctx, "AI turn refused: the backend rejected this server's credentials", attrs...)
	} else {
		slog.WarnContext(ctx, "AI turn refused: the user may not use this agent", attrs...)
	}
	metrics.M.RecordAIRequestError(ctx, notice.metric)
	t.finish()
}

// stoppedByUser ends the turn because someone pressed stop on its card, which is
// the one ending no event carries: a turn parked on a decision has no stream left
// to see it end in. reason is what the footer says became of the run; an empty
// one leaves the generic marker, for a caller that has nothing to add.
func (t *aiTurn) stoppedByUser(reason string) {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.status, t.stopped = larkcard.RichCardStatusError, true
	if reason != "" {
		t.endReason = reason
	}
	t.mu.Unlock()
	t.finish()
}

// setEndReason records why the turn ended, for the footer.
func (t *aiTurn) setEndReason(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reason != "" {
		t.endReason = reason
	}
}

// startRefresh starts the ticker that redraws the tool panel while something is
// pending. It is a no-op when one is already running.
func (t *aiTurn) startRefresh() {
	t.refreshMu.Lock()
	defer t.refreshMu.Unlock()
	if t.refreshCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(t.context())
	t.refreshCancel = cancel

	go func() {
		ticker := time.NewTicker(progressRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				// Only a running tool's elapsed time changes on its own, so an
				// idle turn costs nothing but a timer.
				if !t.progress.hasPending() {
					continue
				}
				t.preview.RefreshProgress(ctx, t.progress.render(now))
			}
		}
	}()
}

// stopRefresh stops the ticker, if one is running.
func (t *aiTurn) stopRefresh() {
	t.refreshMu.Lock()
	cancel := t.refreshCancel
	t.refreshCancel = nil
	t.refreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// finish closes the turn's card and lets go of everything the turn was holding,
// once. A turn can be ended from four places — its own last event, a stop, a
// failure on the way to either, and a stop click on a parked card — and they can
// race, so the first one to get here closes the card and the rest return.
//
// A turn that is only paused is not finished: it has not finished. See observe.
func (t *aiTurn) finish() {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended = true
	status, stopped, notice, failErr := t.status, t.stopped, t.notice, t.failErr
	endReason := t.endReason
	t.mu.Unlock()

	// The ticker must not outlive the card it draws into. Even without this the
	// preview would refuse a redraw once frozen, but a goroutine with no further
	// purpose should not be left to discover that on its own.
	t.stopRefresh()
	// And neither should the clock that ends a wait nobody is waiting on: the turn
	// is over, so a pause left pending by an abrupt ending would only write into a
	// card that no longer accepts writes.
	t.stopDecisionExpiry()
	// A tool that was waiting on a person is not going to run: this turn is over,
	// and the decision it was parked on was never made. Said on the row itself,
	// because "等待人工确认" (awaiting confirmation) on a finished card claims someone can still answer it.
	t.progress.abandonWaiting()
	// Nothing will be written to this context again, and holding its deadline open
	// would keep a timer alive for a turn that is over.
	defer t.cancel()

	ctx, dr, b := t.context(), t.dr, t.b

	// The run is over, so nothing is waiting on a reply to it any more —
	// including the cases that end badly (a failed run, a turn that timed out, a
	// stop). Leaving the question behind would make the next message the user
	// types be read as an answer to a run that no longer exists.
	b.clearPendingPermission(dr.Address(), t.runID)
	// The run is over, so the conversation is idle again — a card left on screen
	// must not claim to be stoppable. Cleared by id, not unconditionally: a turn
	// that ends after a newer one started would otherwise erase the newer turn's
	// claim on the conversation.
	if sess := b.liveConversation(dr.Address()); sess != nil {
		sess.ClearCurrentRun(t.runID)
	}
	b.clearSuspendedTurn(dr.Address(), t)
	// A decision that was delivered but never drawn is folded into the log before
	// the card below is built from it. The flip at the top of this function is what
	// makes this complete rather than merely likely: armDecision refuses once the
	// turn has ended, so an outcome armed before that point is the last one there
	// can be, and one offered after it belongs to a run that is already over — which
	// the backend refuses in turn, and the click reports as a delivery that failed.
	t.settleDecision()
	// Record where the backend left this conversation, so a later turn resumes it
	// instead of starting over. It is written here rather than when the run starts
	// because a backend may hand out its id part-way through — the CLI does — and
	// because a turn that failed still moved the conversation on.
	b.recordAgentSession(ctx, dr.Address(), t.conv.ID())

	metrics.M.RecordAIRequestDuration(ctx, time.Since(t.started).Seconds())

	// Publish whatever the throttle is still holding, then freeze: the card is
	// about to be replaced wholesale, and a delta arriving after that would bump
	// the sequence of a card that no longer accepts updates. The flush is also the
	// last line of defence — if the close below fails, what is on screen is the
	// complete answer rather than the last partial update.
	t.preview.Finish(ctx)
	body := t.preview.Text()
	if notice != "" {
		body = notice
	}
	stepCount, tokens := t.progress.counts()
	footer := composeStatusFooter(time.Since(t.started), stepCount, tokens, status, stopped, endReason)
	// The reasoning comes from the preview rather than from the events: the lane
	// kept every delta even where the publish throttle decided one was not worth a
	// round trip, which is the whole reason there is anything to show again here.
	//
	// It is dropped outright when the conversation turned reasoning off. A backend
	// that streams reasoning anyway would otherwise hand the final card a panel the
	// live card never had, showing someone exactly what they asked not to see. An
	// empty string is how BuildRichCard is told to leave the panel off, so this
	// covers both that case and a backend that simply emits none.
	reasoning := t.preview.Reasoning()
	if !t.showReasoning {
		reasoning = ""
	}
	// The decision log is carried into the rebuild. Without it the final card would
	// be a card with the questions and decisions cut out of it — the one part of
	// the turn that came from a person rather than from the model.
	finalCard := larkcard.BuildRichCardWithDecisions(
		status, reasoning, t.progress.rows(), body, false, footer, t.decisionElements())
	// Reported inside: a close that fails falls back to a fresh message, and only
	// that failing in turn is worth logging.
	b.closeStreamingCard(ctx, dr, t.cardID, finalCard, body, status, t.seq) //nolint:errcheck

	b.mirrorStreamDoneToTUI(dr.Receiver, failErr)
}
