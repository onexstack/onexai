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

// Package usage accumulates the token usage an agent run's model calls report.
//
// Every number here comes from the upstream's own usage field, never from an
// estimate. An OpenAI-compatible API reports usage per model call and has no
// per-tool granularity, which is why a tool's token count is derived from the
// calls around it rather than measured — see ContextDelta.
package usage

import (
	"context"
	"sync"
)

// Call is the usage one model call reported.
type Call struct {
	// PromptTokens is the call's input token count.
	PromptTokens int
	// CompletionTokens is the call's output token count.
	CompletionTokens int
	// TotalTokens is the call's total, as the upstream reported it.
	TotalTokens int
	// ReasoningTokens is the reasoning share of the output, when reported.
	ReasoningTokens int
	// CacheHitTokens is the share of PromptTokens the upstream served out of its
	// own prefix cache, and CacheMissTokens the share it had to compute.
	//
	// They are a provider extension (`prompt_cache_hit_tokens` /
	// `prompt_cache_miss_tokens`), so both stay zero for an upstream that does
	// not report them — every provider but DeepSeek, today. Zero therefore means
	// "not reported", not "nothing was cached", and a reader must not divide by
	// the sum to get a hit rate without first checking that the sum is non-zero
	// and equal to PromptTokens.
	//
	// Why they are worth carrying at all: the two shares are billed at very
	// different rates — DeepSeek's cached input is 0.02 CNY/M against 1 CNY/M
	// uncached — so PromptTokens alone cannot say what a run cost, and the
	// difference is entirely a function of whether the request prefix stayed
	// stable. Recording the split is what makes that property measurable.
	CacheHitTokens  int
	CacheMissTokens int
	// CacheWriteTokens is the share of PromptTokens the upstream wrote into its
	// prefix cache for later calls. Providers bill it as its own line — cheaper
	// than an uncached prompt, dearer than a cache hit — so read hits and misses
	// alone do not describe a run's cost. Zero means it was not reported.
	CacheWriteTokens int
}

// Totals is the accumulated usage of every model call a run has made.
//
// PromptTokens is summed per call, so a tool loop that feeds the same context
// back counts it once per round. The total is therefore the run's true
// consumption, not the size of its final context.
type Totals struct {
	// Calls is how many model calls the totals cover.
	Calls int
	// PromptTokens is the accumulated input token count.
	PromptTokens int
	// CompletionTokens is the accumulated output token count.
	CompletionTokens int
	// TotalTokens is the accumulated total, prompt plus completion.
	TotalTokens int
	// ReasoningTokens is the accumulated reasoning token count.
	ReasoningTokens int
	// CacheHitTokens and CacheMissTokens are the accumulated cached and uncached
	// input shares. Their sum is PromptTokens wherever the upstream reports both;
	// see Call for why zero is a valid value rather than a defect.
	CacheHitTokens  int
	CacheMissTokens int
	// CacheWriteTokens is the accumulated share written into the upstream's
	// prefix cache. See Call.CacheWriteTokens.
	CacheWriteTokens int
}

// Tracker accumulates one run's model usage. It is safe for concurrent use: the
// model records from the goroutine driving the completion while the run loop
// reads snapshots to emit usage events.
type Tracker struct {
	mu    sync.Mutex
	calls []Call
}

// New returns an empty tracker.
func New() *Tracker { return &Tracker{} }

// Record appends one model call's usage. A nil tracker is a no-op so that a
// caller holding an optional tracker needs no guard of its own.
func (t *Tracker) Record(c Call) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, c)
}

// Totals returns the accumulated usage so far.
func (t *Tracker) Totals() Totals {
	if t == nil {
		return Totals{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	out := Totals{Calls: len(t.calls)}
	for _, c := range t.calls {
		out.PromptTokens += c.PromptTokens
		out.CompletionTokens += c.CompletionTokens
		out.TotalTokens += c.TotalTokens
		out.ReasoningTokens += c.ReasoningTokens
		out.CacheHitTokens += c.CacheHitTokens
		out.CacheMissTokens += c.CacheMissTokens
		out.CacheWriteTokens += c.CacheWriteTokens
	}
	return out
}

// Calls returns a snapshot of the per-call usage, oldest first. The run loop
// needs it to attribute context growth to the tool batch that ran between two
// calls; the result is a copy, so a caller may hold it while more calls record.
func (t *Tracker) Calls() []Call {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.calls) == 0 {
		return nil
	}
	out := make([]Call, len(t.calls))
	copy(out, t.calls)
	return out
}

// ContextDelta returns how many prompt tokens the tool batch that ran before
// call i added to the context. It returns 0 when the derivation yields nothing
// usable, so a caller can treat 0 as "do not display a number".
//
// The value is derived, not measured: an OpenAI-compatible API reports usage per
// model call, so a tool result's own cost is only visible as the growth of the
// next call's prompt. Subtracting the previous call's completion tokens removes
// the contribution of the assistant message itself — which carries the tool-call
// arguments and is fed back as input — leaving the tool's result as the
// remainder.
//
// Two consequences the caller has to live with:
//   - tools invoked in the same parallel batch share one delta, because a single
//     following call covers them all;
//   - a chat template adds per-message overhead (role markers, separators), so
//     the number is approximate. A non-positive result means the derivation
//     told us nothing, and 0 is returned rather than a negative count.
func ContextDelta(calls []Call, i int) int {
	if i < 1 || i >= len(calls) {
		return 0
	}
	prev, cur := calls[i-1], calls[i]
	delta := cur.PromptTokens - prev.PromptTokens - prev.CompletionTokens
	if delta <= 0 {
		return 0
	}
	return delta
}

// trackerKey is the context key for a run's tracker. It is an unexported empty
// struct so that no other package can collide with it.
type trackerKey struct{}

// WithTracker attaches a tracker to ctx. The model adapter reads it back in
// FromContext; a context without one is not an error, it simply records nothing.
func WithTracker(ctx context.Context, t *Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, t)
}

// FromContext returns the tracker attached to ctx, or nil when there is none.
// Callers must treat nil as "usage is not being collected" and skip recording
// rather than fail: the model adapter stays usable outside a tracked run.
func FromContext(ctx context.Context) *Tracker {
	t, _ := ctx.Value(trackerKey{}).(*Tracker)
	return t
}
