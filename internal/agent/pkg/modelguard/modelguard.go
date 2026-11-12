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

// Package modelguard bounds and protects model endpoint calls.
//
// It exists because the two failure modes an endpoint has are not the ones the
// existing layers handle. The chat client retries a call that failed; the agent
// fails over to another model after retries. Neither notices that a failing
// endpoint is being hammered by every run at once, and neither caps how many
// calls one endpoint gets — so a provider outage turns into a storm of retries
// that keeps the outage alive, and a deployment with a per-endpoint quota has no
// place to enforce it.
//
// The guard sits at the model call itself (a WrapModel middleware), which is the
// one place every call passes through and the one place whose errors are seen by
// retry and failover above it. Two mechanisms:
//
//   - a token bucket per endpoint, which refuses calls above the configured rate;
//   - a consecutive-failure breaker per endpoint, which refuses calls while the
//     endpoint is known to be failing, and probes it again after a cooldown.
//
// Both are keyed by the run's endpoint name, because that is what an operator
// configured and what a quota belongs to.
package modelguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ErrRateLimited is returned when an endpoint's token bucket is empty.
var ErrRateLimited = errors.New("model guard: rate limited")

// ErrCircuitOpen is returned while an endpoint's breaker is open.
var ErrCircuitOpen = errors.New("model guard: circuit open")

// Config configures the guard. A zero Config disables both mechanisms.
type Config struct {
	// QPS is the sustained call rate allowed per endpoint. Zero disables the
	// bucket.
	QPS float64
	// Burst is the bucket's capacity, i.e. how many calls may arrive at once
	// before the rate applies. Zero uses one call.
	Burst int
	// FailureThreshold is how many consecutive failures open the breaker. Zero
	// disables the breaker.
	FailureThreshold int
	// Cooldown is how long an open breaker refuses calls before letting one
	// probe through. Zero uses 30s.
	Cooldown time.Duration
}

// Guard applies the configured mechanisms. It is safe for concurrent use and is
// a no-op when disabled.
type Guard struct {
	cfg Config
	now func() time.Time

	mu       sync.Mutex
	buckets  map[string]*bucket
	breakers map[string]*breaker
}

type bucket struct {
	tokens float64
	last   time.Time
}

type breaker struct {
	failures int
	// openUntil is when an open breaker may be probed. Zero means closed.
	openUntil time.Time
}

// New returns a guard for cfg. It returns nil when both mechanisms are off, so a
// caller can compare the pointer.
func New(cfg Config) *Guard {
	if cfg.QPS <= 0 && cfg.FailureThreshold <= 0 {
		return nil
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 1
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	return &Guard{
		cfg:      cfg,
		now:      time.Now,
		buckets:  make(map[string]*bucket),
		breakers: make(map[string]*breaker),
	}
}

// Enabled reports whether the guard will do anything.
func (g *Guard) Enabled() bool { return g != nil }

// Allow decides whether a call to key may proceed. It consumes a token when the
// bucket is configured, and refuses while the breaker is open.
func (g *Guard) Allow(key string) error {
	if !g.Enabled() {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	if g.cfg.QPS > 0 {
		b := g.buckets[key]
		if b == nil {
			b = &bucket{tokens: float64(g.cfg.Burst), last: now}
			g.buckets[key] = b
		}
		b.tokens += now.Sub(b.last).Seconds() * g.cfg.QPS
		if b.tokens > float64(g.cfg.Burst) {
			b.tokens = float64(g.cfg.Burst)
		}
		b.last = now
		if b.tokens < 1 {
			return fmt.Errorf("%w (%s)", ErrRateLimited, key)
		}
		b.tokens--
	}

	if g.cfg.FailureThreshold > 0 {
		br := g.breakers[key]
		if br != nil && !br.openUntil.IsZero() {
			if now.Before(br.openUntil) {
				return fmt.Errorf("%w (%s)", ErrCircuitOpen, key)
			}
			// Cooldown elapsed: let one call through as a probe. A further
			// failure reopens immediately because the count is kept at the
			// threshold.
			br.openUntil = time.Time{}
			if br.failures >= g.cfg.FailureThreshold {
				br.failures = g.cfg.FailureThreshold - 1
			}
		}
	}
	return nil
}

// Record reports a call's outcome to the breaker. An error counts as a failure;
// a success closes the breaker and clears the streak.
func (g *Guard) Record(key string, err error) {
	if !g.Enabled() || g.cfg.FailureThreshold <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	br := g.breakers[key]
	if br == nil {
		br = &breaker{}
		g.breakers[key] = br
	}
	if err == nil {
		br.failures = 0
		br.openUntil = time.Time{}
		return
	}
	br.failures++
	if br.failures >= g.cfg.FailureThreshold {
		br.openUntil = g.now().Add(g.cfg.Cooldown)
	}
}

// Middleware wraps every model call with the guard.
func (g *Guard) Middleware() adk.ChatModelAgentMiddleware {
	return &guardMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		guard:                        g,
	}
}

type guardMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	guard *Guard
}

func (m *guardMiddleware) WrapModel(
	ctx context.Context, inner model.BaseChatModel, _ *adk.ModelContext,
) (model.BaseChatModel, error) {
	if !m.guard.Enabled() {
		return inner, nil
	}
	// The key is the endpoint the run resolved, which is what a quota belongs
	// to. A run with no endpoint name shares the "default" bucket.
	key := agentapi.ModelNameFromContext(ctx)
	if key == "" {
		key = "default"
	}
	return &guardedModel{inner: inner, guard: m.guard, key: key}, nil
}

type guardedModel struct {
	inner model.BaseChatModel
	guard *Guard
	key   string
}

func (m *guardedModel) Generate(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	if err := m.guard.Allow(m.key); err != nil {
		return nil, err
	}
	out, err := m.inner.Generate(ctx, in, opts...)
	m.guard.Record(m.key, err)
	return out, err
}

// Stream guards the call that opens a stream. A failure that happens mid-stream
// is not observed: the guard's unit is the call, and the stream is handed back
// before its content arrives. Retry and failover above see only the opening
// error for the same reason.
func (m *guardedModel) Stream(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.guard.Allow(m.key); err != nil {
		return nil, err
	}
	sr, err := m.inner.Stream(ctx, in, opts...)
	m.guard.Record(m.key, err)
	return sr, err
}
