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
	"bytes"
	"context"
	"encoding/gob"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/redis/go-redis/v9"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// The approval tests run over the in-memory checkpoint store, which proves the
// pause/resume protocol but not the bytes it stores: gob registration and the
// Redis encoding only fail on a real store. These tests use miniredis so the
// resumed half is built by a **different engine instance** reading what the
// paused half actually wrote — the same thing two pods do, minus the network.

// redisCheckpointStore returns a checkpoint store backed by a real gob encode
// and a real Redis client (miniredis).
func redisCheckpointStore(t *testing.T) checkpoint.Store {
	t.Helper()
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return checkpoint.New(rdb, "onex", time.Minute)
}

// engineOver builds an engine whose checkpoint store is explicit, so two
// engines can share one store exactly as two pods share Redis.
func engineOver(t *testing.T, url string, store checkpoint.Store, tools ...tool.BaseTool) agentapi.Engine {
	t.Helper()
	eng := NewEngine(EngineConfig{
		BaseURL:     url,
		Model:       "test-model",
		Tools:       func([]string, []string) []tool.BaseTool { return tools },
		Checkpoints: store,
		Reserved:    []string{"ask_user_question"},
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}
	return eng
}

// TestCheckpointResumeCrossesEngineInstances is the guard for the constraint
// the whole resume path rests on: a suspended run must be rebuildable by a
// different process, from nothing but the checkpoint and the run's own options.
// The in-memory store cannot show that — it also hides a gob registration
// mistake, because the same process already has the type registered.
func TestCheckpointResumeCrossesEngineInstances(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	store := redisCheckpointStore(t)
	runs := gatedRun("resume-across-instances")

	paused := engineOver(t, up.URL, store, newGatedTool(t, spy))
	ch, err := paused.Run(t.Context(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	// The checkpoint must be in the store before anything can resume: this is
	// the bytes a second pod would read.
	raw, ok, err := store.Get(context.Background(), runs.RunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || len(raw) == 0 {
		t.Fatal("the paused run wrote no checkpoint to the store")
	}

	// A separate engine: same store, same tools, no shared process state.
	resumed := engineOver(t, up.URL, store, newGatedTool(t, spy))
	resumable, ok := resumed.(agentapi.Resumable)
	if !ok {
		t.Fatal("the eino engine cannot resume a suspended run")
	}
	out, err := resumable.Resume(t.Context(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if answer := drainToAnswer(t, out); answer != "已删除" {
		t.Errorf("the resumed run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 || calls[0] != "test" {
		t.Errorf("the tool ran with %v, want exactly [test]", calls)
	}
}

// TestCheckpointResumeRefusesAGraphWithoutTheApprovedTool pins what happens
// when the graph changed under a suspended run. Resuming is only meaningful if
// the address that paused still resolves to the tool the human approved; if it
// does not, the run must not be reported as finished, because a card that says
// "done" would claim work that never ran.
func TestCheckpointResumeRefusesAGraphWithoutTheApprovedTool(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	store := redisCheckpointStore(t)
	runs := gatedRun("resume-missing-tool")

	paused := engineOver(t, up.URL, store, newGatedTool(t, spy))
	ch, err := paused.Run(t.Context(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	// The resumed engine no longer has the gated tool: a deploy removed it, or
	// a different agent selection resolved a different set.
	other, err := utils.InferTool("echo", "Echo the argument.", func(_ context.Context, args gatedArgs) (string, error) {
		return args.Cluster, nil
	})
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}
	resumed := engineOver(t, up.URL, store, other)
	resumable, ok := resumed.(agentapi.Resumable)
	if !ok {
		t.Fatal("the eino engine cannot resume a suspended run")
	}
	out, err := resumable.Resume(t.Context(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		// Refusing before the run starts is the clearest outcome.
		if calls := spy.calls(); len(calls) != 0 {
			t.Fatalf("the missing tool still ran: %v", calls)
		}
		return
	}

	done := false
	for _, ev := range drain(t, out) {
		switch ev.Type {
		case agentapi.EventDone:
			done = true
		case agentapi.EventFailed:
			done = false
		}
	}
	if done {
		t.Fatal("a run whose graph lost the approved tool reported Done")
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("the missing tool still ran: %v", calls)
	}
}

// TestCheckpointInterruptPayloadsKeepTheirRegisteredNames pins the gob names the
// interrupt payloads are stored under. A checkpoint that is already on disk
// names its types; renaming a registration does not fail any test that encodes
// and decodes in one process, but it makes every stored checkpoint undecodable.
func TestCheckpointInterruptPayloadsKeepTheirRegisteredNames(t *testing.T) {
	for name, payload := range map[string]any{
		"onexai_permission_info":   &agentapi.PermissionInfo{ToolName: "delete_cluster", CallID: "call_1"},
		"onexai_ask_question_info": &agentapi.AskQuestionInfo{Question: "继续吗？"},
	} {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(struct{ Info any }{Info: payload}); err != nil {
			t.Fatalf("encode %T: %v", payload, err)
		}
		if !strings.Contains(buf.String(), name) {
			t.Errorf("%T is encoded without the registered name %q; stored checkpoints will not decode", payload, name)
		}
	}
}
