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

package mcpclient

import (
	"sync"
	"time"
)

const (
	// breakerFailures is how many consecutive timeouts a tool may hit before
	// calls to it are refused without dialing.
	//
	// The number mirrors the run-lifecycle hooks' own first threshold: one
	// timeout is a slow call, four in a row is a server that is not answering.
	breakerFailures = 4
	// breakerCooldown is how long an open breaker refuses calls before one is
	// let through to test the server again.
	//
	// It is deliberately shorter than the tool-set cache TTL: the breaker is
	// about one call costing a run its whole budget, not about how long a
	// discovery result is trusted.
	breakerCooldown = time.Minute
)

// guard is one (server, tool) pair's timeout history.
//
// It is a circuit breaker with a deliberately narrow trigger: only a call the
// per-tool timeout cut short counts as a failure. A tool that answers with an
// error is responsive and resets the streak, and a call cut short by the run's
// own deadline is neither — the run ending says nothing about the server.
//
// This is what keeps one hung MCP server from costing every later run its full
// tool timeout (see the cascading-failures anti-pattern): after the threshold,
// the model gets an immediate, explanatory error instead of a minute of silence.
type guard struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	now       func() time.Time
}

// allow reports whether a call may be attempted now.
//
// An expired cooldown allows exactly the next call through; whether the breaker
// closes or reopens is decided by that call's outcome. The failure count is not
// reset on expiry, so a still-broken server reopens immediately rather than
// needing another full streak.
func (g *guard) allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.now().Before(g.openUntil)
}

// succeeded records that the server answered, breaking any timeout streak.
func (g *guard) succeeded() {
	g.mu.Lock()
	g.failures = 0
	g.openUntil = time.Time{}
	g.mu.Unlock()
}

// timedOut records a call this tool's own timeout cut short, and reports
// whether this failure is the one that opened the breaker.
func (g *guard) timedOut() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures++
	if g.failures < breakerFailures {
		return false
	}
	opened := g.openUntil.IsZero() || !g.now().Before(g.openUntil)
	g.openUntil = g.now().Add(breakerCooldown)
	return opened
}

// guardSet hands out one guard per tool. It lives on the Manager, so a streak
// survives both a cache rebuild and the tool being rebuilt for another agent
// set — the server is the same server either way.
type guardSet struct {
	now   func() time.Time
	mu    sync.Mutex
	byKey map[string]*guard
}

func newGuardSet(now func() time.Time) *guardSet {
	return &guardSet{now: now, byKey: make(map[string]*guard)}
}

// get returns the guard for key, creating it on first use. key names the server
// as well as the tool: a model-facing name is unique within one server set, not
// across every set the process has served.
func (s *guardSet) get(key string) *guard {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.byKey[key]
	if !ok {
		g = &guard{now: s.now}
		s.byKey[key] = g
	}
	return g
}
