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
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/media"
)

// recordingModel records the messages a call was given.
type recordingModel struct{ got []*schema.Message }

func (m *recordingModel) Generate(
	_ context.Context, in []*schema.Message, _ ...einomodel.Option,
) (*schema.Message, error) {
	m.got = in
	return schema.AssistantMessage("ok", nil), nil
}

func (m *recordingModel) Stream(
	_ context.Context, in []*schema.Message, _ ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.got = in
	return nil, errors.New("stream is not used by this test")
}

// stubRows answers an asset lookup with one fixed row, so a reference that
// names an asset id — which is what every reference from the chat service is —
// resolves to the object key it stands for.
type stubRows struct {
	agentstore.AssetStore
	row *model.AssetM
}

func (s *stubRows) Get(_ context.Context, opts *where.Options) (*model.AssetM, error) {
	id, _ := opts.Filters["id"].(string)
	if s.row != nil && s.row.ID == id {
		return s.row, nil
	}
	return nil, agentstore.ErrNotFound
}

// newTestInliner builds an inliner over a real local store holding one image,
// with the row that lets its asset id be resolved.
func newTestInliner(t *testing.T) (*agentedge.Inliner, media.Ref) {
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
	rows := &stubRows{row: &model.AssetM{ID: ref.AssetID, ObjectKey: ref.ObjectKey}}
	return agentedge.New(agentedge.Deps{Bundle: bundle, Rows: rows}), ref
}

// imageMessage builds a user message whose one image part names a reference, as
// toUserSchemaMessage produces it.
func imageMessage(ref media.Ref) *schema.Message {
	url := ref.String()
	message := schema.UserMessage("描述这张图")
	message.UserInputMultiContent = []schema.MessageInputPart{{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
	}}
	return message
}

// TestImageResolveMiddlewareGivesTheModelBytes is the other half of
// TestAskNeverPersistsInlineImages: the engine is handed references, and the
// wrapper turns them into bytes inside the model call — where nothing it
// produces is persisted.
//
// It also pins the copy: the message it was handed belongs to the agent's state,
// which is checkpointed, so writing the data URL through the shared pointer
// would put the bytes back into the one place this design keeps them out of.
func TestImageResolveMiddlewareGivesTheModelBytes(t *testing.T) {
	inliner, ref := newTestInliner(t)
	rec := &recordingModel{}

	wrapped, err := newImageResolveMiddleware().WrapModel(
		agentedge.WithInliner(context.Background(), inliner), rec, nil)
	if err != nil {
		t.Fatalf("WrapModel returned %v", err)
	}

	message := imageMessage(ref)
	if _, err := wrapped.Generate(context.Background(), []*schema.Message{message}); err != nil {
		t.Fatalf("Generate returned %v", err)
	}

	if len(rec.got) != 1 || len(rec.got[0].UserInputMultiContent) != 1 {
		t.Fatalf("the model was given %d messages, want one with one part", len(rec.got))
	}
	got := rec.got[0].UserInputMultiContent[0]
	url, ok := imagePartURLForTest(got)
	if !ok {
		t.Fatal("the model was given no image part")
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("the model was given %q, want a png data URL", url)
	}

	// The caller's message still names the reference: the wrapper copied rather
	// than wrote through the pointer it shares with the agent's state.
	if original := *message.UserInputMultiContent[0].Image.URL; original != ref.String() {
		t.Errorf("the input message was rewritten to %q", original)
	}
}

// TestImageResolveMiddlewareLeavesModelReadyURLsAlone pins that a URL the model
// can already fetch is passed through. Resolving one would turn a remote image
// the provider is meant to fetch into one this service fetches — or drops it,
// when no fetcher is configured.
func TestImageResolveMiddlewareLeavesModelReadyURLsAlone(t *testing.T) {
	rec := &recordingModel{}
	inliner := agentedge.New(agentedge.Deps{}) // nothing configured: never resolves
	wrapped, err := newImageResolveMiddleware().WrapModel(
		agentedge.WithInliner(context.Background(), inliner), rec, nil)
	if err != nil {
		t.Fatalf("WrapModel returned %v", err)
	}

	remote := "https://example.com/a.png"
	message := schema.UserMessage("看图")
	message.UserInputMultiContent = []schema.MessageInputPart{{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &remote}},
	}}
	if _, err := wrapped.Generate(context.Background(), []*schema.Message{message}); err != nil {
		t.Fatalf("Generate returned %v", err)
	}
	url, ok := imagePartURLForTest(rec.got[0].UserInputMultiContent[0])
	if !ok || url != remote {
		t.Errorf("the model was given %q, want the original %q", url, remote)
	}
}

// TestImageResolveMiddlewareWithoutInlinerIsIdentity pins the disabled case: no
// store and no fetcher means nothing to resolve with, and the model is called
// through unchanged rather than wrapped in a no-op.
func TestImageResolveMiddlewareWithoutInlinerIsIdentity(t *testing.T) {
	rec := &recordingModel{}
	wrapped, err := newImageResolveMiddleware().WrapModel(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("WrapModel returned %v", err)
	}
	if wrapped != einomodel.BaseChatModel(rec) {
		t.Error("WrapModel wrapped the model with no resolver to wrap it with")
	}
}

// imagePartURLForTest reads an image part's URL, reporting whether it is one.
func imagePartURLForTest(part schema.MessageInputPart) (string, bool) {
	if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.URL == nil {
		return "", false
	}
	return *part.Image.URL, true
}
