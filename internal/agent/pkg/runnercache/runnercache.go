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
// The original repo for this file is https://github.com/onexstack/onex.

// Package runnercache keeps built agent runners alive between requests.
//
// Building a runner costs a graph construction and, when skills are involved,
// a checkout: work that depends only on which agents a run selected. The same
// selection arrives over and over — every message in a conversation with one
// assistant selects the same agents — so the work is done once and the runner
// reused.
//
// Two properties make that safe, and both are the caller's responsibility:
//
//   - The runner must be a function of the agent set alone. Anything that
//     differs between two runs of the same set — the event sink its tools
//     report to, the approval policy governing it, the authorization gate — has
//     to travel on the run's context rather than being captured when the graph
//     is built.
//   - A run that paused for a human must be resumed by the same graph. The
//     checkpoint addresses the interrupt by position in the graph, so a runner
//     rebuilt in the meantime may not line up. Pin holds an entry still for
//     exactly this reason: while a run that paused against it may still be
//     answered, the entry is neither rebuilt nor evicted.
package runnercache

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"golang.org/x/sync/singleflight"
)

// errNoBuildFunc is returned by a Cache that was constructed without a builder,
// which is a programming error rather than a runtime condition.
var errNoBuildFunc = errors.New("runnercache: no BuildFunc was supplied")

// BuildFunc builds a runner for an agent set, and reports the model-facing
// tool names the graph was built with. It is a function type rather than an
// interface so this package depends on nothing but eino — and therefore neither
// on the agent builder nor on the store.
//
// The tool names travel with the runner because a suspended run resumes by
// naming the tool it paused on: a graph that no longer has that tool cannot
// honour the answer, and the caller needs the names to notice before resuming.
type BuildFunc func(ctx context.Context, agentNames []string) (*adk.Runner, []string, error)

// entry is one cached runner and the bookkeeping around it.
type entry struct {
	runner  *adk.Runner
	tools   []string
	builtAt time.Time
	// fingerprint digests the resolved capability set the graph was built from.
	// The cache key stays the agent set, because a paused run addresses its
	// checkpoint by position in the graph and must find the same entry; the
	// fingerprint is a freshness check on top of it. See fresh.
	fingerprint string
	// pinnedUntil is how long the runs that paused against this exact graph may
	// still be answered. A pinned entry is never rebuilt and never evicted: the
	// human's answer will arrive at an address this graph is the only one that
	// can resolve.
	//
	// It is a deadline rather than a count so it cannot leak. A pause nobody
	// ever answers would hold a count forever, and that entry would then never
	// pick up a change; a deadline bounds the damage to one missed refresh.
	pinnedUntil time.Time
}

// Cache caches runners by agent set.
type Cache struct {
	build BuildFunc
	ttl   time.Duration

	mu      sync.Mutex
	entries map[string]*entry
	sf      singleflight.Group

	// sweeping guards the opportunistic sweep below so at most one runs at a
	// time, and staleThreshold bounds how often one starts.
	sweeping       atomic.Bool
	staleThreshold time.Duration
}

// New returns a Cache. A ttl of zero or less disables expiry, which means an
// entry is rebuilt only when it is explicitly invalidated.
func New(build BuildFunc, ttl time.Duration) *Cache {
	threshold := ttl
	if threshold <= 0 {
		threshold = defaultSweepThreshold
	}
	return &Cache{
		build:          build,
		ttl:            ttl,
		entries:        make(map[string]*entry),
		staleThreshold: threshold,
	}
}

// defaultSweepThreshold is how long a stale entry may linger when caching has
// no TTL to hang the sweep off. Without expiry nothing is ever stale, so the
// sweep is effectively disabled and this value never matters.
const defaultSweepThreshold = time.Hour

// Enabled reports whether anything will be cached. It is safe on a nil Cache,
// which spares every call site a nil check.
func (c *Cache) Enabled() bool { return c != nil && c.build != nil }

// Get returns the runner for an agent set, building it if necessary, together
// with the key it was cached under.
//
// A stale entry is rebuilt synchronously rather than served while refreshing in
// the background. Serving a stale runner would mean serving a graph the caller
// may then pause against and be unable to resume — the one failure this cache
// must not introduce. The rebuild is a graph construction, not a network call,
// so waiting for it is cheap.
func (c *Cache) Get(ctx context.Context, agentNames []string) (*adk.Runner, string, error) {
	runner, _, key, err := c.GetWithTools(ctx, agentNames, "")
	return runner, key, err
}

