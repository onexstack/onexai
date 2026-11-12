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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, so interval thresholds can be tested
// without waiting for real time to pass.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// sinkUpdate is one recorded publish.
type sinkUpdate struct {
	lane string
	seq  int64
	text string
}

// recordingSink records what it was asked to render, and can be told to fail.
type recordingSink struct {
	mu      sync.Mutex
	updates []sinkUpdate
	// failFrom is the 1-based call number at which every call starts failing.
	// Zero means never.
	failFrom int
	calls    int
}

var errSinkUnavailable = errors.New("sink unavailable")

func (s *recordingSink) record(lane string, seq *Sequence, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	// The number is taken before the request is made, as a real sink must: a
	// call that reports failure may still have been applied by the platform, so
	// its number can never be handed out again.
	n := seq.Next()
	if s.failFrom > 0 && s.calls >= s.failFrom {
		return errSinkUnavailable
	}
	s.updates = append(s.updates, sinkUpdate{lane: lane, seq: n, text: text})
	return nil
}

func (s *recordingSink) UpdateText(_ context.Context, seq *Sequence, text string) error {
	return s.record("answer", seq, text)
}

func (s *recordingSink) UpdateReasoning(_ context.Context, seq *Sequence, text string) error {
	return s.record("reasoning", seq, text)
}

func (s *recordingSink) UpdateProgress(_ context.Context, seq *Sequence, text string) error {
	return s.record("progress", seq, text)
}

// snapshot returns a copy of what has been rendered so far.
func (s *recordingSink) snapshot() []sinkUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkUpdate(nil), s.updates...)
}

// last returns the most recent update for a lane, and whether there was one.
func (s *recordingSink) last(lane string) (sinkUpdate, bool) {
	var (
		found sinkUpdate
		ok    bool
	)
	for _, u := range s.snapshot() {
		if u.lane == lane {
			found, ok = u, true
		}
	}
	return found, ok
}

// ---------------------------------------------------------------- Sequence

// TestSequenceMonotonicUnderConcurrency is the reason Sequence exists: with a
// hand-incremented counter shared by concurrent writers, duplicates are routine.
// Run with -race to catch the unsynchronised version too.
func TestSequenceMonotonicUnderConcurrency(t *testing.T) {
	seq := &Sequence{}

	const writers, each = 100, 20
	seen := make(chan int64, writers*each)

	var wg sync.WaitGroup
	wg.Add(writers)
	for range writers {
		go func() {
			defer wg.Done()
			for range each {
				seen <- seq.Next()
			}
		}()
	}
	wg.Wait()
	close(seen)

	got := map[int64]bool{}
	for n := range seen {
		if n < 1 {
			t.Fatalf("Next() returned %d; numbers must start at 1", n)
		}
		if got[n] {
			t.Fatalf("Next() returned %d twice; a repeated sequence is rejected by the platform, silently", n)
		}
		got[n] = true
	}
	if len(got) != writers*each {
		t.Errorf("got %d distinct numbers, want %d", len(got), writers*each)
	}
	if want := int64(writers * each); seq.Current() != want {
		t.Errorf("Current() = %d, want %d", seq.Current(), want)
	}
}

func TestSequenceCurrentDoesNotAdvance(t *testing.T) {
	seq := &Sequence{}
	if got := seq.Current(); got != 0 {
		t.Errorf("Current() = %d before any Next(), want 0", got)
	}
	first := seq.Next()
	if got := seq.Current(); got != first {
		t.Errorf("Current() = %d after Next() returned %d, want the same", got, first)
	}
}

// TestSequenceNotReusedAfterFailedPublish covers the case the counter must
// survive: an update that failed may have been applied anyway, so the number it
// consumed must never be handed out again. Retrying with a fresh Preview over
// the same sequence is exactly how that happens in practice.
func TestSequenceNotReusedAfterFailedPublish(t *testing.T) {
	ctx := context.Background()
	seq := &Sequence{}
	sink := &recordingSink{failFrom: 1}

	// The first attempt fails.
	New(sink, seq, Config{}).UpdateText(ctx, "第一段")
	if got := seq.Current(); got != 1 {
		t.Fatalf("a failed publish left the counter at %d, want 1 — it consumed a number", got)
	}

	// A retry must not reuse it.
	sink.failFrom = 0
	New(sink, seq, Config{}).UpdateText(ctx, "答案")

	updates := sink.snapshot()
	if len(updates) != 1 {
		t.Fatalf("recorded %d successful publishes, want 1", len(updates))
	}
	if updates[0].seq != 2 {
		t.Errorf("the retry published sequence %d, want 2; reusing 1 would be rejected as non-increasing",
			updates[0].seq)
	}
}

// ---------------------------------------------------------------- throttling

