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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"golang.org/x/sync/singleflight"

	"github.com/onexstack/onexai/internal/chat/biz/v1/oncall"
	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/oncallsummary"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// Bounds on what reaches the prompt.
//
// The transcript limit matches the chat service's own cap on a conversation, which
// is its reading of what a conversation is; asking for more would be asking for a
// number it has already decided against. The two rune budgets are what fits in a
// prompt without pushing the material out of the model's attention, and they are
// measured in runes because the material is mostly Chinese.
const (
	summaryTranscriptLimit    = 50
	summaryMaxTranscriptRunes = 12000
	summaryMaxNotesRunes      = 4000
)

// briefMarkerNotice is what the group is told when the model declines to write a
// ticket up.
//
// The ticket is not left looking unimplemented: its summary is the marker, and
// this says the same thing to the people who just closed it. It is not an error
// and must not read like one — "there was nothing worth recording" is a completed
// outcome, and a failure notice would have somebody looking for a fault.
const briefMarkerNotice = "这条工单没有需要复盘的内容，已记为「记录简略」。"

// summaryFlights serialises the summaries of one ticket within this process.
//
// Two `/close` invocations can arrive together — a double tap, a retried card
// action — and each would otherwise pay for its own model run. It is the first of
// three layers: this one costs nothing and covers the common case, the conditional
// UPDATE in the store covers a second process, and re-reading the ticket inside
// the flight covers the case where the summary landed between the two.
var summaryFlights singleflight.Group

// launchSummary starts a ticket's closing write-up in the background.
//
// # Why it is not awaited
//
// Closing a ticket is a decision a person made and is waiting on; summarizing it is
// work the platform owes that ticket. Making the first depend on the second would
// turn a model outage into "you cannot close tickets", which is a worse failure
// than a missing summary — so the close completes, the reply goes out, and this
// runs beside it. A failure reaches the group as a message and the log as an
// error; it does not reach the caller as an exception, because by then there is no
// caller.
//
// # What it copies, and why
//
// dr is per-request: its SessionM and TicketM are pointers other commands in the
// same turn may still be writing. The goroutine takes the values it needs and
// nothing else, so it cannot observe a half-updated request. sr is process-wide
// and safe to capture.
func launchSummary(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, ticket *model.TicketM) {
	if sr == nil || ticket == nil || ticket.TicketID == "" {
		return
	}
	recv, lark := dr.Receiver, dr.LarkServices
	ticketID, title, description := ticket.TicketID, ticket.Title, ticket.Description

	slog.InfoContext(ctx, "oncall: the closing summary is being generated",
		"ticket", ticketID, "group", ticket.GroupID)

	runAsync(sr, func() {
		// Detached on purpose: the request's context is cancelled when the reply
		// is delivered, which is before the model has been asked anything.
		ctx := context.Background()

		text, err := generateWithinFlight(ctx, sr, ticketID, title, description)
		if err != nil {
			metrics.M.RecordOncallSummary(ctx, metrics.SummaryOutcomeFailed)
			slog.ErrorContext(ctx, "oncall: the closing summary could not be generated",
				"ticket", ticketID, "err", err)
			sayInGroup(ctx, lark, recv, fmt.Sprintf(
				"结案总结生成失败，这条工单暂时不会有结案记录。可以再发一次 /close 重试。（%s）", err.Error()))
			return
		}

		// Canonicalised before it is stored: the readers compare exact strings, and
		// the tolerance for the model's punctuation lives in one place. See
		// known/chat.CanonicalSummary.
		text = chatknown.CanonicalSummary(text)

		wrote, err := sr.Store.Ticket().UpdateSummary(ctx, ticketID, text, time.Now())
		if err != nil {
			metrics.M.RecordOncallSummary(ctx, metrics.SummaryOutcomeFailed)
			slog.ErrorContext(ctx, "oncall: the closing summary could not be saved",
				"ticket", ticketID, "err", err)
			sayInGroup(ctx, lark, recv, "结案总结已生成，但写入工单失败。可以再发一次 /close 重试。")
			return
		}
		if !wrote {
			// Somebody else wrote it first: another /close, another replica, or a
			// retry that finished while this one was still thinking. Nothing to say
			// in the group — whoever won has already said it.
			slog.InfoContext(ctx, "oncall: a closing summary for this ticket already existed",
				"ticket", ticketID)
			return
		}

		slog.InfoContext(ctx, "oncall: closing summary saved",
			"ticket", ticketID, "runes", len([]rune(text)))

		// TODO(regen): push the summary to the incident platform when the ticket came
		// from there. It cannot happen yet — the Regen client's UpdateIncident carries
		// only status and severity, and its Create is the only call with a free-text
		// field — so this is where it goes once that API accepts one. The ticket
		// already knows which case it is: `source = "regen"` is written at creation by
		// the store decorator that mirrors the row there, and RegenIncidentID is the
		// handle. Two ways to close the gap, in the order worth trying: give the
		// incident a resolution field, or use a note/comment endpoint if the platform
		// has one. Until then the summary lives on the ticket and nowhere else.
		if chatknown.IsBriefSummary(text) {
			// A decline is a completed outcome, not a failure — see the outcome
			// constants, which exist so a run of these does not read as an outage.
			metrics.M.RecordOncallSummary(ctx, metrics.SummaryOutcomeBrief)
			sayInGroup(ctx, lark, recv, briefMarkerNotice)
			return
		}
		metrics.M.RecordOncallSummary(ctx, metrics.SummaryOutcomeWritten)
		sayInGroup(ctx, lark, recv, "结案总结已生成："+text)
	})
}

