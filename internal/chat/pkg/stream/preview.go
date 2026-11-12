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

package stream

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Config tunes the publish throttle.
type Config struct {
	// Interval is the minimum time between two publishes of a lane. Zero or
	// negative publishes as often as deltas arrive, which suits a transport that
	// already rate-limits on its own.
	Interval time.Duration
	// MinDeltaChars is how much unpublished content makes a publish worthwhile.
	MinDeltaChars int
	// MaxChars bounds what a lane publishes. Beyond it the preview stops
	// growing: a card element has a size the platform will start rejecting, and a
	// live panel that quietly stops updating is a worse failure than one that
	// stops at a known point. The bound is on the preview only — the caller's
	// terminal render reads the full lane, so a long answer still arrives whole.
	// Zero means unbounded.
	MaxChars int
	// Now is the clock, injectable for tests. Nil means time.Now.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.MinDeltaChars < 1 {
		c.MinDeltaChars = 1
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Preview throttles and publishes the incremental output of one turn.
//
// It exists because publishing every delta is both wasteful and visibly wrong:
// a card update is a network round trip, and one per token produces text that
// reflows and flickers on the reader's screen. So a publish needs two things to
// be true at once — enough time has passed, and enough new content has
// accumulated. Either alone is wrong: interval alone publishes a single word
// after a long pause, delta alone bursts.
//
// It also owns the failure policy, which is the part worth stating plainly:
// rendering must never destroy the content it is rendering. A failed publish
// marks the preview degraded, stops further publishes, and the turn carries on —
// the caller still holds the complete text, and writes it in one piece at the
// end. Before this, a transient card-update failure propagated out of the stream
// callback and turned the user's answer into a red card.
type Preview struct {
	sink Sink
	seq  *Sequence
	cfg  Config

	mu        sync.Mutex
	lanes     map[laneID]*lane
	lastFlush time.Time
	degraded  bool
	frozen    bool
	err       error
}

// New creates a Preview publishing to sink. seq is shared with whoever else
// writes to the same target — typically only this Preview — and must not be
// advanced by hand.
func New(sink Sink, seq *Sequence, cfg Config) *Preview {
	if seq == nil {
		seq = &Sequence{}
	}
	p := &Preview{
		sink: sink,
		seq:  seq,
		cfg:  cfg.withDefaults(),
		lanes: map[laneID]*lane{
			laneAnswer:    {grows: true},
			laneReasoning: {grows: true},
			laneProgress:  {grows: false},
		},
	}
	p.lastFlush = p.cfg.Now()
	return p
}

// UpdateText appends delta to the answer lane and publishes it if the throttle
// allows.
func (p *Preview) UpdateText(ctx context.Context, delta string) {
	p.withLane(ctx, laneAnswer, delta, false)
}

// UpdateReasoning appends delta to the reasoning lane and publishes it if the
// throttle allows.
func (p *Preview) UpdateReasoning(ctx context.Context, delta string) {
	p.withLane(ctx, laneReasoning, delta, false)
}

// SetProgress replaces the progress lane and publishes it if the throttle
// allows. Progress is re-rendered wholesale from the running step list rather
// than appended to, so it is set, not appended.
func (p *Preview) SetProgress(ctx context.Context, text string) {
	p.withLane(ctx, laneProgress, text, true)
}

// RefreshProgress replaces the progress lane and publishes it regardless of the
// throttle.
//
// It exists for figures that change on their own rather than because anything
// was appended — the elapsed time of a tool that is still running. Those are
// exactly what the throttle is blind to: a row moving from "234ms" to "1.2s"
// differs by two characters, so a policy tuned to publish prose in useful
// chunks would leave it frozen at its first value until the tool ended, which
// is the opposite of what a live panel is for. The caller is the rate limit
// here, so it must not call this in a tight loop.
//
// Only the progress lane is published: the answer and reasoning lanes keep
// their own cadence, which is what keeps a streaming answer readable.
func (p *Preview) RefreshProgress(ctx context.Context, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozen {
		return
	}
	p.lanes[laneProgress].set(text)
	p.publishLanes(ctx, true, progressLanes)
}

func (p *Preview) withLane(ctx context.Context, id laneID, content string, replace bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Frozen means the turn is over and its card has been rendered: content
	// arriving now belongs to a card that no longer exists, and mutating state
	// the caller has already read would be worse than discarding it.
	if p.frozen {
		return
	}
	l := p.lanes[id]
	if replace {
		l.set(content)
	} else {
		l.append(content)
	}
	p.publish(ctx, false)
}

// Flush publishes every lane behind the sink, ignoring the throttle. It is for
// the moments when waiting would be wrong — a run ending, a step appearing —
// and does nothing once the preview is finished.
func (p *Preview) Flush(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publish(ctx, true)
}

// Finish publishes whatever is left and freezes the preview: no further update
// reaches the sink, so a late delta cannot bump the sequence of a card that has
// already been closed. The lane contents stay readable, because the caller's
// terminal rendering is built from them.
func (p *Preview) Finish(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publish(ctx, true)
	p.frozen = true
}

// Text returns the full answer accumulated so far.
func (p *Preview) Text() string { return p.laneText(laneAnswer) }

// Reasoning returns the full reasoning accumulated so far.
func (p *Preview) Reasoning() string { return p.laneText(laneReasoning) }

// Progress returns the current progress rendering.
func (p *Preview) Progress() string { return p.laneText(laneProgress) }

func (p *Preview) laneText(id laneID) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lanes[id].text()
}

