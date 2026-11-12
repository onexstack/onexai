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

package agent

import (
	"context"
	"sync"

	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/usage"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// runEmitter serialises sends on a run's event channel.
//
// Tool steps are emitted from the ADK's tool goroutine while the engine's own
// loop emits answers, usage and the terminal event, and that loop is what closes
// the channel. A send that loses the race against the close panics the whole
// agent process, so every send goes through here and a send arriving after
// the run finished is dropped instead.
//
// A send can block when the reader has stopped reading — a caller that
// disconnected, or one that stopped pumping — and holding the lock across that
// send would freeze every other emitter on the run. So once the run's context is
// gone (see stopWhen) a send that cannot be delivered is abandoned instead: the
// events after it are not worth a leaked goroutine holding a per-conversation
// slot.
type runEmitter struct {
	mu     sync.Mutex
	out    chan<- agentapi.Event
	closed bool
	// done, when non-nil, is the run's cancellation. A nil done means "block
	// until delivered", which is what the tests of the emission logic want.
	done <-chan struct{}
	// calls is how many model calls the last reported usage covered. It detects
	// a new call, which is the only moment usage changes.
	calls int
	// sinceCall holds the tool steps emitted since that last call, in order.
	// Their Tokens can only be derived once the next call reports usage — see
	// usage.ContextDelta — so they are re-emitted with the same ID at that point.
	sinceCall []agentapi.ToolStep
}

func newRunEmitter(out chan<- agentapi.Event) *runEmitter {
	return &runEmitter{out: out}
}

// stopWhen makes later sends abandonable once the run's context is done.
//
// It must be called before the run starts emitting: it is set once, without a
// lock, on the goroutine that owns the emitter until then.
func (e *runEmitter) stopWhen(done <-chan struct{}) *runEmitter {
	e.done = done
	return e
}

// deliver hands one event to the reader.
//
// It tries the channel first and only falls back to waiting when the buffer is
// full, so a run that is being cancelled still reports its terminal event to a
// caller that is still reading. Cancellation is a reason to give up on a reader
// that is not there; it is not a reason to withhold the news that the run
// ended.
func (e *runEmitter) deliver(ev agentapi.Event) {
	if e.done == nil {
		e.out <- ev
		return
	}
	select {
	case e.out <- ev:
		return
	default:
	}
	// Buffer full: wait for room, or give up once the run is over.
	select {
	case e.out <- ev:
	case <-e.done:
	}
}

// send delivers one event, remembering the tool steps it carries so the next
// model call can attribute tokens to them. Events arriving after finish are
// dropped rather than delivered.
func (e *runEmitter) send(ev agentapi.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if ev.Type == agentapi.EventToolStep {
		e.note(ev.Steps)
	}
	e.deliver(ev)
}

// finish delivers the run's terminal event and closes the emitter, so the tool
// goroutine's straggling sends are dropped instead of racing the channel close.
func (e *runEmitter) finish(ev agentapi.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	e.deliver(ev)
}

// note records the tool steps an event carried, collapsing repeats of the same
// invocation onto one entry: a step is matched by its tool-call ID, so a
// pending step and its terminal step — and, after an approval, a resumed run's
// repeat of the same call — update one row instead of adding another. Steps
// without an ID fall back to matching on the name.
// Image reports images a tool drew during the run.
//
// It implements agentapi.ImageSink, which the run's context carries so a tool
// reaches it. The event is not terminal: a run may draw several pictures and go
// on working, and a renderer shows each as it arrives rather than treating it
// as a result.
func (e *runEmitter) Image(_ context.Context, refs []media.Ref) {
	if len(refs) == 0 {
		return
	}
	e.send(agentapi.Event{Type: agentapi.EventImage, Images: refs})
}

// Sources reports the documents a retrieval returned during the run.
//
// It implements agentapi.SourceSink, which the run's context carries so a tool
// reaches it. Not terminal, for the reason Image gives: a run may retrieve more
// than once — one question can be asked of several knowledge bases, and a model
// may search, read, and search again — so a renderer shows each report as it
// arrives rather than treating the first as the answer's whole provenance.
func (e *runEmitter) Sources(_ context.Context, refs []agentapi.SourceRef) {
	if len(refs) == 0 {
		return
	}
	e.send(agentapi.Event{Type: agentapi.EventSources, Sources: refs})
}

func (e *runEmitter) note(steps []agentapi.ToolStep) {
	for _, st := range steps {
		if i := indexStep(e.sinceCall, st); i >= 0 {
			e.sinceCall[i] = st
			continue
		}
		e.sinceCall = append(e.sinceCall, st)
	}
}

// indexStep returns the position of want in steps, or -1.
func indexStep(steps []agentapi.ToolStep, want agentapi.ToolStep) int {
	for i, st := range steps {
		if want.ID != "" {
			if st.ID == want.ID {
				return i
			}
			continue
		}
		if st.ID == "" && st.Name == want.Name {
			return i
		}
	}
	return -1
}

// usageIfChanged reports the run's accumulated usage once a new model call has
// been recorded, and attributes that call's context growth to the tool steps
// emitted since the previous one.
//
// This is where the per-tool token figure comes from, and why it appears one
// round late: an OpenAI-compatible API reports usage per model call and never
// per tool, so the only real measurement of what a batch of tools added is
// prompt(next) − prompt(prev) − completion(prev). The same arithmetic means
// tools invoked in one parallel batch share a single figure — there is no way
// to split it, and dividing it would be an invention.
func (e *runEmitter) usageIfChanged(tk *usage.Tracker) {
	if tk == nil {
		return
	}
	totals := tk.Totals()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || totals.Calls <= e.calls {
		return
	}
	e.calls = totals.Calls
	e.out <- agentapi.Event{Type: agentapi.EventUsage, Usage: toRunUsage(totals)}

	if delta := usage.ContextDelta(tk.Calls(), totals.Calls-1); delta > 0 {
		for _, st := range e.sinceCall {
			st.Tokens = delta
			e.out <- agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{st}}
		}
	}
	e.sinceCall = e.sinceCall[:0]
}

// toRunUsage converts accumulated usage to the form carried by an event.
// reportRunUsage records a finished run's token totals.
//
// It is called where the totals are read for the caller, so the metric and the
// number the caller sees are the same number.
func reportRunUsage(t usage.Totals) {
	metrics.M.AddTokens("prompt", t.PromptTokens)
	metrics.M.AddTokens("completion", t.CompletionTokens)
	metrics.M.AddTokens("reasoning", t.ReasoningTokens)
	// The cache split rides its own kinds rather than replacing "prompt": the
	// prompt total stays the thing that matches the upstream's bill, and these
	// two are how much of it was cheap. A hit rate is the ratio of the two, so a
	// dashboard needs them side by side and neither alone.
	metrics.M.AddTokens("cache_hit", t.CacheHitTokens)
	metrics.M.AddTokens("cache_miss", t.CacheMissTokens)
	metrics.M.AddTokens("cache_write", t.CacheWriteTokens)
}

func toRunUsage(t usage.Totals) *agentapi.Usage {
	return &agentapi.Usage{
		Calls:            t.Calls,
		PromptTokens:     t.PromptTokens,
		CompletionTokens: t.CompletionTokens,
		TotalTokens:      t.TotalTokens,
		ReasoningTokens:  t.ReasoningTokens,
		CacheHitTokens:   t.CacheHitTokens,
		CacheMissTokens:  t.CacheMissTokens,
		CacheWriteTokens: t.CacheWriteTokens,
	}
}