// GetWithTools is Get, plus the model-facing tool names the returned graph was
// built with. A caller that is about to resume a suspended run needs them: the
// run resumes by naming the tool it paused on, and a graph that lost that tool
// would otherwise answer from the unknown-tool text and report the run done.
//
// A non-empty fingerprint is a freshness check: when it differs from the one the
// cached graph was built with, the entry is rebuilt unless a paused run depends
// on it. An empty fingerprint keeps the previous behavior (agent set only).
func (c *Cache) GetWithTools(ctx context.Context, agentNames []string, fingerprint string) (*adk.Runner, []string, string, error) {
	key := Key(agentNames)
	if !c.Enabled() {
		runner, tools, err := c.buildIfPossible(ctx, agentNames)
		return runner, tools, key, err
	}

	if runner, tools, ok := c.fresh(key, fingerprint); ok {
		c.maybeSweep()
		return runner, tools, key, nil
	}

	// The singleflight key carries the fingerprint: two callers asking for
	// different capability sets must not share one build.
	flight := key + "\x00" + fingerprint
	result, err, _ := c.sf.Do(flight, func() (any, error) {
		// Another goroutine may have built it while this one waited.
		if runner, tools, ok := c.fresh(key, fingerprint); ok {
			return built{runner: runner, tools: tools}, nil
		}
		runner, tools, err := c.build(ctx, agentNames)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		// Carry a live pin across the rebuild: a pause that raced it must not
		// lose the protection it depends on.
		pinnedUntil := time.Time{}
		if existing, ok := c.entries[key]; ok && time.Now().Before(existing.pinnedUntil) {
			pinnedUntil = existing.pinnedUntil
		}
		c.entries[key] = &entry{
			runner:      runner,
			tools:       tools,
			builtAt:     time.Now(),
			fingerprint: fingerprint,
			pinnedUntil: pinnedUntil,
		}
		c.mu.Unlock()
		return built{runner: runner, tools: tools}, nil
	})
	if err != nil {
		return nil, nil, key, err
	}
	b := result.(built)
	return b.runner, b.tools, key, nil
}

// built is what the singleflight group returns: the two facts a caller needs.
type built struct {
	runner *adk.Runner
	tools  []string
}

// buildIfPossible builds without caching, for the disabled case.
func (c *Cache) buildIfPossible(ctx context.Context, agentNames []string) (*adk.Runner, []string, error) {
	if c.build == nil {
		return nil, nil, errNoBuildFunc
	}
	return c.build(ctx, agentNames)
}

// maybeSweep drops expired, unpinned entries.
//
// Without it the map only ever loses the key being looked up, so an agent set
// that is used once and never again stays resident for the process's lifetime —
// a slow leak on a deployment with many selections. It runs on the read path
// rather than on a timer so a cache nobody reads costs nothing.
func (c *Cache) maybeSweep() {
	if c.ttl <= 0 || !c.sweeping.CompareAndSwap(false, true) {
		return
	}
	defer c.sweeping.Store(false)

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.entries {
		if now.Before(e.pinnedUntil) || now.Sub(e.builtAt) < c.ttl {
			continue
		}
		delete(c.entries, key)
	}
}

// fresh reports whether the cached entry for key is usable as it stands.
func (c *Cache) fresh(key, fingerprint string) (*adk.Runner, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, nil, false
	}
	if fingerprint != "" && e.fingerprint != fingerprint {
		// A paused run resumes by position in the graph it paused against, so a
		// pinned entry is served even when the capability set has moved on. The
		// next run after the pause ends rebuilds.
		if time.Now().Before(e.pinnedUntil) {
			return e.runner, e.tools, true
		}
		delete(c.entries, key)
		return nil, nil, false
	}
	if c.ttl > 0 && !time.Now().Before(e.pinnedUntil) && time.Since(e.builtAt) >= c.ttl {
		// Expired and nobody depends on this exact graph: drop it so the caller
		// rebuilds rather than resumes against a graph that may not match.
		delete(c.entries, key)
		return nil, nil, false
	}
	return e.runner, e.tools, true
}

// Pin marks an agent set's graph as depended on by a run that just paused, so
// it is not rebuilt for as long as that pause may still be answered — normally
// the checkpoint TTL.
//
// It takes a duration rather than requiring a matching Unpin because the answer
// may never come: a card nobody clicks would otherwise pin the entry forever.
// Unpin releases it early on the path where the run does finish, and the
// deadline bounds every other path.
func (c *Cache) Pin(agentNames []string, d time.Duration) {
	if !c.Enabled() || d <= 0 {
		return
	}
	key := Key(agentNames)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return
	}
	if until := time.Now().Add(d); until.After(e.pinnedUntil) {
		e.pinnedUntil = until
	}
}

// Unpin releases an agent set's pin early, for a run that finished before its
// deadline. It is a no-op for a set that is not pinned.
func (c *Cache) Unpin(agentNames []string) {
	if !c.Enabled() {
		return
	}
	key := Key(agentNames)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.pinnedUntil = time.Time{}
	}
}

// Len reports how many runners are cached, for logs and tests.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Key returns the cache key of an agent set: the names, sorted and joined.
//
// Sorting is what makes the key independent of the order the caller listed the
// agents in, so the same selection arriving two ways shares one entry. An empty
// set keys on a fixed marker rather than the empty string, so a run that
// selects nothing is still a cacheable selection rather than a miss.
func Key(agentNames []string) string {
	names := make([]string, 0, len(agentNames))
	seen := make(map[string]struct{}, len(agentNames))
	for _, n := range agentNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	if len(names) == 0 {
		return "__no_agents__"
	}
	sort.Strings(names)
	return strings.Join(names, ".")
}
