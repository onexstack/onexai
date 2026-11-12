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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/contextx"
)

// guardedTool builds a wireTool over inner, with a guard that uses the given
// clock.
func guardedTool(t *testing.T, inner tool.InvokableTool, now func() time.Time) (*wireTool, *guard) {
	t.Helper()
	w, err := newWireTool(context.Background(), inner, "test_tool")
	if err != nil {
		t.Fatalf("newWireTool: %v", err)
	}
	g := newGuardSet(now).get("server\x00test_tool")
	w.guard = g
	return w, g
}

func TestGuardOpensAfterConsecutiveToolTimeouts(t *testing.T) {
	now := time.Unix(1000, 0)
	guards := newGuardSet(func() time.Time { return now })
	g := guards.get("server\x00tool")

	for i := range breakerFailures {
		if !g.allow() {
			t.Fatalf("refused after %d failures, want allow", i)
		}
		g.timedOut()
	}
	if g.allow() {
		t.Fatalf("still allowed after %d consecutive timeouts", breakerFailures)
	}

	now = now.Add(breakerCooldown - time.Second)
	if g.allow() {
		t.Fatal("allowed before the cooldown elapsed")
	}
	now = now.Add(2 * time.Second)
	if !g.allow() {
		t.Fatal("refused after the cooldown elapsed")
	}

	g.succeeded()
	for range breakerFailures - 1 {
		g.timedOut()
	}
	if !g.allow() {
		t.Fatal("a success did not reset the timeout streak")
	}
}

func TestWireToolRefusesWhileTheToolKeepsTimingOut(t *testing.T) {
	now := time.Unix(2000, 0)
	var calls []string
	inner := &recordedTool{wire: "test_tool", err: context.DeadlineExceeded, calls: &calls}
	w, _ := guardedTool(t, inner, func() time.Time { return now })
	ctx := contextx.WithToolTimeout(context.Background())

	for range breakerFailures {
		if _, err := w.InvokableRun(ctx, "{}"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call before the breaker opened: err = %v", err)
		}
	}
	if len(calls) != breakerFailures {
		t.Fatalf("server was dialed %d times, want %d", len(calls), breakerFailures)
	}

	_, err := w.InvokableRun(ctx, "{}")
	if err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("open breaker returned %v, want a temporary-unavailable error", err)
	}
	if len(calls) != breakerFailures {
		t.Fatal("an open breaker still called the server")
	}

	// After the cooldown one call is let through; a success closes the breaker.
	now = now.Add(breakerCooldown + time.Second)
	inner.err = nil
	if _, err := w.InvokableRun(ctx, "{}"); err != nil {
		t.Fatalf("half-open call: %v", err)
	}
	if len(calls) != breakerFailures+1 {
		t.Fatalf("server was dialed %d times, want %d", len(calls), breakerFailures+1)
	}
}

// The run's own deadline says nothing about the server, so it must not open a
// breaker — otherwise every run that ends near its budget would poison the MCP
// tools for the next minute.
func TestWireToolDoesNotBlameTheServerForTheRunsDeadline(t *testing.T) {
	var calls []string
	inner := &recordedTool{wire: "test_tool", err: context.DeadlineExceeded, calls: &calls}
	w, _ := guardedTool(t, inner, time.Now)

	for range breakerFailures + 2 {
		if _, err := w.InvokableRun(context.Background(), "{}"); err == nil {
			t.Fatal("want the deadline error")
		}
	}
	if len(calls) != breakerFailures+2 {
		t.Fatalf("server was dialed %d times; the run's deadline was counted as a server failure", len(calls))
	}
}

// A tool that answers with an error is responsive. That answer ends a timeout
// streak rather than extending it.
func TestWireToolResetsTheStreakWhenTheServerAnswers(t *testing.T) {
	var calls []string
	inner := &recordedTool{wire: "test_tool", err: context.DeadlineExceeded, calls: &calls}
	w, _ := guardedTool(t, inner, time.Now)
	ctx := contextx.WithToolTimeout(context.Background())

	for range breakerFailures - 1 {
		_, _ = w.InvokableRun(ctx, "{}")
	}
	inner.err = errors.New("invalid arguments")
	if _, err := w.InvokableRun(ctx, "{}"); err == nil {
		t.Fatal("want the tool's own error")
	}
	inner.err = context.DeadlineExceeded
	for range breakerFailures - 1 {
		_, _ = w.InvokableRun(ctx, "{}")
	}
	_, err := w.InvokableRun(ctx, "{}")
	if err != nil && strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatal("an answered error did not reset the timeout streak")
	}
}

// The guard must survive a cache rebuild: the Manager owns it, not the entry.
func TestManagerSkipsAToolWhoseServerKeepsTimingOut(t *testing.T) {
	var calls []string
	inner := &recordedTool{wire: "test_tool", err: context.DeadlineExceeded, calls: &calls}
	dialer := newFakeDialer()
	dialer.tools["srv"] = []tool.BaseTool{inner}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("srv")}}, Options{
		Enabled: true,
		Dialer:  dialer,
	})
	defer m.Close()

	tools := m.Tools([]string{"srv"}, nil)
	if len(tools) != 1 {
		t.Fatalf("Tools returned %d tools, want 1", len(tools))
	}
	invokable, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool %T is not invokable", tools[0])
	}
	ctx := contextx.WithToolTimeout(context.Background())
	for range breakerFailures {
		// The manager wraps tools with the error handler, which turns a failure
		// into model-visible text and a nil error.
		if _, err := invokable.InvokableRun(ctx, "{}"); err != nil {
			t.Fatalf("InvokableRun: %v", err)
		}
	}
	if len(calls) != breakerFailures {
		t.Fatalf("server was dialed %d times, want %d", len(calls), breakerFailures)
	}
	if _, err := invokable.InvokableRun(ctx, "{}"); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if len(calls) != breakerFailures {
		t.Fatal("the manager did not attach a guard: an open breaker still called the server")
	}
}
