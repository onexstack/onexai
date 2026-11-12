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
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// imageEngine records the messages it was handed, so a test can say what
// actually reached the model.
type imageEngine struct{ got []agentapi.Message }

func (*imageEngine) Name() string { return "image" }
func (e *imageEngine) Run(_ context.Context, msgs []agentapi.Message, _ agentapi.RunOptions) (<-chan agentapi.Event, error) {
	e.got = msgs
	out := make(chan agentapi.Event, 1)
	out <- agentapi.Event{Type: agentapi.EventDone, Final: "ok"}
	close(out)
	return out, nil
}

// testPNGBytes renders a small PNG. Real bytes matter: the store types an
// object by sniffing it, so a fixture of "png-ish" text would never be stored.
func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// newImageBiz builds a biz whose images resolve through a real local store.
func newImageBiz(t *testing.T, engine agentapi.Engine) (*AgentBiz, media.Ref, mem.SessionStore) {
	t.Helper()

	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	bundle := media.NewBundle(store, nil)

	ref, err := bundle.Put(context.Background(), media.KindImage, "ou_asker", "shot.png", testPNGBytes(t))
	if err != nil {
		t.Fatalf("store the test image: %v", err)
	}

	sessions := mem.NewSessionStore(nil, "test", 0)
	b := New(Deps{
		Engine:               engine,
		Sessions:             sessions,
		Stops:                runstop.NewLocal(),
		MaxConcurrentPerConv: 1,
		Images:               agentedge.New(agentedge.Deps{Bundle: bundle}),
	})
	return b, ref, sessions
}

// TestAskNeverPersistsInlineImages is the invariant the whole design exists for.
//
// The model needs bytes, so something has to turn a reference into a data URL.
// The store must never see that result: a session holding a data URL is a
// base64 payload Redis reads back in full on every following turn of that
// conversation, which is the cost this change was made to remove.
//
// The resolution therefore happens later than this layer — inside the model
// call, in imageResolveMiddleware, see TestImageResolveMiddlewareGivesTheModelBytes
// for that half. What is asserted here is that this layer hands the engine
// references and nothing else, and that the persisted history holds the same.
// Together the two tests say the bytes exist for exactly one model call.
func TestAskNeverPersistsInlineImages(t *testing.T) {
	engine := &imageEngine{}
	b, ref, sessions := newImageBiz(t, engine)

	events, err := b.Ask(context.Background(), &agentapi.AskRequest{
		SessionID: "oc_conv",
		Input:     agentapi.Input{Text: "描述这张图", Images: []media.Ref{ref}},
		Subject:   agentapi.Principal{ID: "ou_asker"},
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("Ask returned %v", err)
	}
	drain(t, events)

	// The engine was handed the reference, and no bytes: the resolver runs
	// inside the model call, not before the engine is invoked.
	if len(engine.got) == 0 {
		t.Fatal("the engine ran with no messages")
	}
	var sent int
	for _, m := range engine.got {
		for _, img := range m.Images {
			sent++
			if strings.HasPrefix(img.URL, "data:") {
				t.Fatalf("a data URL reached the engine: %q", img.URL[:min(len(img.URL), 40)])
			}
			if img.AssetID != ref.AssetID {
				t.Errorf("the engine received asset id %q, want %q", img.AssetID, ref.AssetID)
			}
		}
	}
	if sent == 0 {
		t.Fatal("the engine received no image at all")
	}

	// The history kept the reference.
	history, err := sessions.Load(context.Background(), "oc_conv")
	if err != nil {
		t.Fatalf("Load returned %v", err)
	}
	if len(history) == 0 {
		t.Fatal("nothing was persisted")
	}
	var persisted int
	for _, m := range history {
		for _, img := range m.Images {
			persisted++
			if strings.HasPrefix(img.URL, "data:") {
				t.Fatalf("the session history holds a data URL: %q", img.URL[:min(len(img.URL), 40)])
			}
			if img.AssetID != ref.AssetID {
				t.Errorf("persisted asset id = %q, want %q", img.AssetID, ref.AssetID)
			}
		}
	}
	if persisted == 0 {
		t.Fatal("the turn's image was not persisted at all")
	}
}

// TestAskWithoutStoreSkipsImages pins the degradation. A deployment with no
// media store cannot read an image, and the turn still runs: an engine that
// never sees the image is a worse answer than the user hoped for, and refusing
// to answer at all is worse than that.
func TestAskWithoutStoreSkipsImages(t *testing.T) {
	engine := &imageEngine{}
	b, ref, _ := newImageBiz(t, engine)
	// The inliner is what resolves references; without one nothing does.
	b.deps.Images = agentedge.New(agentedge.Deps{})

	events, err := b.Ask(context.Background(), &agentapi.AskRequest{
		SessionID: "oc_conv",
		Input:     agentapi.Input{Text: "描述这张图", Images: []media.Ref{ref}},
		Subject:   agentapi.Principal{ID: "ou_asker"},
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("Ask returned %v", err)
	}
	drain(t, events)

	for _, m := range engine.got {
		for _, img := range m.Images {
			// An unresolved reference has no URL the model could fetch, so the
			// engine's own guard skips it rather than sending an empty one.
			if _, ok := img.ModelReadyURL(); ok && !strings.HasPrefix(img.URL, "data:") {
				t.Fatalf("unexpected URL reached the engine: %q", img.URL)
			}
		}
	}
}