// Degraded reports whether a publish failed. A degraded preview has stopped
// updating the sink, and its caller is responsible for writing the complete
// answer by whatever means remain.
func (p *Preview) Degraded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.degraded
}

// Err returns the first publish failure, or nil.
func (p *Preview) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Sequence returns the shared counter, for the caller's own terminal update.
func (p *Preview) Sequence() *Sequence { return p.seq }

// progressLanes is the single lane RefreshProgress publishes.
var progressLanes = []laneID{laneProgress}

// publish pushes every lane behind the sink, if force or the throttle allows.
// The caller must hold p.mu.
func (p *Preview) publish(ctx context.Context, force bool) {
	p.publishLanes(ctx, force, laneOrder)
}

// publishLanes pushes the given lanes behind the sink, if force or the throttle
// allows. The caller must hold p.mu.
func (p *Preview) publishLanes(ctx context.Context, force bool, ids []laneID) {
	// A nil sink is a configuration, not a failure: when incremental publishing
	// is off, the preview still accumulates the text for the caller's terminal
	// render and simply never talks to anyone.
	if p.frozen || p.degraded || p.sink == nil {
		return
	}

	published := false
	for _, id := range ids {
		l := p.lanes[id]
		text := p.outbound(l)
		if !p.shouldPublish(l, text, force) {
			continue
		}
		if err := p.push(ctx, id, text); err != nil {
			p.degrade(err)
			return
		}
		l.markSent(text)
		published = true
	}
	// One clock for all lanes, so a single busy lane cannot push the publish
	// rate above the configured one by making the other lanes eligible sooner.
	// Lanes that became eligible in this same pass were considered before the
	// clock moved.
	if published {
		p.lastFlush = p.cfg.Now()
	}
}

// outbound returns what the sink would be shown for this lane right now, which
// MaxChars may have cut short. The lane keeps the whole text regardless.
func (p *Preview) outbound(l *lane) string {
	return truncateRunes(l.text(), p.cfg.MaxChars)
}

// push sends one lane to the sink.
func (p *Preview) push(ctx context.Context, id laneID, text string) error {
	switch id {
	case laneAnswer:
		return p.sink.UpdateText(ctx, p.seq, text)
	case laneReasoning:
		return p.sink.UpdateReasoning(ctx, p.seq, text)
	case laneProgress:
		return p.sink.UpdateProgress(ctx, p.seq, text)
	default:
		return nil
	}
}

// degrade records the first publish failure and stops publishing.
func (p *Preview) degrade(err error) {
	p.degraded = true
	if p.err == nil {
		p.err = err
	}
	slog.Warn("streaming preview degraded; the turn continues and the final render will carry the full text",
		"error", err, "sequence", p.seq.Current())
}

// shouldPublish reports whether this lane may go to the sink now: the sink has
// to be behind, enough time has to have passed since the last publish, and the
// lane has to have accumulated enough content to be worth a round trip.
//
// The delta threshold is per lane, because each lane is a separate API call:
// aggregating them would mean a burst of reasoning publishes a one-character
// answer fragment alongside it, spending a round trip on content the threshold
// just said was not worth one.
//
// A lane waiting on the delta threshold is only re-examined when the next delta
// arrives. That is the intended trade: if generation has paused, the reader has
// nothing further to read anyway, and whatever is left is published by Finish.
func (p *Preview) shouldPublish(l *lane, text string, force bool) bool {
	if !l.stale(text) {
		return false
	}
	if force {
		return true
	}
	if p.cfg.Interval > 0 && p.cfg.Now().Sub(p.lastFlush) < p.cfg.Interval {
		return false
	}
	// A replaced lane has nothing to accumulate: it changed, or it did not. The
	// interval above is what keeps a fast-moving step list from flooding the card.
	if !l.grows {
		return true
	}
	return l.pending(text) >= p.cfg.MinDeltaChars
}
