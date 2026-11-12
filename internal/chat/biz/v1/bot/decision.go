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
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
)

// A pause is drawn on the turn's own card, as two card components:
//
//	? **需要你的批准** (approval needed): pipeline  ← decision_log, one per turn
//	[✓ 允许 (allow)] [✗ 拒绝 (deny)]               ← decision_row_1
//	[✓ 总是允许 (always allow)]                    ← decision_grant_1
//
// The log is one element for the whole turn, which is what makes a turn that
// pauses twice read as a timeline rather than as two unrelated questions:
//
//	? **需要你的批准** (approval needed): pipeline
//	✓ 已允许 (allowed): pipeline · 14:33
//	? **请选择** (choose): which environment to deploy to?
//	✓ 已选择 (selected): edu-onex · 14:34
//
// The buttons are one set per pause, because they are deleted the moment their
// pause is answered. A card with nothing pending carries no button at all, which
// is how "no dead buttons" is implemented rather than promised.
const (
	decisionLogElementID = "decision_log"
	// decisionRowIDPattern names a pause's row of answer buttons, and
	// decisionGrantIDPattern the row under it.
	//
	// Both ids are kept short on purpose. Feishu caps an element_id at 20
	// characters ("Only alphabets, numbers, and underscores are allowed. It must
	// start with an alphabet and not exceed 20 characters"), and cardkit enforces
	// it when a component is added to a card that already exists — which is the
	// only moment a decision is ever drawn. An id over the cap is therefore not a
	// cosmetic problem: the buttons cannot be put on the turn's card, the pause
	// falls back to a card of its own, and one turn ends up with two cards. The
	// names are short enough that even a turn that pauses a hundred times stays
	// inside the cap.
	decisionRowIDPattern = "decision_row_%d"
	// decisionGrantIDPattern names the second row of a permission's buttons.
	// It is a row of its own on purpose: it grants the tool for the rest of the
	// conversation, and a button that does that should not sit under the thumb
	// that is aiming at 允许 (allow).
	decisionGrantIDPattern = "decision_grant_%d"

	// placeholderCleared is what a card element is set to when it should show
	// nothing at all: cardkit refuses an empty content outright, and a
	// zero-width space is the empty-looking value it does accept. Written as an
	// escape because the character itself is invisible in a source file.
	placeholderCleared = "\u200b"

	// summaryAwaiting and summaryThinking are what the conversation list shows for
	// the card. A card waiting on a person is the one thing a reader cannot work
	// out from the list, so it is the one thing the summary says.
	summaryAwaiting = larkcard.GlyphPending + " 等待你的选择"
	summaryThinking = "正在思考中"
)

// decisionBlock is one pause as the card should show it: the line appended to the
// log, and the buttons that answer it.
//
// The same value describes the pause whether it is drawn into the live card or
// sent as a card of its own, so the two paths cannot come to describe the same
// pause differently.
type decisionBlock struct {
	// request is the markdown appended to the turn's decision log.
	request string
	// buttons answer the pause. Empty when the answer is typed rather than
	// clicked — a multi-select question — in which case request carries the
	// numbered list and the instruction that goes with it.
	buttons []larkcard.CardButton
	// secondary is the row under the main one, and is where the session-scoped
	// grant goes.
	secondary []larkcard.CardButton
	// layout is how the buttons are arranged when there are any.
	layout larkcard.CardActionLayout
}

// decisionLog is a turn's append-only record of what it asked and what was
// decided.
//
// It is a type rather than a slice field because two goroutines write it: the
// event loop appends a request when the run pauses, and a card click appends the
// outcome when someone answers, and both may be holding the same turn.
type decisionLog struct {
	mu    sync.Mutex
	lines []string
	// onCard records that the log element exists on the card. The first pause
	// inserts it, every later one rewrites it, and the two are different calls.
	onCard bool
}

