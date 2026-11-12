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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
)

// Approval decision values carried in "perm:*" card buttons.
const (
	PermAllow    = "perm:allow"
	PermDeny     = "perm:deny"
	PermAllowAll = "perm:allow_all"
)

// What a permission decision is called, in one place: the buttons that offer it,
// the log line that records it, and the card the fallback path replaces its
// message with all read from here, so no two of them can end up saying different
// things about the same decision.
//
// The characters are the card's own vocabulary (larkcard.Glyph*). They are bare
// characters rather than emoji because a card 2.0 button label is plain text —
// a coloured tag cannot go inside one — so the colour has to come from the
// button's type, and the glyph has to carry the rest.
const (
	buttonAllowText    = larkcard.GlyphDone + " 允许"
	buttonDenyText     = larkcard.GlyphFailed + " 拒绝"
	buttonAllowAllText = larkcard.GlyphDone + " 总是允许"

	labelAllowed    = "已允许"
	labelDenied     = "已拒绝"
	labelAllowedAll = "总是允许"
)

// permDecisions maps a permission button's value onto the decision the backend
// is asked for. It is a closed map rather than a prefix strip so that a button
// value this build does not know answers nothing at all, instead of forwarding
// an arbitrary string as somebody's decision about running a tool.
var permDecisions = map[string]string{
	PermAllow:    agent.DecisionAllow,
	PermDeny:     agent.DecisionDeny,
	PermAllowAll: agent.DecisionAllowAll,
}

// askerIdentity is the ownership identity of whoever triggered a turn: the
// platform's stable id for them, never a display name.
//
// It is what AskRequest carries as the sender — the MCP tools scope the records
// they touch by it — and what an approval is tied to, so that the person who
// answers a pause can be compared with the person who caused it.
func askerIdentity(dr *runtime.DynamicRuntime) string {
	return dr.Identity()
}

// sendApprovalCard sends a permission-request card when an agent run pauses on
// a human-approval interrupt. The card's buttons carry the run ID and the pause
// it answers, so a callback resumes the exact run and the exact pause it was
// rendered for.
//
// It is the fallback: normally the pause is drawn on the turn's own card, and
// this is what happens when that card will not take it. The buttons it builds are
// the same decision with the same words, minus the mark that would make a click
// try to rewrite a card that never got the decision in it.
func (b *botBiz) sendApprovalCard(ctx context.Context, dr *runtime.DynamicRuntime, runID string, it *agent.Interrupt) {
	if dr == nil || dr.Receiver == nil || it == nil {
		return
	}
	reqs := it.Permissions()
	if len(reqs) == 0 {
		return
	}

	blk := b.approvalDecision(dr, runID, it)
	if blk == nil {
		return
	}
	body := approvalBody(reqs)
	// The standalone card is the whole message, so it carries the confirmation a
	// click replaces it with: the live card does not need one, because it records
	// the outcome in its own log instead.
	withResult := func(btn larkcard.CardButton, label, color string) larkcard.CardButton {
		btn.Extra = withoutInlineMark(btn.Extra)
		btn.Extra["perm_label"] = label
		btn.Extra["perm_color"] = color
		btn.Extra["perm_body"] = body
		return btn
	}

	note := "点击按钮以继续或终止本次执行"
	if len(reqs) > 1 {
		note = "一次点击决定以上全部工具：允许则全部执行，拒绝则全部不执行"
	}

	cb := larkcard.NewCard().Title("权限请求", "orange").Markdown(body).Markdown(blk.request)
	if len(blk.buttons) == 2 {
		cb = cb.ButtonsEqual(
			withResult(blk.buttons[0], labelAllowed, "green"),
			withResult(blk.buttons[1], labelDenied, "red"),
		)
	} else if len(blk.buttons) > 0 {
		cb = cb.Buttons(blk.buttons...)
	}
	if len(blk.secondary) > 0 {
		cb = cb.Buttons(withResult(blk.secondary[0], labelAllowedAll, "green"))
	}

	if err := b.SendCardModel(ctx, dr.Receiver, cb.Note(note).Build()); err != nil {
		slog.WarnContext(ctx, "send approval card failed", "error", err)
	}
}

