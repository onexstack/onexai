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

package agentbuilder

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// TestReductionHandlerNeedsASandbox pins the boundary: the reducer offloads
// full tool outputs to disk, so without the sandbox there is nowhere it may
// write and the engine's truncation guard remains the only bound.
func TestReductionHandlerNeedsASandbox(t *testing.T) {
	dir := t.TempDir()
	backend := newTestBackend(t, dir)

	b := NewBuilder(Deps{}, Options{MaxToolOutputChars: 1000, OffloadClearTokens: 10_000})
	if mw := b.reductionHandler(context.Background(), "", backend); mw != nil {
		t.Errorf("reductionHandler built a reducer with no skills directory")
	}
	if mw := b.reductionHandler(context.Background(), dir, nil); mw != nil {
		t.Errorf("reductionHandler built a reducer with no backend")
	}

	withBudgets := NewBuilder(Deps{}, Options{MaxToolOutputChars: 1000, OffloadClearTokens: 10_000})
	if mw := withBudgets.reductionHandler(context.Background(), dir, backend); mw == nil {
		t.Fatal("reductionHandler built no reducer despite both budgets being set")
	}

	// Both budgets off is a deployment that asked for no context management.
	off := NewBuilder(Deps{}, Options{})
	if mw := off.reductionHandler(context.Background(), dir, backend); mw != nil {
		t.Errorf("reductionHandler built a reducer with every budget disabled")
	}
}

// TestReductionRunsBeforeTheSummarizer pins the order that makes the cheap
// shedding pay first: at the shared threshold the reducer drops tool results
// without a model call, and the summarizer only runs if that was not enough.
func TestReductionRunsBeforeTheSummarizer(t *testing.T) {
	dir := t.TempDir()
	backend := newTestBackend(t, dir)
	summarizer := &instructionAppendHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, text: "sum"}

	b := NewBuilder(Deps{}, Options{MaxToolOutputChars: 1000, OffloadClearTokens: 10_000})
	got, err := b.assembleHandlers(context.Background(), Request{
		Handlers: HandlerGroups{SummaryHandler: summarizer},
	}, "agent", dir, backend, nil, nil)
	if err != nil {
		t.Fatalf("assembleHandlers: %v", err)
	}

	reducer, err := reduction.New(context.Background(), &reduction.Config{
		Backend:           backend,
		ReadFileToolName:  "read_file",
		MaxLengthForTrunc: 1000,
		MaxTokensForClear: 10_000,
	})
	if err != nil {
		t.Fatalf("reduction.New: %v", err)
	}

	var reducerAt, summarizerAt = -1, -1
	for i, mw := range got {
		if reflect.TypeOf(mw) == reflect.TypeOf(reducer) {
			reducerAt = i
		}
		if mw == adk.ChatModelAgentMiddleware(summarizer) {
			summarizerAt = i
		}
	}
	if reducerAt < 0 {
		t.Fatal("the reducer is not in the handler chain")
	}
	if summarizerAt < 0 {
		t.Fatal("the summarizer is not in the handler chain")
	}
	if reducerAt > summarizerAt {
		t.Errorf("reducer at %d runs after the summarizer at %d", reducerAt, summarizerAt)
	}
}

// TestRunOffloadPathStaysInsideTheSandbox pins the containment: a run ID is
// caller-supplied, and a "/" in one must not move the file out of its run
// directory.
func TestRunOffloadPathStaysInsideTheSandbox(t *testing.T) {
	root := filepath.Join(t.TempDir(), offloadDirName)
	ctx := agentapi.WithRunID(context.Background(), "../../escape/run-1")

	got := runOffloadPath(ctx, root, "trunc", nil)
	rel, err := filepath.Rel(root, got)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if len(rel) >= 2 && rel[:2] == ".." {
		t.Fatalf("offload path escaped the sandbox: %s", got)
	}
	if want := filepath.Join(root, "trunc", ".._.._escape_run-1"); filepath.Dir(got) != want {
		t.Errorf("run directory = %s, want %s", filepath.Dir(got), want)
	}
}

func TestRunOffloadPathUsesTheCallID(t *testing.T) {
	root := t.TempDir()
	ctx := agentapi.WithRunID(context.Background(), "run-1")

	got := runOffloadPath(ctx, root, "trunc", &reduction.ToolDetail{
		ToolContext: &adk.ToolContext{Name: "search", CallID: "call/1"},
	})
	if want := filepath.Join(root, "trunc", "run-1", "call_1"); got != want {
		t.Errorf("path = %s, want %s", got, want)
	}
}

// TestSweepOffloadRemovesOnlyExpiredRuns pins the collection bound: a paused
// run keeps its files until the checkpoint TTL passes, and expired ones do not
// accumulate in the skills directory forever.
func TestSweepOffloadRemovesOnlyExpiredRuns(t *testing.T) {
	skillsDir := t.TempDir()
	root := filepath.Join(skillsDir, offloadDirName, "trunc")
	oldDir := filepath.Join(root, "old-run")
	newDir := filepath.Join(root, "new-run")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "offload"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldDir, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	b := NewBuilder(Deps{}, Options{OffloadTTL: time.Hour})
	b.sweepOffload(skillsDir)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("expired run directory still exists (stat err = %v)", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("live run directory was removed: %v", err)
	}
}