// append adds one line. An empty line is dropped: an empty log is what says this
// turn never needed a decision at all, and it must not be confused with one whose
// only line was blank.
func (l *decisionLog) append(line string) {
	if l == nil || strings.TrimSpace(line) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

// snapshot returns the lines, for the final card and for tests.
func (l *decisionLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// markdown renders the log as the content of its element.
func (l *decisionLog) markdown() string {
	return decisionLogMarkdown(l.snapshot())
}

// empty reports whether anything has been asked or decided on this turn.
func (l *decisionLog) empty() bool {
	return len(l.snapshot()) == 0
}

// decisionLogMarkdown renders the log's lines. It is a pure function so the whole
// decision rendering is testable without a card, a tenant, or a clock.
func decisionLogMarkdown(lines []string) string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// Request lines. Every one of them opens with the pending glyph: a reader
// scrolling the card should be able to tell what is still open from the first
// character of each line.
const (
	decisionTitleAsking   = larkcard.GlyphPending + " **需要你的批准**："
	decisionTitleChoosing = larkcard.GlyphPending + " **请选择**："
)

// Outcome lines. Each names what came of the request, and — for the decisions
// that were never made — that the execution did not continue, because that is
// the part a reader cannot work out for themselves.
//
// The characters are the card's own vocabulary (larkcard.Glyph*): ✓ decided,
// ✗ refused, ○ never decided. Cancelling and expiring share ○ deliberately: in
// both, nobody decided and nothing ran, and the words after the glyph are what
// say which happened.
func decisionLineAllowed(subject string, now time.Time) string {
	return larkcard.GlyphDone + " 已允许：" + subject + decisionStamp(now)
}

func decisionLineDenied(subject string, now time.Time) string {
	return larkcard.GlyphFailed + " 已拒绝：" + subject + decisionStamp(now)
}

func decisionLineAllowedAll(subject string, now time.Time) string {
	return larkcard.GlyphDone + " 总是允许：" + subject + decisionStamp(now)
}

func decisionLineChosen(choice string, now time.Time) string {
	return larkcard.GlyphDone + " 已选择：" + choice + decisionStamp(now)
}

func decisionLineAnswered(now time.Time) string {
	return larkcard.GlyphDone + " 已回复你输入的答案" + decisionStamp(now)
}

// Why a decision went unmade. They are consts because the same three situations
// are reachable from more than one place — a stop is both a button and a command
// — and the card should read the same whichever way it arrived.
const (
	reasonStopped   = "你停止了本次运行"
	reasonTakeover  = "收到新消息"
	reasonCancelled = "你取消了本次确认"
)

func decisionLineCancelled(reason string) string {
	return larkcard.GlyphIdle + " 已取消：" + reason + "，本次执行未继续"
}

// decisionOutcomeLine names what became of a permission the user just answered.
// It is built from the decision rather than from the button's label so that the
// line states what the backend was actually asked to do.
func decisionOutcomeLine(decision, subject string, now time.Time) string {
	switch decision {
	case agent.DecisionDeny:
		return decisionLineDenied(subject, now)
	case agent.DecisionAllowAll:
		return decisionLineAllowedAll(subject, now)
	default:
		return decisionLineAllowed(subject, now)
	}
}

func decisionLineExpired() string {
	return larkcard.GlyphIdle + " 已失效：等待超时，本次执行未继续"
}

func decisionLineSuperseded() string {
	return larkcard.GlyphIdle + " 已被新的确认替代，本次执行未继续"
}

// decisionStamp appends the wall clock a decision was made at, so that a log
// read afterwards says when the person answered and not only that they did.
func decisionStamp(now time.Time) string {
	return " · " + now.Format("15:04")
}

// decisionActionsElements builds a pause's buttons, under the ids they will be
// deleted by. The button ids are returned with them so that the caller deletes
// exactly what it inserted.
func decisionActionsElements(slot int, blk *decisionBlock, sessionKey string) ([]map[string]any, []string) {
	var elements []map[string]any
	var ids []string

	if len(blk.buttons) > 0 {
		id := fmt.Sprintf(decisionRowIDPattern, slot)
		elements = append(elements, larkcard.ElementButtonRow(id, blk.buttons, sessionKey, blk.layout))
		ids = append(ids, id)
	}
	if len(blk.secondary) > 0 {
		id := fmt.Sprintf(decisionGrantIDPattern, slot)
		elements = append(elements, larkcard.ElementButtonRow(id, blk.secondary, sessionKey, blk.layout))
		ids = append(ids, id)
	}
	return elements, ids
}

// approvalDecision renders a permission pause as a decision block, and records
// the pending decision so that a reply typed as text can reach it too.
func (b *botBiz) approvalDecision(dr *runtime.DynamicRuntime, runID string, it *agent.Interrupt) *decisionBlock {
	reqs := it.Permissions()
	if len(reqs) == 0 {
		return nil
	}
	subject := strings.Join(approvalToolNames(reqs), "、")

	b.recordPendingPermission(dr, &session.PendingPermission{
		RunID:     runID,
		RequestID: it.RequestID,
		Kind:      agent.InterruptKindPermission,
		ToolName:  strings.Join(approvalToolNames(reqs), ", "),
		Question:  approvalPrompt(reqs),
		AskedBy:   askerIdentity(dr),
	})

	extra := func(label string) map[string]string {
		return map[string]string{
			"run_id":       runID,
			"req_id":       it.RequestID,
			"perm_label":   label,
			"perm_subject": subject,
			// The decision is drawn on the turn's own card, so answering it rewrites
			// that card instead of replacing the message it was sent on.
			"inline": "1",
		}
	}
	block := &decisionBlock{
		request: decisionTitleAsking + approvalSummary(reqs),
		buttons: []larkcard.CardButton{
			approvalButton(buttonAllowText, "primary", PermAllow, extra(labelAllowed)),
			approvalButton(buttonDenyText, "danger", PermDeny, extra(labelDenied)),
		},
		secondary: []larkcard.CardButton{
			approvalButton(buttonAllowAllText, "default", PermAllowAll, extra(labelAllowedAll)),
		},
		layout: larkcard.CardActionLayoutEqualColumns,
	}
	if len(reqs) > 1 {
		// Every tool the pause waits on is named, because one decision covers the
		// group: the framework re-runs the whole tool node on resume, so a card that
		// showed one of three pending tools would be asking a question whose answer
		// applies to three.
		block.request += "\n\n" + approvalArguments(reqs)
		block.request += "\n\n一次点击决定以上全部工具：允许则全部执行，拒绝则全部不执行。"
	}
	return block
}

// approvalSummary compresses a permission pause into the one line the decision
// log gives it, with the arguments the call will run with beside the tool they
// are for. Without them the question is "may I use pipeline?", which is not a
// question anybody can answer: the same tool with different arguments is a
// different action, and the one the reader is being asked about is this one.
func approvalSummary(reqs []agent.InterruptRequest) string {
	if len(reqs) == 1 {
		return toolNameOrAgent(reqs[0]) + larkcard.ArgumentsDigest(reqs[0].Arguments)
	}
	return fmt.Sprintf("%d 个工具：%s", len(reqs), strings.Join(approvalToolNames(reqs), "、"))
}

// approvalArguments lists the arguments of a pause that covers several calls, one
// per line under the asking line: with more than one tool in the group, the tool
// names alone say nothing about what any of them would do.
//
// The list is capped. A pause that covers twenty calls is one whose arguments do
// not fit on a card, and the reader's decision is the same for all of them —
// which is what the sender note under this list says.
func approvalArguments(reqs []agent.InterruptRequest) string {
	const maxListed = 5
	var b strings.Builder
	listed := 0
	for _, req := range reqs {
		digest := larkcard.ArgumentsDigest(req.Arguments)
		if digest == "" {
			continue
		}
		if listed >= maxListed {
			fmt.Fprintf(&b, "\n- 还有 %d 个工具的参数未列出", len(reqs)-listed)
			break
		}
		fmt.Fprintf(&b, "\n- %s%s", toolNameOrAgent(req), digest)
		listed++
	}
	return b.String()
}

// questionDecision renders a question pause as a decision block.
//
// A clickable question gets one button per option; a multi-select or optionless
// one gets the numbered list and the instruction to reply with a number, which
// is the only way a set of answers can be given in a single message.
func (b *botBiz) questionDecision(
	dr *runtime.DynamicRuntime, runID string, it *agent.Interrupt, req agent.InterruptRequest,
) *decisionBlock {
	question := req.Question
	if question == "" {
		question = "请选择一个选项"
	}
	options := req.Options

	b.recordPendingPermission(dr, &session.PendingPermission{
		RunID:     runID,
		RequestID: it.RequestID,
		Kind:      agent.InterruptKindQuestion,
		ToolName:  toolNameOrAgent(req),
		Question:  question,
		Address:   req.Address,
		AskedBy:   askerIdentity(dr),
	})

	block := &decisionBlock{
		request: decisionTitleChoosing + question,
		layout:  larkcard.CardActionLayoutRow,
	}
	if req.MultiSelect || len(options) == 0 {
		var list strings.Builder
		for i, opt := range options {
			fmt.Fprintf(&list, "\n%d. **%s**", i+1, opt)
		}
		block.request += list.String()
		block.request += "\n\n回复选项编号（多个用逗号分隔），或直接回复你的答案。"
		return block
	}

	buttons := make([]larkcard.CardButton, 0, len(options))
	for i, opt := range options {
		buttons = append(buttons, larkcard.CardButton{
			Text:  opt,
			Type:  "default",
			Value: fmt.Sprintf("askq:%d", i+1),
			Extra: map[string]string{
				"run_id":        runID,
				"req_id":        it.RequestID,
				"askq_addr":     req.Address,
				"askq_label":    opt,
				"askq_question": question,
				// It is rendered on the turn's card, so answering it rewrites that
				// card rather than returning a card of its own.
				"inline": "1",
			},
		})
	}
	block.buttons = buttons
	return block
}

// pause presents a decision on the turn's own card.
//
// This is the whole point of the rework: the card that says 正在思考中，请稍候 (thinking, please wait) and
// the card that asks what to do next are one card, so there is never a question
// of which of them is live. When the card cannot be written to — the element API
// refusing the insert, a card finalized elsewhere — the pause falls back to the
// separate decision message this code used to send, and the turn is no worse off
// than it was.
func (t *aiTurn) pause(ctx context.Context, it *agent.Interrupt) {
	if it == nil || len(it.Requests) == 0 {
		// A pause with nothing to ask is still a pause: the run is parked, and this
		// turn says so rather than putting an empty question on screen.
		slog.WarnContext(ctx, "agent paused without reporting what it is waiting on",
			"conv_id", t.dr.Address(), "run_id", t.runID)
		return
	}

	// Split the pause the way this side always has: an approval first, because an
	// approval has a closed vocabulary and an answer is free text, and a question
	// re-interrupts afterwards. Two live decision surfaces would let an approval
	// ride in on an answer to an unrelated question.
	var block *decisionBlock
	var fallback func()
	if perms := it.Permissions(); len(perms) > 0 {
		block = t.b.approvalDecision(t.dr, t.runID, it)
		fallback = func() { t.b.sendApprovalCard(ctx, t.dr, t.runID, it) }
	} else if q, ok := it.FirstQuestion(); ok {
		block = t.b.questionDecision(t.dr, t.runID, it, q)
		fallback = func() { t.b.sendAskQuestionCard(ctx, t.dr, t.runID, it, q) }
	}
	if block == nil {
		return
	}

	// The card's answer element holds a placeholder that says the model is
	// thinking. It is not any more, so the placeholder goes — but only while it is
	// still the placeholder: once the model has said something, that text is the
	// answer and it stays.
	//
	// This is the one write that cannot go through the preview. Clearing the
	// element means writing an empty string, and the preview's staleness check
	// treats empty text as "nothing to say" rather than as a change.
	if t.preview.Text() == "" {
		if err := t.clearAnswerPlaceholder(ctx); err != nil {
			slog.WarnContext(ctx, "clear answer placeholder failed", "error", err, "card_id", t.cardID)
		}
	}

	// Nothing is running while a person decides, so the panel must stop counting:
	// the rows still pending are waiting on the reader, not on the tool.
	t.progress.markWaiting()
	// And it must say so now rather than on the next publish. The ticker that would
	// have redrawn the panel was stopped above, so without this write a row waiting
	// on a person would go on showing itself as a running tool with a clock frozen
	// at the moment it stopped — which reads as a hung tool, not as a question.
	// RefreshProgress is the one publish that ignores the throttle, which is what
	// makes the card say what changed at the moment it changed.
	if steps, _ := t.progress.counts(); steps > 0 {
		t.preview.RefreshProgress(ctx, t.progress.render(time.Now()))
	}

	if err := t.presentDecision(ctx, block); err != nil {
		slog.WarnContext(ctx, "render decision on card failed, falling back to a card of its own",
			"error", err, "card_id", t.cardID, "run_id", t.runID)
		t.setInlineDecision(false)
		fallback()
		return
	}
	t.setInlineDecision(true)
	t.armDecisionExpiry()
}

// presentDecision writes a pause onto the card, immediately before the stop row.
func (t *aiTurn) presentDecision(ctx context.Context, block *decisionBlock) error {
	cards := t.cards()
	if cards == nil {
		return errNoCardServices
	}
	seq := t.seq
	log := t.decisionLog()

	// A pause whose buttons are still on the card when another pause arrives was
	// never answered: a backend that holds its stream open while it waits — the
	// CLI, whose process owns the request — can interrupt again without an answer
	// in between. Its buttons go, and the log says what became of it, because a
	// row still offering a decision that can no longer be delivered is exactly the
	// dead button the decision area exists to avoid.
	if stale := t.takePendingActions(); len(stale) > 0 {
		log.append(decisionLineSuperseded())
		if err := cards.DeleteCardElements(ctx, t.cardID, seq, stale...); err != nil {
			return err
		}
	}

	// The request is appended before anything is written, so that the log the
	// card shows and the log the final card is built from cannot disagree. What
	// the element held before it is kept as well, because a pause whose buttons
	// are refused takes its request back off the card (see below) while the log
	// itself keeps the line: the decision was asked, and the card that gets built
	// when the turn ends should say so even when the asking happened elsewhere.
	previous := log.markdown()
	log.append(block.request)
	content := log.markdown()

	elements, ids := decisionActionsElements(t.nextDecisionSlot(), block, t.dr.Address())

	if !log.hasLogElement() {
		// The turn's first pause, so the log element and the buttons go in as one
		// request: the card gains the whole decision or none of it, and a refusal
		// (a cardkit rule this side got wrong, a card finalized elsewhere) leaves
		// the card exactly as it was rather than holding a question with nothing
		// to answer it.
		batch := append([]map[string]any{larkcard.ElementMarkdown(decisionLogElementID, content)}, elements...)
		if err := cards.InsertCardElements(ctx, t.cardID, stopRowCardElementID, seq, batch...); err != nil {
			return err
		}
		log.setLogElement(true)
		t.setPendingActions(ids...)
		t.summarizePause(ctx)
		return nil
	}

	// The element is already on the card — this is the turn's second pause — so
	// the log grows in place. The element keeps its position, which is above every
	// pause's buttons and below the answer.
	if err := cards.UpdateCardElementText(ctx, t.cardID, decisionLogElementID, content, seq); err != nil {
		return err
	}
	if len(elements) > 0 {
		if err := cards.InsertCardElements(ctx, t.cardID, stopRowCardElementID, seq, elements...); err != nil {
			// The request is on the card but its buttons are not, which is the one
			// shape the decision area exists to avoid: a question with nothing to
			// answer it. The log goes back to what it said before this pause, and
			// the fallback card is where the question is asked instead. The next
			// pause rewrites the whole log, so the line is not lost for good.
			if previous != "" {
				if rerr := cards.UpdateCardElementText(ctx, t.cardID, decisionLogElementID, previous, seq); rerr != nil {
					slog.WarnContext(ctx, "revert decision log failed", "error", rerr, "card_id", t.cardID)
				}
			}
			return err
		}
		t.setPendingActions(ids...)
	}
	t.summarizePause(ctx)
	return nil
}

// summarizePause points the conversation list at the decision, which is the only
// place a reader who has not opened the card can see that it is waiting on them.
// It is the last write of a pause and never fails one: the decision is on the card
// by the time it runs, and a summary that could not be updated is only a label.
func (t *aiTurn) summarizePause(ctx context.Context) {
	cards := t.cards()
	if cards == nil {
		return
	}
	if err := cards.SetCardSummary(ctx, t.cardID, summaryAwaiting, t.seq); err != nil {
		slog.WarnContext(ctx, "set card summary failed", "error", err, "card_id", t.cardID)
	}
}

// armDecision records the outcome of a decision that is about to be delivered,
// so that the turn's log cannot lose it to a run that finishes first.
//
// It is called before the answer goes out, and the outcome is not drawn until it
// has: an armed outcome is deliberately invisible — not in the log, not on the
// card — so a delivery that fails can drop it and leave the card holding its
// buttons, which are the only way to answer a pause that is still waiting.
//
// It reports false when the turn has already ended: there is then no card left to
// draw on, and no reason to hold an outcome the final card has already been built
// without. A decision offered that late is one the backend refuses in turn.
func (t *aiTurn) armDecision(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return false
	}
	t.armedDecision = line
	return true
}

// disarmDecision drops an armed outcome whose delivery failed. The log never saw
// it, so there is nothing to take back off the card.
func (t *aiTurn) disarmDecision() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.armedDecision = ""
}

