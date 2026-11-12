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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// TestAppendSteps pins how the port's tool steps land in the rich-card step
// list: every field the backend reports is carried, and an invocation reported
// more than once stays one row.
//
// Malformed input is not covered here because it cannot reach this function any
// more: the port's ToolStep is a value, not a pointer, so the "nil entry" case
// that the proto accessor forced is settled in the llm adapter's translation
// instead.
func TestAppendSteps(t *testing.T) {
	t.Run("a tool step becomes a tool-lane entry", func(t *testing.T) {
		got := appendSteps(nil, []agent.ToolStep{
			{ID: "call_a", Name: "grep", Status: "ok", Duration: 234 * time.Millisecond, Tokens: 820},
			{ID: "call_b", Name: "Bash", Status: "pending"},
		})
		if len(got) != 2 {
			t.Fatalf("appendSteps = %d steps, want 2", len(got))
		}
		want := larkcard.RichStep{
			Kind: larkcard.RichStepKindTool, ID: "call_a", Name: "grep",
			Status: "ok", DurationMs: 234, Tokens: 820,
		}
		if got[0] != want {
			t.Errorf("step = %+v, want %+v", got[0], want)
		}
		if got[1].Status != "pending" {
			t.Errorf("status = %q, want pending", got[1].Status)
		}
	})

	t.Run("a repeat of one invocation updates its row", func(t *testing.T) {
		// The backend reports a tool when it starts, when it ends, and again
		// when the next model call prices its result. Three reports, one row.
		got := appendSteps(nil, []agent.ToolStep{{ID: "call_a", Name: "grep", Status: "pending"}})
		got = appendSteps(got, []agent.ToolStep{{
			ID: "call_a", Name: "grep", Status: "ok", Duration: 234 * time.Millisecond,
		}})
		got = appendSteps(got, []agent.ToolStep{{ID: "call_a", Name: "grep", Status: "ok", Tokens: 820}})

		if len(got) != 1 {
			t.Fatalf("appendSteps = %+v, want the three reports collapsed into one row", got)
		}
		want := larkcard.RichStep{
			Kind: larkcard.RichStepKindTool, ID: "call_a", Name: "grep",
			Status: "ok", DurationMs: 234, Tokens: 820,
		}
		if got[0] != want {
			t.Errorf("row = %+v, want %+v — a silent later report must not erase the figures already shown", got[0], want)
		}
	})

	t.Run("steps with no id can only be appended", func(t *testing.T) {
		// A backend that reports no call id (the local CLI does not) gets a row
		// per report; there is nothing to match them on.
		got := appendSteps(nil, []agent.ToolStep{
			{Name: "exec", Status: "pending"},
			{Name: "exec", Status: "ok"},
		})
		if len(got) != 2 {
			t.Fatalf("appendSteps = %+v, want both reports kept", got)
		}
	})

	t.Run("appends to an existing list without mutating it", func(t *testing.T) {
		existing := []larkcard.RichStep{{Kind: larkcard.RichStepKindThinking, Name: "t"}}
		got := appendSteps(existing, []agent.ToolStep{{Name: "exec", Status: "pending"}})
		if len(got) != 2 {
			t.Fatalf("appendSteps = %d steps, want 2", len(got))
		}
		if got[0].Name != "t" {
			t.Errorf("existing step reordered: %+v", got[0])
		}
		if len(existing) != 1 {
			t.Errorf("the existing slice grew in place: %+v", existing)
		}
	})

	t.Run("an update does not write through the caller's slice", func(t *testing.T) {
		// The update path rewrites an element, so it has to work on a copy: a
		// caller (or another goroutine) still holding the old slice must not see
		// it change. A slice with spare capacity is the case that catches an
		// implementation which only copies on append.
		existing := make([]larkcard.RichStep, 1, 8)
		existing[0] = larkcard.RichStep{Kind: larkcard.RichStepKindTool, ID: "call_a", Name: "grep", Status: "pending"}

		got := appendSteps(existing, []agent.ToolStep{{
			ID: "call_a", Name: "grep", Status: "ok", Duration: 234 * time.Millisecond,
		}})
		if got[0].Status != "ok" || got[0].DurationMs != 234 {
			t.Fatalf("row = %+v, want it updated", got[0])
		}
		if existing[0].Status != "pending" || existing[0].DurationMs != 0 {
			t.Errorf("the caller's slice was written through: %+v", existing[0])
		}
	})

	t.Run("no reported steps is a no-op", func(t *testing.T) {
		if got := appendSteps(nil, nil); len(got) != 0 {
			t.Errorf("appendSteps(nil, nil) = %+v, want empty", got)
		}
	})

	t.Run("what the tool returned reaches the row", func(t *testing.T) {
		// The result is what makes a claim about a tool's effect checkable: the
		// card shows the trigger's own output beside the sentence describing it.
		const body = `{"triggered":true,"task_id":39}`
		got := appendSteps(nil, []agent.ToolStep{
			{ID: "call_a", Name: "pipeline", Status: "ok", Result: body},
		})
		if got[0].Result != body {
			t.Errorf("result = %q, want the tool's own output", got[0].Result)
		}
	})

	t.Run("a result is redacted on the way in", func(t *testing.T) {
		// Tool output is free-form text produced by a command that could read the
		// process's configuration, so it is treated as untrusted for display. The
		// redaction happens here rather than in a renderer so that no rendering
		// path can be added later that forgets it.
		got := appendSteps(nil, []agent.ToolStep{{
			ID: "call_a", Name: "pipeline", Status: "ok",
			Result: `{"app_secret":"s3cr3t-value"}`,
		}})
		if strings.Contains(got[0].Result, "s3cr3t-value") {
			t.Errorf("a secret reached the card's row: %q", got[0].Result)
		}
		if !strings.Contains(got[0].Result, "[redacted]") {
			t.Errorf("result %q was not redacted", got[0].Result)
		}
	})

	t.Run("a silent later report does not blank the result", func(t *testing.T) {
		// The result arrives with the report that ends the call. The report after
		// it carries tokens alone, so writing its empty result over the row would
		// erase the one piece of evidence the card has.
		got := appendSteps(nil, []agent.ToolStep{
			{ID: "call_a", Name: "pipeline", Status: "ok", Result: "triggered"},
		})
		got = appendSteps(got, []agent.ToolStep{
			{ID: "call_a", Name: "pipeline", Status: "ok", Tokens: 820},
		})

		if len(got) != 1 {
			t.Fatalf("appendSteps = %+v, want one row", got)
		}
		if got[0].Result != "triggered" {
			t.Errorf("result = %q, want it kept — the token report says nothing about the output", got[0].Result)
		}
		if got[0].Tokens != 820 {
			t.Errorf("tokens = %d, want the later figure", got[0].Tokens)
		}
	})
}

