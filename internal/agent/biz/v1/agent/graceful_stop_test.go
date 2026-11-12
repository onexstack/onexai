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
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// TestEinoEngineStopsAtAToolBoundary pins what a stop means now: the tool that
// is already running finishes, the run ends as stopped, and nothing after it
// runs. Hard-cancelling the context used to abort the call wherever it was,
// which is how a stopped run could still leave a destructive tool half-done.
func TestEinoEngineStopsAtAToolBoundary(t *testing.T) {
	var ran atomic.Int32
	started := make(chan struct{}, 1)
	slow, err := utils.InferTool("slow_tool", "Take a moment before answering.",
		func(context.Context, noArgs) (string, error) {
			ran.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			// Long enough that a hard cancel would land inside the call, short
			// enough to stay well inside the engine's grace period.
			time.Sleep(150 * time.Millisecond)
			return "tool finished", nil
		})
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}

	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", "slow_tool", `{}`)),
		streamReply(`{"content":"finished"}`),
	)
	eng := NewEngine(EngineConfig{
		BaseURL:     up.URL,
		Model:       "test-model",
		Tools:       func([]string, []string) []tool.BaseTool { return []tool.BaseTool{slow} },
		Checkpoints: checkpoint.New(nil, "onex", time.Minute),
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}

	var stop func()
	ch, err := eng.Run(t.Context(), []agentapi.Message{{Role: agentapi.RoleUser, Content: "run the tool"}}, agentapi.RunOptions{
		RunID:       "stop-at-boundary",
		OnStopReady: func(s func()) { stop = s },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stop == nil {
		t.Fatal("the engine offered no stop, so a stop would have to cancel the context")
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the tool never started")
	}
	stop()

	events := drain(t, ch)
	if len(events) == 0 {
		t.Fatal("the stopped run emitted no events")
	}
	for _, ev := range events {
		switch ev.Type {
		case agentapi.EventDone, agentapi.EventFailed:
			t.Fatalf("a stopped run also reported %v: %+v", ev.Type, ev)
		}
	}
	if last := events[len(events)-1].Type; last != agentapi.EventStopped {
		t.Fatalf("the stopped stream ended with %v, want EventStopped", last)
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("the tool ran %d times, want exactly 1 (the call that was already in flight)", got)
	}
	// The model was asked once: the stop landed before the round that would have
	// answered from the tool's result.
	if got := up.calls(); got != 1 {
		t.Errorf("the model was called %d times, want 1: a stopped run must not start another round", got)
	}
}