// settleDecision folds an armed outcome into the log and reports it. It is what
// turns "the answer was delivered" into "the card says so", and it is idempotent
// in the sense that matters here: whoever gets there first wins, and the other
// side finds nothing left to do.
//
// Both callers matter. A click that still has the card settles the outcome it
// armed and draws it; a turn that ended while the answer was in flight settles it
// on its way out, so the final card carries a decision that was delivered even
// though nobody got to draw it. A false second return means there was nothing
// armed — the delivery failed, or this is a pause that was never answered.
func (t *aiTurn) settleDecision() (string, bool) {
	t.mu.Lock()
	line := t.armedDecision
	t.armedDecision = ""
	t.mu.Unlock()
	if line == "" {
		return "", false
	}
	// Only now does the log learn of it, and only for an outcome that was
	// delivered: the log is what the final card is built from.
	t.decisionLog().append(line)
	return line, true
}

// resolveDecision draws a decision that has been delivered onto the card it was
// made on, and takes its buttons away.
//
// It is called after the answer has been delivered, never before: the buttons
// are the only way to answer a pause that is still waiting, and a card that lost
// them to a delivery that failed would strand the run with no way to continue it.
// The outcome was armed before the delivery for the same reason it is settled
// here rather than appended: the resumed run is already streaming on a goroutine
// of its own, and may have ended — and built its final card — before this point.
func (t *aiTurn) resolveDecision(ctx context.Context) {
	t.stopDecisionExpiry()
	// Already settled means the turn ended first and its final card carries the
	// outcome. There is no live card left to write to, and a closed one accepts no
	// element writes.
	if _, fresh := t.settleDecision(); !fresh {
		return
	}
	t.drawDecision(ctx)
	if cards := t.cards(); cards != nil {
		if err := cards.SetCardSummary(ctx, t.cardID, summaryThinking, t.seq); err != nil {
			slog.WarnContext(ctx, "restore card summary failed", "error", err, "card_id", t.cardID)
		}
	}
}