// TestRenderToolsProgress pins the block that streams into the card while the
// turn runs. It is rewritten in full on every update, so an unstable rendering —
// a trailing newline, an icon that changes between calls for the same status —
// shows up as a card that jitters rather than as a wrong answer.
func TestRenderToolsProgress(t *testing.T) {
	now := time.Now()

	if got := renderToolsProgress(nil, nil, now); got != "" {
		t.Errorf("renderToolsProgress(nil) = %q, want nothing to render", got)
	}

	steps := []larkcard.RichStep{
		{Kind: larkcard.RichStepKindTool, ID: "a", Name: "grep", Status: "ok", DurationMs: 234, Tokens: 820},
		{Kind: larkcard.RichStepKindTool, ID: "b", Name: "Bash", Status: "fail"},
		{Kind: larkcard.RichStepKindTool, ID: "c", Name: "Read", Status: "pending"},
	}
	got := renderToolsProgress(steps, nil, now)

	// One line per step, no trailing blank line: the block is appended to a card
	// that already has a footer under it.
	if lines := strings.Split(got, "\n"); len(lines) != 3 {
		t.Fatalf("renderToolsProgress = %q, want one line per step", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("renderToolsProgress = %q, want no trailing newline", got)
	}
	// Each status gets its own glyph, coloured, and the tool's display title
	// rather than the raw name the backend reported.
	for _, want := range []string{
		"<font color='green'>✓</font> Search text",
		"<font color='red'>✗</font> Run command",
		"<font color='blue'>⟳</font> Read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderToolsProgress = %q, want it to contain %q", got, want)
		}
	}
	// A finished tool shows what the backend measured. An unreported figure is
	// left out rather than shown as zero.
	if !strings.Contains(got, "<font color='green'>✓</font> Search text（⚡ 234ms | ✨ 820）") {
		t.Errorf("renderToolsProgress = %q, want the finished tool's figures", got)
	}
	if strings.Contains(got, "Run command（") {
		t.Errorf("renderToolsProgress = %q, must not invent figures for a tool that reported none", got)
	}

	// The same input renders the same text: the card updates in place, and a
	// rendering that differed between calls would be an update for nothing.
	if again := renderToolsProgress(steps, nil, now); again != got {
		t.Errorf("two calls rendered differently:\n%q\n%q", got, again)
	}

	// An unknown status is not a missing step. A status this build does not know
	// still names the tool that ran, and prints the value it was given — with no
	// colour, because that text is upstream's and a tag is what it could break out
	// of — rather than a neutral mark that could pass for a successful run.
	got = renderToolsProgress([]larkcard.RichStep{{Name: "exec", Status: "who-knows"}}, nil, now)
	if !strings.Contains(got, "Run command") || !strings.Contains(got, "who-knows") {
		t.Errorf("renderToolsProgress = %q, want an unrecognised status kept and shown verbatim", got)
	}
	if strings.Contains(got, "<font") {
		t.Errorf("renderToolsProgress = %q, want no colour tag around an unrecognised status", got)
	}
}