// runAsync starts f away from the caller, or beside it when the runtime says so.
//
// See StaticRuntime.Async: the indirection is what lets a test observe work that
// a request does not wait for.
func runAsync(sr *runtime.StaticRuntime, f func()) {
	if sr.Async != nil {
		sr.Async(f)
		return
	}
	go f()
}

// summarizeTicket assembles one ticket's material and asks the model for the
// write-up.
func summarizeTicket(ctx context.Context, sr *runtime.StaticRuntime, ticketID, title, description string) (string, error) {
	// The handling log first: it is the only written record of what was done, and
	// unlike the conversation it cannot be recovered from anywhere else.
	_, notes, err := sr.Store.TicketNote().ListByTicket(ctx, ticketID, 0)
	if err != nil {
		return "", fmt.Errorf("读取处理记录失败: %w", err)
	}

	var (
		transcript string
		missing    []string
	)
	if sr.Oncall == nil {
		// Not wired: the summary is still worth writing from the ticket's own
		// fields, and saying the conversation is unavailable keeps the model from
		// reading its absence as "nothing happened in the group".
		missing = append(missing, "工单群的聊天记录（本部署未接线）")
	} else {
		conv, err := sr.Oncall.Transcript(ctx, ticketID)
		if err != nil {
			return "", fmt.Errorf("读取工单群会话失败: %w", err)
		}
		var dropped int
		transcript, dropped = renderTranscript(conv.Messages)
		if dropped > 0 {
			missing = append(missing, fmt.Sprintf("较早的聊天记录（已省略 %d 条）", dropped))
		}
		switch conv.Source {
		case oncall.SourceArchive:
			// Said out loud because it changes what the write-up can claim: the
			// archive is what was copied out before the group was dissolved, so it is
			// a snapshot rather than the conversation.
			missing = append(missing, "工单群已解散（记录取自解散时的归档）")
		case oncall.SourceNone:
			missing = append(missing, "工单群的聊天记录")
		}
	}

	prompt := oncallsummary.Material{
		TicketID:    ticketID,
		Title:       title,
		Description: description,
		Notes:       renderNotes(notes),
		Transcript:  transcript,
		Missing:     missing,
	}.Prompt()

	// One bucket per ticket: two tickets sharing one would let one incident's
	// material into the other's summary. See Generate.
	return oncallsummary.Generate(ctx, sr.Clientset.Agent(), "oncall-summary:"+ticketID, prompt)
}

// GenerateWithinFlight is summarizeTicket behind the per-ticket de-duplication.
//
// It exists so the two callers — the close path and the retry path — cannot
// disagree about whether the flight is taken. See summaryFlights.
func generateWithinFlight(ctx context.Context, sr *runtime.StaticRuntime, ticketID, title, description string) (string, error) {
	v, err, _ := summaryFlights.Do(ticketID, func() (any, error) {
		// Re-read before spending anything. The cheap in-process flight covers two
		// calls arriving together in this process; this covers a summary that was
		// written by another process (or an earlier flight) while we were queued.
		if current, gerr := sr.Store.Ticket().Get(ctx, where.F("ticket_id", ticketID)); gerr == nil && current != nil {
			if strings.TrimSpace(current.Summary) != "" {
				return current.Summary, nil
			}
		}
		return summarizeTicket(ctx, sr, ticketID, title, description)
	})
	if err != nil {
		return "", err
	}
	text, _ := v.(string)
	return text, nil
}