// cardError logs and swallows a card write that could not be made. Every write in
// this file is best-effort by design: the card is a view of a decision that has
// already been taken, and a view that failed to update must not fail the decision.
func (t *aiTurn) cardWarn(ctx context.Context, what string, err error) {
	if err != nil {
		slog.WarnContext(ctx, what, "error", err, "card_id", t.cardID, "run_id", t.runID)
	}
}

// recordDecision appends an outcome to the log and deletes the buttons it
// answers. It is the whole of what a decision does to the card, and it is
// separate from resolveDecision because the paths that abandon a turn — a new
// message taking over, a stop, the wait running out — change nothing else: their
// card is finalized a moment later, and a summary written now would be replaced
// by the final card anyway.
func (t *aiTurn) recordDecision(ctx context.Context, line string) {
	t.decisionLog().append(line)
	t.drawDecision(ctx)
}

// drawDecision puts the log as it now stands on the card and takes away the
// buttons of the pause it answers, leaving the log itself alone: the line is
// appended by the caller, because whether it has already been appended differs
// between them. A click settled its outcome before the answer was delivered and
// only needs it drawn; every other path appends and draws in one step.
func (t *aiTurn) drawDecision(ctx context.Context) {
	// The log is recorded before it is drawn, and the two are never taken
	// together: it is what the final card is built from, and a decision made by a
	// person is not something to lose because a card write failed.
	cards := t.cards()
	if cards == nil {
		return
	}
	log := t.decisionLog()
	if err := cards.UpdateCardElementText(ctx, t.cardID, decisionLogElementID, log.markdown(), t.seq); err != nil {
		slog.WarnContext(ctx, "update decision log failed", "error", err, "card_id", t.cardID)
	}
	if ids := t.takePendingActions(); len(ids) > 0 {
		if err := cards.DeleteCardElements(ctx, t.cardID, t.seq, ids...); err != nil {
			slog.WarnContext(ctx, "delete decision buttons failed", "error", err, "card_id", t.cardID)
		}
	}
}