// TestRenderToolsProgressPendingElapsed covers the figure the backend cannot
// supply yet: until an invocation ends there is no measured duration, so the row
// shows how long the client has been waiting for it, counted from the moment the
// step first arrived.
func TestRenderToolsProgressPendingElapsed(t *testing.T) {
	began := time.Now()
	started := map[string]time.Time{"c": began}
	steps := []larkcard.RichStep{
		{Kind: larkcard.RichStepKindTool, ID: "c", Name: "Read", Status: "pending"},
	}

	got := renderToolsProgress(steps, started, began.Add(1200*time.Millisecond))
	if !strings.Contains(got, "<font color='blue'>⟳</font> Read（⚡ 1.2s）") {
		t.Errorf("renderToolsProgress = %q, want the pending row's elapsed time", got)
	}

	// The word "pending" is about the tool, not about its measurement: once the
	// backend reports a duration, that is the figure used, not the waiting time.
	done := []larkcard.RichStep{{Kind: larkcard.RichStepKindTool, ID: "c", Name: "Read", Status: "ok", DurationMs: 300}}
	if got := renderToolsProgress(done, started, began.Add(time.Hour)); !strings.Contains(got, "⚡ 300ms") {
		t.Errorf("renderToolsProgress = %q, want the measured duration, not the waiting time", got)
	}

	// A pending step with no recorded start — a backend that reports no call id,
	// so nothing was recorded for it — shows no time rather than an invented one.
	anon := []larkcard.RichStep{{Kind: larkcard.RichStepKindTool, Name: "Read", Status: "pending"}}
	if got := renderToolsProgress(anon, started, began.Add(time.Hour)); strings.Contains(got, "⚡") {
		t.Errorf("renderToolsProgress = %q, want no elapsed time without a start", got)
	}
}