// TestDualThresholdFlushesOnInterval pins that the interval alone does not
// decide: with enough pending content, the publish waits for the interval.
func TestDualThresholdFlushesOnInterval(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 1500 * time.Millisecond, MinDeltaChars: 30, Now: clock.Now})

	clock.Advance(time.Second)
	p.UpdateText(ctx, strings.Repeat("字", 40))
	if _, ok := sink.last("answer"); ok {
		t.Fatal("published a full second before the interval elapsed")
	}

	clock.Advance(600 * time.Millisecond)
	p.UpdateText(ctx, "尾")
	last, ok := sink.last("answer")
	if !ok {
		t.Fatal("published nothing after the interval elapsed with 40 pending characters")
	}
	if want := strings.Repeat("字", 40) + "尾"; last.text != want {
		t.Errorf("published %q, want the full lane content %q", last.text, want)
	}
}

// TestDualThresholdFlushesOnMinDelta is the other half: with the interval long
// past, the publish waits for enough content.
func TestDualThresholdFlushesOnMinDelta(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 1500 * time.Millisecond, MinDeltaChars: 30, Now: clock.Now})

	clock.Advance(time.Hour)
	p.UpdateText(ctx, strings.Repeat("字", 29))
	if _, ok := sink.last("answer"); ok {
		t.Fatal("published 29 characters when the threshold is 30")
	}

	p.UpdateText(ctx, "字")
	last, ok := sink.last("answer")
	if !ok {
		t.Fatal("published nothing after the 30th character")
	}
	if len([]rune(last.text)) != 30 {
		t.Errorf("published %q, want 30 characters", last.text)
	}
}

// TestThrottleDisabledPublishesEveryDelta covers the configuration where the
// transport rate-limits on its own: interval 0 means no interval threshold at
// all, rather than "publish at most once per epoch".
func TestThrottleDisabledPublishesEveryDelta(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 0, MinDeltaChars: 1})

	p.UpdateText(ctx, "一")
	p.UpdateText(ctx, "二")

	updates := sink.snapshot()
	if len(updates) != 2 {
		t.Fatalf("recorded %d publishes, want 2 (one per delta)", len(updates))
	}
	if updates[0].text != "一" || updates[1].text != "一二" {
		t.Errorf("published %q then %q, want cumulative lane contents", updates[0].text, updates[1].text)
	}
}

// TestLanesThrottleIndependently pins that a burst of reasoning does not publish
// a half-written answer: the thresholds are evaluated over what is pending, and
// each lane carries only its own content.
func TestLanesThrottleIndependently(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: time.Second, MinDeltaChars: 10, Now: clock.Now})

	clock.Advance(2 * time.Second)
	p.UpdateText(ctx, "答")
	if _, ok := sink.last("answer"); ok {
		t.Error("published a 1-character answer with a 10-character threshold")
	}

	clock.Advance(2 * time.Second)
	p.UpdateReasoning(ctx, "这是一段足够长的推理内容")
	if _, ok := sink.last("reasoning"); !ok {
		t.Fatal("published nothing when the reasoning lane crossed the threshold")
	}
	if _, ok := sink.last("answer"); ok {
		t.Error("the reasoning flush also published the pending answer")
	}
}

// TestSetProgressShrinkingStillPublishes covers the progress lane, which is
// re-rendered wholesale rather than appended to: a shorter render is a change
// worth publishing, not a no-op.
func TestSetProgressShrinkingStillPublishes(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: time.Second, MinDeltaChars: 5, Now: clock.Now})

	p.SetProgress(ctx, "✅ 读取文件\n⏳ 执行命令")
	clock.Advance(2 * time.Second)
	p.SetProgress(ctx, "✅ 读取文件")

	last, ok := sink.last("progress")
	if !ok {
		t.Fatal("a shorter progress rendering was not published")
	}
	if last.text != "✅ 读取文件" {
		t.Errorf("published %q, want the replacement content", last.text)
	}
}

// TestSequenceIncreasesAcrossLanes pins that the shared counter orders updates
// from different lanes, which is what stops the platform from silently dropping
// one of them.
func TestSequenceIncreasesAcrossLanes(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{})

	p.UpdateText(ctx, "答案")
	p.UpdateReasoning(ctx, "推理")
	p.SetProgress(ctx, "步骤")

	updates := sink.snapshot()
	if len(updates) != 3 {
		t.Fatalf("recorded %d publishes, want 3", len(updates))
	}
	for i := 1; i < len(updates); i++ {
		if updates[i].seq <= updates[i-1].seq {
			t.Fatalf("publish %d used sequence %d after %d; sequences must strictly increase",
				i, updates[i].seq, updates[i-1].seq)
		}
	}
}

// ---------------------------------------------------------------- failure