// cards returns the turn's outbound card port, or nil when the caller has none —
// a REST or MCP caller renders no cards, and a turn only exists after one was
// created through this same port.
func (t *aiTurn) cards() runtime.LarkServices {
	if t.dr == nil {
		return nil
	}
	return t.dr.LarkServices
}

// clearAnswerPlaceholder empties the answer element while it still holds the
// "正在思考中" (thinking) placeholder.
//
// Emptied means a zero-width space rather than the empty string: cardkit refuses
// an element write whose content is empty ("code=99992402, msg=field validation
// failed"), which is how the placeholder stayed on the card through the whole
// pause on the machines this ran on. The character renders as nothing, and the
// first token of the answer overwrites it.
func (t *aiTurn) clearAnswerPlaceholder(ctx context.Context) error {
	cards := t.cards()
	if cards == nil {
		return errNoCardServices
	}
	return cards.UpdateCardText(ctx, t.cardID, placeholderCleared, t.seq)
}

// errNoCardServices is what a card write returns when there is no card port to
// write through: a caller that renders nothing. It is an error rather than a
// silent success because the caller decides what to do about it — the pause falls
// back to a card of its own, and the decision itself carries on either way.
var errNoCardServices = errors.New("no card services for this conversation")

// withoutInlineMark strips the mark that says a decision is drawn on the turn's
// card. A card that is a message of its own needs the opposite treatment — the
// click replaces it with a confirmation — so the fallback sends buttons that do
// not carry the mark, and the two paths cannot end up answering a click the same
// way on cards that are not the same.
func withoutInlineMark(extra map[string]string) map[string]string {
	out := make(map[string]string, len(extra))
	for k, v := range extra {
		if k == "inline" {
			continue
		}
		out[k] = v
	}
	return out
}