// toolPanelRowContents returns the markdown of each step row in the final card's
// tool-call (工具调用) panel.
//
// The card is unmarshalled rather than searched as text on purpose: Go's
// encoding/json escapes "<" to < in the payload, so the colour tag a row
// carries is never literally present in the bytes Feishu is sent.
func toolPanelRowContents(t *testing.T, cardJSON string) []string {
	t.Helper()

	var doc struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(cardJSON), &doc); err != nil {
		t.Fatalf("final card is not valid JSON: %v", err)
	}

	var rows []string
	for _, el := range doc.Body.Elements {
		if el["tag"] != "collapsible_panel" {
			continue
		}
		title, _ := el["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
		if !strings.HasPrefix(title, "工具调用") {
			continue
		}
		for _, raw := range el["elements"].([]any) {
			row, ok := raw.(map[string]any)
			if !ok || row["tag"] != "markdown" {
				continue
			}
			content, _ := row["content"].(string)
			rows = append(rows, content)
		}
	}
	return rows
}

// TestToolStepLineReadsTheSameInBothLists is the contract this change exists to
// establish: a tool call rendered while the turn runs and the same call rendered
// in the finished card's tool-call (工具调用) panel are one string, byte for byte.
//
// The two lists are built in different packages and reach Feishu by different
// routes — the live one through a cardkit element update, the final one inside the
// card JSON. Nothing but this test ties their formatting together, and pairing them
// here rather than comparing each against its own expectation is the point: the two
// used to disagree about the name, the icon carrier and the separator, and every
// test on both sides passed anyway, because each only ever saw itself.
func TestToolStepLineReadsTheSameInBothLists(t *testing.T) {
	steps := []larkcard.RichStep{
		{Kind: larkcard.RichStepKindTool, ID: "a", Name: "exec", Summary: "ls -la", Status: "ok", DurationMs: 234, Tokens: 820},
		{Kind: larkcard.RichStepKindTool, ID: "b", Name: "read", Status: "fail", DurationMs: 12},
		{Kind: larkcard.RichStepKindTool, ID: "c", Name: "grep", Status: "pending"},
	}

	live := strings.Split(renderToolsProgress(steps, nil, time.Now()), "\n")
	final := toolPanelRowContents(t, larkcard.BuildRichCard(larkcard.RichCardStatusWorking, "", steps, "", false, ""))

	if len(live) != len(steps) || len(final) != len(steps) {
		t.Fatalf("live panel rendered %d rows and the final card %d, want %d each", len(live), len(final), len(steps))
	}
	for i, step := range steps {
		want := larkcard.ToolStepLine(step, step.DurationMs)
		if live[i] != want {
			t.Errorf("live row %d = %q, want %q", i, live[i], want)
		}
		if final[i] != want {
			t.Errorf("final card row %d = %q, want %q", i, final[i], want)
		}
	}
}