// TestDegradeStopsPublishingAndKeepsText is the failure policy: a rendering
// failure must not be able to destroy the content it was rendering. The preview
// stops talking to the sink, and the text stays available for the caller to
// write in one piece.
func TestDegradeStopsPublishingAndKeepsText(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{failFrom: 1}
	p := New(sink, &Sequence{}, Config{})

	p.UpdateText(ctx, "第一段")
	if !p.Degraded() {
		t.Fatal("Degraded() = false after the sink failed")
	}
	if !errors.Is(p.Err(), errSinkUnavailable) {
		t.Errorf("Err() = %v, want the sink's error", p.Err())
	}

	// Further updates must not keep hammering a sink that is known to be down.
	p.UpdateText(ctx, "第二段")
	p.UpdateReasoning(ctx, "推理")
	p.Flush(ctx)
	if n := len(sink.snapshot()); n != 0 {
		t.Errorf("the sink was called %d more times after failing; publishing must stop", n)
	}

	if got := p.Text(); got != "第一段第二段" {
		t.Errorf("Text() = %q, want the full answer %q — the caller still needs it", got, "第一段第二段")
	}
	if got := p.Reasoning(); got != "推理" {
		t.Errorf("Reasoning() = %q, want %q", got, "推理")
	}
}

// TestFinishAfterDegradePublishesNothing covers the end of a degraded turn: the
// final flush must not resurrect a dead sink. The caller writes the full answer
// by its own means.
func TestFinishAfterDegradePublishesNothing(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{failFrom: 1}
	p := New(sink, &Sequence{}, Config{})

	p.UpdateText(ctx, "内容")
	p.Finish(ctx)

	if n := len(sink.snapshot()); n != 0 {
		t.Errorf("Finish called the failed sink %d times, want 0", n)
	}
	if got := p.Text(); got != "内容" {
		t.Errorf("Text() = %q after Finish, want %q", got, "内容")
	}
}

// TestFinishFlushesPendingContent covers the ordinary ending: whatever the
// throttle was still holding back is published before the card is closed.
func TestFinishFlushesPendingContent(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: time.Hour, MinDeltaChars: 1000, Now: clock.Now})

	p.UpdateText(ctx, "几乎没人看得到的答案")
	if _, ok := sink.last("answer"); ok {
		t.Fatal("published before Finish despite both thresholds blocking")
	}

	p.Finish(ctx)
	last, ok := sink.last("answer")
	if !ok {
		t.Fatal("Finish published nothing; the pending answer would be lost to anyone reading the card")
	}
	if last.text != "几乎没人看得到的答案" {
		t.Errorf("Finish published %q, want the full answer", last.text)
	}
}

// TestFreezeSuppressesFurtherFlush is the guard against a late delta: once the
// card has been finalized, another update would bump the sequence of a closed
// card, which the platform rejects.
func TestFreezeSuppressesFurtherFlush(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{})

	p.UpdateText(ctx, "答案")
	p.Finish(ctx)
	before := len(sink.snapshot())

	p.UpdateText(ctx, "迟到的增量")
	p.UpdateReasoning(ctx, "迟到的推理")
	p.SetProgress(ctx, "迟到的步骤")
	p.Flush(ctx)

	if got := len(sink.snapshot()); got != before {
		t.Errorf("the sink was called %d times after Finish, want none", got-before)
	}
	// Frozen also means frozen in memory: the caller has already read the lane
	// contents to build the final card, so mutating them afterwards would make
	// what it rendered disagree with what it can still read back.
	if got := p.Text(); got != "答案" {
		t.Errorf("Text() = %q after Finish, want %q", got, "答案")
	}
}

// TestMaxCharsBoundsPreviewNotTheLane pins the split: the sink never sees more
// than MaxChars, and the lane keeps everything. A long answer therefore arrives
// whole in the caller's terminal render even though the live panel stopped
// growing.
func TestMaxCharsBoundsPreviewNotTheLane(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{MaxChars: 5})

	p.UpdateText(ctx, "一二三四五六七八")
	last, ok := sink.last("answer")
	if !ok {
		t.Fatal("published nothing")
	}
	if want := "一二三四五"; last.text != want {
		t.Errorf("published %q, want it cut to %q", last.text, want)
	}

	// Content past the bound is not a reason to call the sink again: the text it
	// would receive has not changed.
	p.UpdateText(ctx, "九十")
	if n := len(sink.snapshot()); n != 1 {
		t.Errorf("the sink was called %d times, want 1 — nothing new is renderable", n)
	}
	if want := "一二三四五六七八九十"; p.Text() != want {
		t.Errorf("Text() = %q, want the full answer %q", p.Text(), want)
	}
}