// cancelDecision records that a pause is being withdrawn rather than answered,
// and takes its buttons away. It is what the three ways a decision goes unmade
// have in common, and it says so on the card: the tool that was waiting did not
// run and will not.
func (t *aiTurn) cancelDecision(ctx context.Context, reason string) {
	if !t.hasPendingActions() && !t.paused() {
		return
	}
	t.recordDecision(ctx, decisionLineCancelled(reason))
}

// decisionTimeout is how long the writes that close a decision get, once the
// turn's own deadline has already passed.
const decisionTimeout = 2 * time.Second

// armDecisionExpiry starts the clock that ends a pause nobody answers.
//
// A pause has to end by itself. The turn's own context expires after its
// timeout and the grace window — but nothing watches that: the run is parked on
// the backend, no event will ever arrive, and so a card left with two live
// buttons would stand in the conversation for ever, offering a decision nobody
// can make any more.
func (t *aiTurn) armDecisionExpiry() {
	t.mu.Lock()
	if t.ended || t.expired {
		t.mu.Unlock()
		return
	}
	if t.expiry != nil {
		t.expiry.Stop()
	}
	// The timer belongs to the pause that armed it: a second pause starts its own
	// wait rather than inheriting what is left of the first.
	t.expiry = time.AfterFunc(approvalWaitGrace, t.expireDecision)
	t.mu.Unlock()
}

