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
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// TestPausedRunCheckpointHoldsReferencesNotBytes is the regression this design
// exists for.
//
// A run that pauses for approval writes its state to the checkpoint store, and a
// resumed run is reconstructed from it. If the image bytes were resolved before
// the engine was invoked — which is where they were resolved before this change
// — that state would hold a data URL, and the checkpoint, which is Redis, would
// carry a base64 payload for every paused run. That is the cost the reference
// abstraction was introduced to remove, and a pause is exactly where it would
// come back.
//
// The test drives the whole path, Ask included, and asserts both halves at one
// instant: the model was given bytes (the upstream request body carries a data
// URL), and the checkpoint holds the reference and no bytes. Either alone would
// pass while the property was broken — an engine that was handed nothing, or a
// checkpoint that received everything.
func TestPausedRunCheckpointHoldsReferencesNotBytes(t *testing.T) {
	ctx := t.Context()

	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	bundle := media.NewBundle(store, nil)
	ref, err := bundle.Put(ctx, media.KindImage, "ou_asker", "shot.png", testPNGBytes(t))
	if err != nil {
		t.Fatalf("store the test image: %v", err)
	}
	rows := &stubRows{row: &model.AssetM{ID: ref.AssetID, ObjectKey: ref.ObjectKey}}

	cps := checkpoint.New(nil, "onex", time.Minute)
	up := newApprovalUpstream(t, streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)))
	eng := NewEngine(EngineConfig{
		BaseURL:     up.URL,
		Model:       "test-model",
		Tools:       func([]string, []string) []tool.BaseTool { return []tool.BaseTool{newGatedTool(t, &gateSpy{})} },
		Checkpoints: cps,
		Reserved:    []string{"ask_user_question"},
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}

	b := New(Deps{
		Engine:               eng,
		Sessions:             mem.NewSessionStore(nil, "test", 0),
		Stops:                runstop.NewLocal(),
		Checkpoints:          cps,
		MaxConcurrentPerConv: 1,
		RequireApproval:      []string{gatedToolName},
		Images:               agentedge.New(agentedge.Deps{Bundle: bundle, Rows: rows}),
	})

	events, err := b.Ask(ctx, &agentapi.AskRequest{
		SessionID: "c1",
		Input:     agentapi.Input{Text: "描述这张图", Images: []media.Ref{ref}},
		Subject:   agentapi.Principal{ID: "ou_asker"},
		RunID:     "leak",
	})
	if err != nil {
		t.Fatalf("Ask returned %v", err)
	}
	drainToPause(t, events)

	// The model was given bytes: resolving inside the call is the point.
	if body := up.sentToModel(t, 1); !strings.Contains(body, "data:image/png;base64,") {
		t.Errorf("the model was not given the image; the request body was %.200s", body)
	}

	raw, found, err := cps.Get(context.Background(), "leak")
	if err != nil {
		t.Fatalf("checkpoint Get: %v", err)
	}
	if !found {
		t.Fatal("the paused run wrote no checkpoint, so there is nothing to assert about")
	}
	if bytes.Contains(raw, []byte("data:image")) {
		t.Error("the checkpoint holds a data URL: a paused run wrote image bytes into the checkpoint store")
	}
	if !bytes.Contains(raw, []byte(ref.AssetID)) {
		t.Errorf("the checkpoint names neither the asset (%s) nor its bytes", ref.AssetID)
	}
}