// TestSameLengthReplacementStillPublishes pins what a rune count cannot see: a
// progress panel whose content changes without changing length — a step flipping
// from pending to done — is exactly the update the reader is waiting for.
func TestSameLengthReplacementStillPublishes(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{})

	p.SetProgress(ctx, "⏳ 执行命令")
	p.SetProgress(ctx, "✅ 执行命令")

	if n := len(sink.snapshot()); n != 2 {
		t.Fatalf("the sink was called %d times, want 2 — the second render differs only in content", n)
	}
	if last, _ := sink.last("progress"); last.text != "✅ 执行命令" {
		t.Errorf("published %q last, want the replacement", last.text)
	}
}

// TestNilSinkAccumulatesWithoutPublishing covers incremental publishing being
// turned off: the turn still has to end with the whole answer.
func TestNilSinkAccumulatesWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	p := New(nil, &Sequence{}, Config{})

	p.UpdateText(ctx, "答")
	p.UpdateReasoning(ctx, "推理")
	p.SetProgress(ctx, "步骤")
	p.Flush(ctx)
	p.Finish(ctx)

	if p.Text() != "答" || p.Reasoning() != "推理" || p.Progress() != "步骤" {
		t.Errorf("Text/Reasoning/Progress = %q/%q/%q, want the accumulated content",
			p.Text(), p.Reasoning(), p.Progress())
	}
	if p.Degraded() {
		t.Error("a nil sink is a configuration, not a failure; Degraded() must stay false")
	}
}

func TestPreviewPublishesNothingWhenNoLaneChanged(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 0, MinDeltaChars: 1, Now: clock.Now})

	p.Flush(ctx)
	p.Finish(ctx)

	if n := len(sink.snapshot()); n != 0 {
		t.Errorf("the sink was called %d times with no lane content, want 0", n)
	}
}

// TestRefreshProgressBypassesTheThrottle is the reason the method exists: a
// running tool's elapsed time changes without anything being appended, and the
// change is a character or two — exactly what both thresholds are built to wait
// through. Without the bypass the row would stay at its first figure until the
// tool ended.
func TestRefreshProgressBypassesTheThrottle(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 1500 * time.Millisecond, MinDeltaChars: 30, Now: clock.Now})

	clock.Advance(2 * time.Second)
	p.SetProgress(ctx, "⏳ Read  (⚡ 234ms)")
	first, ok := sink.last("progress")
	if !ok {
		t.Fatal("the first progress render was throttled away")
	}

	clock.Advance(100 * time.Millisecond)
	p.RefreshProgress(ctx, "⏳ Read  (⚡ 334ms)")

	last, ok := sink.last("progress")
	if !ok || last.seq == first.seq {
		t.Fatal("RefreshProgress published nothing 100ms in, want it to ignore the interval")
	}
	if last.text != "⏳ Read  (⚡ 334ms)" {
		t.Errorf("published %q, want the refreshed panel", last.text)
	}
	// Two characters of change, well under the delta threshold, and it still went.
	if got := len([]rune(last.text)) - len([]rune(first.text)); got >= 30 {
		t.Fatalf("the fixtures no longer exercise a sub-threshold change (%d characters)", got)
	}
	if p.Progress() != last.text {
		t.Errorf("Progress() = %q, want the refreshed lane content", p.Progress())
	}
}

// TestRefreshProgressLeavesOtherLanesAlone pins that the bypass is scoped: an
// answer that is still accumulating does not get flushed early just because a
// tool's clock ticked, which would spend a round trip on the throttle's behalf
// and publish a half-written answer.
func TestRefreshProgressLeavesOtherLanesAlone(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 1500 * time.Millisecond, MinDeltaChars: 30, Now: clock.Now})

	clock.Advance(time.Second)
	p.UpdateText(ctx, "半句话")
	p.SetProgress(ctx, "⏳ Read")
	for _, u := range sink.snapshot() {
		if u.lane == "answer" {
			t.Fatal("the answer lane published before the interval elapsed")
		}
	}

	p.RefreshProgress(ctx, "⏳ Read  (⚡ 1.0s)")
	for _, u := range sink.snapshot() {
		if u.lane == "answer" {
			t.Fatal("RefreshProgress flushed the answer lane as well")
		}
	}
	if last, ok := sink.last("progress"); !ok || last.text != "⏳ Read  (⚡ 1.0s)" {
		t.Errorf("progress = %+v, want the refreshed panel", last)
	}
}

// TestRefreshProgressAfterFinishPublishesNothing keeps a late tick from bumping
// the sequence of a card that has already been closed.
func TestRefreshProgressAfterFinishPublishesNothing(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	sink := &recordingSink{}
	p := New(sink, &Sequence{}, Config{Interval: 0, MinDeltaChars: 1, Now: clock.Now})

	p.SetProgress(ctx, "⏳ Read")
	p.Finish(ctx)
	before := len(sink.snapshot())

	p.RefreshProgress(ctx, "⏳ Read  (⚡ 9.9s)")

	if after := len(sink.snapshot()); after != before {
		t.Errorf("the sink was called %d more times after Finish, want 0", after-before)
	}
}