// stopDecisionExpiry cancels the wait, if one is running.
func (t *aiTurn) stopDecisionExpiry() {
	t.mu.Lock()
	timer := t.expiry
	t.expiry = nil
	t.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// expiredWait reports whether this turn's last pause ran out of time.
func (t *aiTurn) expiredWait() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

// expireDecision ends a pause nobody answered.
//
// The turn is over as far as a reader can tell — no button will resume it and
// nothing more will be rendered into it — so it is closed here rather than left
// standing. It runs on a context of its own: the turn's context died when its
// deadline passed, which is what the wait running out means.
func (t *aiTurn) expireDecision() {
	t.mu.Lock()
	if t.ended || t.expired {
		t.mu.Unlock()
		return
	}
	t.expired = true
	t.expiry = nil
	// An ended turn, in the vocabulary the card already has: nothing was decided,
	// so nothing ran, and the log line above says why.
	t.status, t.stopped = larkcard.RichCardStatusError, true
	t.mu.Unlock()

	// Recorded before the turn is closed, because closing it is what deregisters
	// the turn: from here on, the only thing that still knows this pause is over is
	// the conversation's note that its wait ran out — and a button somebody is
	// still looking at can be clicked minutes from now.
	t.b.recordExpiredPause(t.dr.Address(), t.runID)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.base), decisionTimeout)
	defer cancel()

	t.recordDecision(ctx, decisionLineExpired())
	t.finish()
}

