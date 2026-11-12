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
	"strings"
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// progressRefreshInterval is how often the tool panel is redrawn while a tool is
// still running. It exists because a running tool's elapsed time changes on its
// own, and the publish throttle — tuned to batch prose — would otherwise never
// consider "234ms" worth updating to "1.2s". One second is the resolution the
// panel shows, and the ticker only fires while something is actually pending,
// so a card sees at most one extra update per second per running tool.
const progressRefreshInterval = time.Second

// toolProgress is one turn's tool panel: the rows, the run's token total, and
// when each invocation was first seen.
//
// It is a guarded value rather than a handful of locals on the turn because two
// goroutines touch it — the event callback, which appends as the backend
// reports, and the refresh ticker, which redraws once a second so a running
// tool's elapsed time keeps moving.
type toolProgress struct {
	mu      sync.Mutex
	steps   []larkcard.RichStep
	started map[string]time.Time
	tokens  int
	// waiting records that the run is parked on a person, so the rows still say
	// "pending" are drawn as waiting rather than as running.
	waiting bool
}

func newToolProgress() *toolProgress {
	return &toolProgress{started: map[string]time.Time{}}
}

// observe folds a reported batch of tool steps in. An invocation already in the
// list is updated in place, so the pending row and the finished row are one row.
func (p *toolProgress) observe(reported []agent.ToolStep, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = appendSteps(p.steps, reported)
	noteStepStarts(p.started, reported, now)
}

// setTokens records the run's reported token total and reports whether it was
// an increase. The total is kept monotonic because usage and tool events are
// produced by different goroutines on the backend side and can arrive in either
// order: a lower figure is a stale report, not a correction.
func (p *toolProgress) setTokens(total int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if total <= p.tokens {
		return false
	}
	p.tokens = total
	return true
}

// render draws the panel as of now. A pending row's elapsed time is measured
// from when it was first seen, since the backend reports a duration only once
// the invocation has ended.
func (p *toolProgress) render(now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	steps := p.steps
	if p.waiting {
		// A copy, because "waiting" is how the row is drawn and not what the row
		// is: the port reports "pending", and a resume has to be able to put it
		// back. Only the rows the backend has not finished are affected — a tool
		// that already ran is not waiting on anyone.
		steps = make([]larkcard.RichStep, len(p.steps))
		copy(steps, p.steps)
		for i := range steps {
			if strings.TrimSpace(steps[i].Status) == larkcard.RichStepStatusPending {
				steps[i].Status = larkcard.RichStepStatusWaiting
			}
		}
	}
	return renderToolsProgress(steps, p.started, now)
}

// counts returns the step count and the run's token total for the final footer.
func (p *toolProgress) counts() (steps, tokens int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.steps), p.tokens
}

// rows returns the tool steps for the final rich card. The slice is copied: the
// caller reads it after the lock is released, and the whole point of the type is
// that another goroutine may be folding more steps in.
func (p *toolProgress) rows() []larkcard.RichStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]larkcard.RichStep, len(p.steps))
	copy(out, p.steps)
	return out
}

// hasPending reports whether any invocation is still running, which is the only
// time the panel's elapsed figures change without an event.
func (p *toolProgress) hasPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting {
		// Waiting on a person is not running: the panel's figures do not change on
		// their own, so the ticker that redraws them has nothing to do.
		return false
	}
	for _, s := range p.steps {
		if s.Status == larkcard.RichStepStatusPending {
			return true
		}
	}
	return false
}

// markWaiting reports that the run has parked on a decision. The pending rows
// are drawn as waiting from here on: they are not running, and what they are
// waiting for is a person.
func (p *toolProgress) markWaiting() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting = true
}

// abandonWaiting closes the waiting rows for good, on the paths where the pause
// will never be answered — a message taking the conversation over, a stop, the
// wait running out. The tool did not run and will not, so the rows say that
// rather than leaving a frozen "等待人工确认" (awaiting confirmation) on a card that is being finalized.
//
// Pending as well as waiting, because "waiting" is how a row is drawn and not
// what it holds: the backend reports "pending" and render is what shows it as a
// question. Reading only the drawn status would find nothing to close and leave
// the finalized card showing a running tool that never ran.
func (p *toolProgress) abandonWaiting() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.waiting {
		return
	}
	p.waiting = false
	for i := range p.steps {
		switch strings.TrimSpace(p.steps[i].Status) {
		case larkcard.RichStepStatusPending, larkcard.RichStepStatusWaiting:
			p.steps[i].Status = larkcard.RichStepStatusSkipped
		}
	}
}

// resetPendingStarted re-bases the pending rows' clocks on now. A pending row
// measures how long the client has been waiting on it, and a tool whose call
// paused for approval has been waiting on a person for part of that — time the
// tool did not spend and must not be credited with. It is called when a paused
// turn resumes, so the figure that appears once the tool runs is its own.
func (p *toolProgress) resetPendingStarted(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// The run is moving again, so the rows are running again: a row left marked as
	// waiting would keep saying "等待人工确认" (awaiting confirmation) while the tool it names is working.
	p.waiting = false
	for _, s := range p.steps {
		// A row with no id measures nothing — render only times the identified
		// rows — so there is no clock to re-base for it.
		if s.Status == larkcard.RichStepStatusPending && s.ID != "" {
			p.started[s.ID] = now
		}
	}
}
