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

	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// fakeAssets stands in for the media service. It records what it was asked to
// store, so a test can say what a migration actually moved.
type fakeAssets struct {
	stored  [][]byte
	sources []string
	err     error
}

func (f *fakeAssets) Create(context.Context, *v1.CreateAssetRequest) (*v1.Asset, error) {
	return nil, errors.New("unused")
}

func (f *fakeAssets) Store(
	_ context.Context, _, _, _, source string, data []byte,
) (media.Ref, error) {
	if f.err != nil {
		return media.Ref{}, f.err
	}
	f.stored = append(f.stored, data)
	f.sources = append(f.sources, source)
	return media.Ref{
		AssetID:  "img_migrated",
		URL:      "/media/images/u/img_migrated/a.png",
		MimeType: "image/png",
		Source:   media.SourceUpload,
	}, nil
}

func (f *fakeAssets) Get(context.Context, string) (*v1.Asset, error) {
	return nil, errors.New("unused")
}
func (f *fakeAssets) Content(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("unused")
}
func (f *fakeAssets) Delete(context.Context, string) error { return nil }
func (f *fakeAssets) Configured() bool                     { return true }

// The fake implements the whole port, so the compiler keeps it honest as the
// port changes.
var _ asset.AssetBiz = (*fakeAssets)(nil)

// newMigratingBiz builds a biz whose only moving part is the migration.
func newMigratingBiz(t *testing.T, assets *fakeAssets) (*AgentBiz, mem.SessionStore) {
	t.Helper()
	sessions := mem.NewSessionStore(nil, "test", 0)
	b := New(Deps{
		Engine:               &imageEngine{},
		Sessions:             sessions,
		Stops:                runstop.NewLocal(),
		MaxConcurrentPerConv: 1,
		Assets:               assets,
	})
	return b, sessions
}

// legacyHistory is what a session written before images were references holds:
// the bytes themselves, inline.
func legacyHistory() []agentapi.Message {
	return []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "这张图", Images: []media.Ref{
			{URL: "data:image/png;base64,iVBORw0KGgo=", MimeType: "image/png"},
		}},
		{Role: agentapi.RoleAssistant, Content: "看起来是蓝色"},
	}
}

// TestMigrateMovesLegacyImagesIntoTheStore is the whole point of the migration:
// a session that predates references holds base64 that the store re-reads on
// every turn of that conversation, and loading it is the only moment anything
// sees it again.
func TestMigrateMovesLegacyImagesIntoTheStore(t *testing.T) {
	assets := &fakeAssets{}
	b, sessions := newMigratingBiz(t, assets)
	ctx := context.Background()

	history := legacyHistory()
	if err := sessions.Save(ctx, "oc_1", history); err != nil {
		t.Fatalf("seed the session: %v", err)
	}

	req := &agentapi.AskRequest{SessionID: "oc_1", Input: agentapi.Input{Text: "再描述一次"}, Subject: agentapi.Principal{ID: "ou_a"}}
	out := b.migrateInlineImages(ctx, req, history)

	if len(assets.stored) != 1 {
		t.Fatalf("the image was not stored: %+v", assets.stored)
	}
	got := out[0].Images[0]
	if strings.HasPrefix(got.URL, "data:") {
		t.Errorf("the reference still carries bytes: %q", got.URL[:min(len(got.URL), 32)])
	}
	if got.AssetID != "img_migrated" {
		t.Errorf("the reference was not replaced: %+v", got)
	}
	if out[1].Content != "看起来是蓝色" {
		t.Errorf("the text was disturbed: %+v", out[1])
	}

	// And it was written back, which is what stops the next turn repeating the
	// work.
	saved, err := sessions.Load(ctx, "oc_1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(saved[0].Images) != 1 || strings.HasPrefix(saved[0].Images[0].URL, "data:") {
		t.Errorf("the migrated history was not persisted: %+v", saved[0].Images)
	}
}

// TestMigrateLeavesACleanHistoryAlone pins that a session already holding
// references is not written back. Saving on every turn would rewrite a
// conversation's whole history on every message it receives.
func TestMigrateLeavesACleanHistoryAlone(t *testing.T) {
	assets := &fakeAssets{}
	b, sessions := newMigratingBiz(t, assets)
	ctx := context.Background()

	clean := []agentapi.Message{{Role: agentapi.RoleUser, Content: "看", Images: []media.Ref{
		{AssetID: "img_a", URL: "/media/images/u/img_a/a.png"},
	}}}
	if err := sessions.Save(ctx, "oc_1", clean); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := &agentapi.AskRequest{SessionID: "oc_1", Subject: agentapi.Principal{ID: "ou_a"}}
	out := b.migrateInlineImages(ctx, req, clean)

	if len(assets.stored) != 0 {
		t.Errorf("a reference was stored again: %+v", assets.stored)
	}
	if out[0].Images[0].AssetID != "img_a" {
		t.Errorf("the reference was altered: %+v", out[0].Images)
	}
}

// TestMigrateKeepsTheImageWhenStorageFails pins the degradation.
//
// Dropping the reference would turn a storage problem into a conversation that
// quietly loses its pictures — and the turn can still be answered from the data
// URL, which is what every turn of that session did before this code existed.
func TestMigrateKeepsTheImageWhenStorageFails(t *testing.T) {
	assets := &fakeAssets{err: errors.New("store is down")}
	b, _ := newMigratingBiz(t, assets)
	ctx := context.Background()

	history := legacyHistory()
	req := &agentapi.AskRequest{SessionID: "oc_1", Subject: agentapi.Principal{ID: "ou_a"}}
	out := b.migrateInlineImages(ctx, req, history)

	if len(out[0].Images) != 1 {
		t.Fatalf("the image was dropped when the store failed: %+v", out[0].Images)
	}
	if !strings.HasPrefix(out[0].Images[0].URL, "data:") {
		t.Errorf("the reference was changed to something unresolvable: %+v", out[0].Images[0])
	}
}

// TestMigrateWithoutAStoreIsANoOp pins the disabled deployment: nothing is
// configured, so nothing is moved and the turn still runs.
func TestMigrateWithoutAStoreIsANoOp(t *testing.T) {
	sessions := mem.NewSessionStore(nil, "test", 0)
	b := New(Deps{
		Engine:               &imageEngine{},
		Sessions:             sessions,
		Stops:                runstop.NewLocal(),
		MaxConcurrentPerConv: 1,
	})
	ctx := context.Background()

	history := legacyHistory()
	req := &agentapi.AskRequest{SessionID: "oc_1", Subject: agentapi.Principal{ID: "ou_a"}}
	out := b.migrateInlineImages(ctx, req, history)

	if !strings.HasPrefix(out[0].Images[0].URL, "data:") {
		t.Errorf("a migration ran with no store configured: %+v", out[0].Images)
	}
}