// TestComposeStatusFooter pins the line under the final answer. It is the only
// place the turn's cost is reported, and its two terminal markers are what tell
// a user who scrolled back whether the red card was a failure or their own stop.
func TestComposeStatusFooter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		steps   int
		tokens  int
		status  larkcard.RichCardStatus
		stopped bool
		reason  string
		want    []string
		absent  []string
	}{
		{
			name:    "a plain success reports time, steps and cost",
			elapsed: 1500 * time.Millisecond, steps: 3, tokens: 21500, status: larkcard.RichCardStatusDone,
			want: []string{"⏱ 用时 1.5s", "🧩 3 步", "✨ 21.5k tokens"},
		},
		{
			name:    "a failure is named",
			elapsed: time.Second, status: larkcard.RichCardStatusError,
			want: []string{"失败"}, absent: []string{"已停止"},
		},
		{
			name:    "a stop is told apart from a failure",
			elapsed: time.Second, status: larkcard.RichCardStatusError, stopped: true,
			want: []string{"已停止"}, absent: []string{"失败"},
		},
		{
			// The reason is what turns "this ended" into "this is why it ended".
			// A card taken over by a newer message used to be indistinguishable
			// from one the user had stopped.
			name:    "a takeover is named",
			elapsed: time.Second, status: larkcard.RichCardStatusError, stopped: true, reason: reasonTakeover,
			want: []string{"已被新消息接管"}, absent: []string{"已停止", "失败"},
		},
		{
			name:    "a stop names the person who stopped it",
			elapsed: time.Second, status: larkcard.RichCardStatusError, stopped: true, reason: reasonStopped,
			want: []string{"你停止了本次运行"}, absent: []string{"失败"},
		},
		{
			name:    "a cancelled decision is named",
			elapsed: time.Second, status: larkcard.RichCardStatusError, stopped: true, reason: reasonCancelled,
			want: []string{"已取消"}, absent: []string{"失败"},
		},
		{
			// A success says nothing about endings, whatever reason it carries.
			name:    "a reason does not decorate a successful run",
			elapsed: time.Second, status: larkcard.RichCardStatusDone, reason: reasonTakeover,
			absent: []string{"接管", "已停止", "失败"},
		},
		{
			name:    "an instant turn reports no time",
			elapsed: 0, steps: 1, status: larkcard.RichCardStatusDone,
			want: []string{"🧩 1 步"}, absent: []string{"⏱"},
		},
		{
			name:    "a turn that ran no tools reports no step count",
			elapsed: time.Minute, status: larkcard.RichCardStatusDone,
			want: []string{"⏱ 用时 1m0s"}, absent: []string{"🧩"},
		},
		{
			// An upstream that reports no usage must not make the run look free.
			name:    "a run that reported no usage shows no token segment",
			elapsed: time.Second, steps: 2, status: larkcard.RichCardStatusDone,
			absent: []string{"✨", "tokens"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := composeStatusFooter(tc.elapsed, tc.steps, tc.tokens, tc.status, tc.stopped, tc.reason)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("footer = %q, want it to contain %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("footer = %q, must not contain %q", got, absent)
				}
			}
			if strings.HasPrefix(got, " ·") || strings.HasSuffix(got, "· ") {
				t.Errorf("footer = %q, want the parts joined without a dangling separator", got)
			}
		})
	}

	// A turn with nothing to say about itself says nothing, rather than sending a
	// line that reads as "0 steps, no time, fine".
	if got := composeStatusFooter(0, 0, 0, larkcard.RichCardStatusDone, false, ""); got != "" {
		t.Errorf("an empty footer = %q, want nothing", got)
	}
}

// TestToolProgress covers the shared state between the event callback and the
// once-a-second refresh, which are separate goroutines.
func TestToolProgress(t *testing.T) {
	p := newToolProgress()
	began := time.Now()

	if p.hasPending() {
		t.Error("a fresh progress has nothing running")
	}

	p.observe([]agent.ToolStep{{ID: "a", Name: "grep", Status: "pending"}}, began)
	if !p.hasPending() {
		t.Error("hasPending = false with a pending row, want true — the refresh would never start")
	}
	// A second sighting of the same invocation must not restart its clock: the
	// row's elapsed time would jump backwards.
	p.observe([]agent.ToolStep{{ID: "a", Name: "grep", Status: "ok", Duration: 300 * time.Millisecond}},
		began.Add(time.Minute))
	if !strings.Contains(p.render(began.Add(2*time.Second)), "⚡ 300ms") {
		t.Errorf("render = %q, want the measured duration", p.render(began.Add(2*time.Second)))
	}
	if p.hasPending() {
		t.Error("hasPending = true with every row finished, want false")
	}

	// The token total is monotonic: a lower report is stale, not a correction,
	// because usage and tool events can arrive in either order.
	if !p.setTokens(500) {
		t.Error("setTokens(500) = false, want the first report to count")
	}
	if p.setTokens(400) {
		t.Error("setTokens(400) = true, want a lower report to be treated as stale")
	}
	if p.setTokens(600) != true {
		t.Error("setTokens(600) = false, want a higher report to count")
	}
	if steps, tokens := p.counts(); steps != 1 || tokens != 600 {
		t.Errorf("counts = (%d, %d), want (1, 600)", steps, tokens)
	}
	if rows := p.rows(); len(rows) != 1 || rows[0].Name != "grep" {
		t.Errorf("rows = %+v, want the one row", rows)
	}
}

// TestAgentSelection is the guard for the silent-tool case: the LLM server reads
// an empty agent selection as "no MCP tools", not as "the default agent", so a