// decisionLog returns the turn's log, creating it on first use.
func (t *aiTurn) decisionLog() *decisionLog {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.decision == nil {
		t.decision = &decisionLog{}
	}
	return t.decision
}

// decisionLines returns what the log holds, for the final card.
func (t *aiTurn) decisionLines() []string {
	t.mu.Lock()
	log := t.decision
	t.mu.Unlock()
	return log.snapshot()
}

// decisionElements renders the log as the card components the final card carries.
// A turn that never paused returns nothing, which is how the card is left exactly
// as it was built before this existed.
func (t *aiTurn) decisionElements() []map[string]any {
	content := decisionLogMarkdown(t.decisionLines())
	if content == "" {
		return nil
	}
	return []map[string]any{larkcard.ElementMarkdown(decisionLogElementID, content)}
}

// nextDecisionSlot numbers the pauses of this turn, so that each one's buttons
// get an element id of their own.
func (t *aiTurn) nextDecisionSlot() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.decisionSlot++
	return t.decisionSlot
}

// setPendingActions records the element ids of the live pause's buttons.
func (t *aiTurn) setPendingActions(ids ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingActions = ids
}

// takePendingActions returns the live pause's button ids and forgets them, so
// that a second resolution cannot try to delete them again.
func (t *aiTurn) takePendingActions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := t.pendingActions
	t.pendingActions = nil
	return ids
}

// hasPendingActions reports whether a pause's buttons are still on the card.
func (t *aiTurn) hasPendingActions() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pendingActions) > 0
}

// setInlineDecision records whether this pause's decision lives on the turn's
// card. It is how a click knows whether answering it rewrites the card it came
// from or replaces the message it was sent on.
func (t *aiTurn) setInlineDecision(inline bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inlineDecision = inline
}

// inlineDecisionOn reports whether the decision is drawn on the turn's card.
func (t *aiTurn) inlineDecisionOn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inlineDecision
}

// decisionLogHasElement reports whether the log element has been created on the
// card, which is what decides whether a pause inserts it or rewrites it.
func (l *decisionLog) hasLogElement() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.onCard
}

// setLogElement records that the log element now exists on the card.
func (l *decisionLog) setLogElement(on bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onCard = on
}