// approvalBody renders the pending calls for the card body. A single call keeps
// the original two-line shape; a group is listed in full, since that is the whole
// point of showing a group at all.
func approvalBody(reqs []agent.InterruptRequest) string {
	if len(reqs) == 1 {
		return "**工具**：" + approvalToolNames(reqs)[0] + "\n\n**请求**：" + reqs[0].Question
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**待批工具**（共 %d 项）", len(reqs))
	for i, r := range reqs {
		fmt.Fprintf(&b, "\n\n%d. **%s**", i+1, toolNameOrAgent(r))
		if r.Question != "" {
			b.WriteString("\n" + r.Question)
		}
	}
	return b.String()
}

// approvalPrompt renders a pause as one plain sentence, for the places that have
// no card to put a list in: the re-prompt that tells a user their reply was not
// understood, and the log.
func approvalPrompt(reqs []agent.InterruptRequest) string {
	if len(reqs) == 1 {
		if reqs[0].Question != "" {
			return reqs[0].Question
		}
		return fmt.Sprintf("工具 %s 请求执行，是否允许？", toolNameOrAgent(reqs[0]))
	}
	return fmt.Sprintf("%d 个工具待批准：%s", len(reqs), strings.Join(approvalToolNames(reqs), "、"))
}

// approvalToolNames lists the tools a pause waits on, in the order it reported
// them.
func approvalToolNames(reqs []agent.InterruptRequest) []string {
	names := make([]string, 0, len(reqs))
	for _, r := range reqs {
		names = append(names, toolNameOrAgent(r))
	}
	return names
}

// toolNameOrAgent names the tool a request is about, falling back to the agent
// itself for an interrupt point that belongs to no single tool.
func toolNameOrAgent(r agent.InterruptRequest) string {
	if r.ToolName != "" {
		return r.ToolName
	}
	return "agent"
}

// approvalButton builds a CardButton carrying the approval action and extras.
func approvalButton(text, typ, value string, extra map[string]string) larkcard.CardButton {
	return larkcard.CardButton{Text: text, Type: typ, Value: value, Extra: extra}
}

// renderApprovalResultCard rebuilds a green/red confirmation card from the
// button extras captured at send time, mirroring cc-connect's perm callback.
//
// It replaces the request card, which has done its job once it has been
// answered. The run itself is not finished — it continues on the streaming card,
// which the answer resumes — so this card states the decision and says nothing
// about the outcome.
func renderApprovalResultCard(label, color, body string) *larkcard.Card {
	return larkcard.NewCard().
		Title(label, color).
		Markdown(body).
		Build()
}

// sendAskQuestionCard sends an AskUserQuestion card when an agent run pauses on
// a question interrupt. Each option becomes a button carrying the selected
// answer, so the callback can resume the run with that answer.
//
// It is the fallback for a question that could not be drawn on the turn's card,
// and it is the card at its most useful: the question is on screen with its
// options, and nothing else is.
//
// One question is put at a time even when the pause carries several: an answer is
// free text that has to be attached to one interrupt point, and a group of them
// would need a form rather than a card. The rest are not lost — an unanswered
// question re-interrupts, so the next pause asks it.
func (b *botBiz) sendAskQuestionCard(
	ctx context.Context, dr *runtime.DynamicRuntime, runID string, it *agent.Interrupt, req agent.InterruptRequest,
) {
	if dr == nil || dr.Receiver == nil {
		return
	}

	blk := b.questionDecision(dr, runID, it, req)
	if blk == nil {
		return
	}
	question := req.Question
	if question == "" {
		question = "请选择一个选项"
	}

	cb := larkcard.NewCard().Title("请选择", "blue").Markdown(blk.request)
	if len(blk.buttons) > 0 {
		// The options become list items rather than a row of buttons: a message of
		// its own has the room to show each option in full, and an option can be a
		// sentence.
		for _, btn := range blk.buttons {
			cb = cb.ListItemBtnExtra(btn.Text, "选择", "default", btn.Value, withoutInlineMark(btn.Extra))
		}
		cb = cb.Note("点击选择答案")
	}

	if err := b.SendCardModel(ctx, dr.Receiver, cb.Build()); err != nil {
		slog.WarnContext(ctx, "send ask question card failed", "error", err)
	}
}

// answererAllowed reports whether the person who clicked may answer this
// conversation's pending decision.
//
// The asker is the authority by default: a pause is about what their own
// instruction is allowed to do. The SRE and admin roles are the override, which
// is what makes a pause answerable when the asker has gone home or is the one
// who needs a second pair of eyes.
//
// Two identities that cannot be compared allow the answer through rather than
// refusing it. A missing clicker (a callback shape that does not report one) and
// a pause recorded before this was tracked are both "unknown", not "wrong", and
// refusing on either would strand a run with no way to continue it.
func (b *botBiz) answererAllowed(sessionKey, clicker string) bool {
	if clicker == "" {
		return true
	}
	sess := b.liveConversation(sessionKey)
	if sess == nil {
		return true
	}
	pending := sess.PendingPermission()
	if pending == nil || pending.AskedBy == "" || pending.AskedBy == clicker {
		return true
	}
	// The role arm is decided by the configured SRE/admin lists (see
	// pkg/runtime.StaticRuntime). A deployment that lists neither admits no one
	// here, so only the asker — or a conversation with no pending asker — passes,
	// which is the safe direction for a permission prompt.
	return b.sr.IsSREUser(clicker) || b.sr.IsAdminUser(clicker)
}

// recordPendingPermission remembers the pause this conversation is now blocked
// on, so a reply typed as plain text can be delivered without the card's buttons
// — which is how a multi-select question is answered anyway ("请回复选项编号", i.e.
// reply with the option number), and how anyone replies when they would rather type "允许" (allow) than aim at a button
// on a phone.
//
// Nothing is recorded when no session manager is configured: without one there is
// nowhere to hold the question, and the buttons still work.
func (b *botBiz) recordPendingPermission(dr *runtime.DynamicRuntime, pending *session.PendingPermission) {
	if dr == nil || dr.Receiver == nil || pending == nil || pending.RunID == "" {
		return
	}
	sess, err := b.conversation(dr.Address())
	if err != nil || sess == nil {
		return
	}
	pending.AskedAt = time.Now()
	sess.SetPendingPermission(pending)
	// This conversation is waiting on something again, so whatever pause it last
	// let expire is no longer the answer to what a stale click is about.
	b.clearExpiredPause(dr.Address())
}

// clearPendingPermission forgets the question for runID, if that is the one the
// conversation is waiting on. A question that has been superseded keeps waiting:
// the run it belongs to is still blocked on it.
func (b *botBiz) clearPendingPermission(sessionKey, runID string) {
	if sess := b.liveConversation(sessionKey); sess != nil {
		sess.ClearPendingPermission(runID)
	}
}