// renderNotes writes the handling log, one entry per line, oldest first.
//
// The author and the time travel with each line because they are what makes the log
// trustworthy to a reader: 「先重启了网关」 (restarted the gateway first) is a claim, and 「2026-10-09 14:20 Lingfei Kong：
// 先重启了网关」 (2026-10-09 14:20, Kong Lingfei: restarted the gateway first) is a record of who claimed it and when.
func renderNotes(notes []*model.TicketNoteM) string {
	var b strings.Builder
	for _, n := range notes {
		if n == nil {
			continue
		}
		who := strings.TrimSpace(n.AuthorName)
		if who == "" {
			who = strings.TrimSpace(n.AuthorID)
		}
		marks := ""
		if n.Review {
			marks += " [需复盘]"
		}
		if n.Demand {
			marks += " [转需求]"
		}
		fmt.Fprintf(&b, "- %s %s%s：%s\n",
			n.CreatedAt.Format("2006-01-02 15:04"), who, marks, strings.TrimSpace(n.Note))
	}
	return clampRunes(b.String(), summaryMaxNotesRunes)
}

// renderTranscript writes the conversation and reports how many messages it
// dropped.
//
// # Why the trim keeps the tail
//
// The two ends of this material are not equally valuable. The tail is where the
// conversation ended, and for a ticket that ends when it is solved, that is where
// the answer is; the head is where the problem was stated, which the ticket's own
// title and description already carry. So the trim keeps the *newest* messages,
// and hands back the count it dropped rather than pretending it had them all — a
// model that believes it read the whole conversation writes a summary that does not
// mention the gap, and a reader has no way to notice.
func renderTranscript(msgs []*im.Message) (string, int) {
	kept := make([]string, 0, len(msgs))
	used := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m == nil {
			continue
		}
		// Cards and images carry no words this path can use. A card is the bot's
		// own rendering of something already in the log, and an image is a
		// reference to bytes nobody here reads.
		if t := strings.TrimSpace(m.Content.Type); t != "" && t != "text" && t != "rich_text" {
			continue
		}
		text := strings.TrimSpace(m.Content.Text)
		if text == "" {
			continue
		}
		who := strings.TrimSpace(m.SenderName)
		if who == "" {
			who = strings.TrimSpace(m.SenderID)
		}
		if m.IsBot {
			// Named rather than dropped: the bot's own replies are often the answer
			// ("/oncall 建单成功" (ticket created successfully), a command's output), and a summary that silently
			// lost them would look complete.
			who = "机器人"
		}
		line := fmt.Sprintf("%s %s：%s", m.CreateTime.Format("01-02 15:04"), who, text)
		cost := len([]rune(line))
		if used+cost > summaryMaxTranscriptRunes && len(kept) > 0 {
			return joinChronological(kept), i + 1
		}
		kept = append(kept, line)
		used += cost
	}
	return joinChronological(kept), 0
}

// joinChronological reverses the newest-first accumulation back into reading order.
func joinChronological(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[len(lines)-1-i] = l
	}
	return strings.Join(out, "\n")
}

// clampRunes truncates to n runes, saying that it did.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n（其余略）"
}

// sayInGroup posts a follow-up message about a summary.
//
// It cannot use the command's Responder: that one buffers for the lifetime of the
// command and is flushed when the command returns, which is before this goroutine
// has an answer. A nil LarkServices means the caller was REST or MCP — there is no
// group to speak to, and the summary is a database row for them either way.
//
// The ordering is an assumption rather than a guarantee: this message and the
// close reply travel by different paths, and in practice the model takes seconds
// while the flush takes milliseconds. Writing it here because the alternative — a
// user who sees only "正在生成" (generating) and never the answer — is a worse failure than a
// rare out-of-order pair.
func sayInGroup(ctx context.Context, lark runtime.LarkServices, recv *v1.Receiver, content string) {
	if lark == nil || recv == nil || strings.TrimSpace(content) == "" {
		return
	}
	if err := lark.SendText(ctx, recv, content); err != nil {
		slog.WarnContext(ctx, "oncall: could not post the summary result to the group", "err", err)
	}
}
