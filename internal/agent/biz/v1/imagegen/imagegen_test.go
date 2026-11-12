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

package imagegen

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// fakeGenerator answers with a fixed number of images, and can be told to fail.
type fakeGenerator struct {
	images  []chatmodel.GeneratedImage
	err     error
	gotReqs []chatmodel.ImageRequest
}

func (g *fakeGenerator) Generate(_ context.Context, req chatmodel.ImageRequest) ([]chatmodel.GeneratedImage, error) {
	g.gotReqs = append(g.gotReqs, req)
	return g.images, g.err
}

// fakeAssets records what the generator stored, and which call failed.
//
// The failure is keyed by call number rather than by how many have succeeded:
// a failing call does not advance the stored count, so keying on that would
// make every call after the first failure fail too — which is a different
// scenario from the one the test is describing.
type fakeAssets struct {
	stored   []string
	content  map[string][]byte
	storeErr map[int]error
	calls    int
}

func newFakeAssets() *fakeAssets {
	return &fakeAssets{content: map[string][]byte{}, storeErr: map[int]error{}}
}

func (f *fakeAssets) Store(
	_ context.Context, _, _, _, _ string, data []byte,
) (media.Ref, error) {
	idx := f.calls
	f.calls++
	if err := f.storeErr[idx]; err != nil {
		return media.Ref{}, err
	}
	id := "img_" + string(rune('a'+idx))
	f.stored = append(f.stored, id)
	f.content[id] = data
	return media.Ref{AssetID: id, URL: "/media/" + id, MimeType: "image/png", Size: int64(len(data))}, nil
}

func (f *fakeAssets) Content(_ context.Context, id string) ([]byte, string, error) {
	data, ok := f.content[id]
	if !ok {
		return nil, "", errors.New("no such asset")
	}
	return data, "image/png", nil
}

func (f *fakeAssets) Configured() bool { return true }
func (f *fakeAssets) Create(context.Context, *v1.CreateAssetRequest) (*v1.Asset, error) {
	return nil, errors.New("unused")
}
func (f *fakeAssets) Get(context.Context, string) (*v1.Asset, error) {
	return nil, errors.New("unused")
}
func (f *fakeAssets) Delete(context.Context, string) error { return nil }

func drawn(n int) []chatmodel.GeneratedImage {
	out := make([]chatmodel.GeneratedImage, n)
	for i := range out {
		out[i] = chatmodel.GeneratedImage{Data: []byte{0x89, 'P', 'N', 'G', byte(i)}, MimeType: "image/png"}
	}
	return out
}

// TestGenerateStoresWhatItDrew is the package's reason to exist: a drawn image
// is stored like any other, so everything that reads an image keeps working on
// it.
func TestGenerateStoresWhatItDrew(t *testing.T) {
	gen := &fakeGenerator{images: drawn(2)}
	assets := newFakeAssets()
	b := New(gen, assets)

	out, err := b.Generate(context.Background(), &v1.ImageGenerationRequest{
		Prompt: "两只猫", SenderID: "go.kong", Model: "gpt-image-1", N: 2,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(out.GetData()) != 2 {
		t.Fatalf("got %d images, want 2", len(out.GetData()))
	}
	if len(assets.stored) != 2 {
		t.Errorf("the store holds %d images, want 2", len(assets.stored))
	}

	// Each one comes back as a usable reference, which is what a caller that
	// forwards the image rather than rendering it needs.
	for i, img := range out.GetData() {
		if img.GetAssetId() == "" || img.GetRef() == nil {
			t.Fatalf("image %d has no reference: %+v", i, img)
		}
		if img.GetRef().GetAssetId() != img.GetAssetId() {
			t.Errorf("image %d: the ref names %q but the asset is %q",
				i, img.GetRef().GetAssetId(), img.GetAssetId())
		}
		if !strings.HasPrefix(img.GetUrl(), "/media/") {
			t.Errorf("image %d: url = %q, want this service's own", i, img.GetUrl())
		}
	}

	// The request reached the provider with the caller's own parameters.
	got := gen.gotReqs[0]
	if got.Prompt != "两只猫" || got.Model != "gpt-image-1" || got.N != 2 {
		t.Errorf("the provider was asked for %+v", got)
	}
}

// TestGenerateAsksForInlineBytesOnlyWhenRequested pins the OpenAI-compatible
// spelling. The bytes are read back from the store rather than kept from the
// provider, so the two answers cannot disagree.
func TestGenerateAsksForInlineBytesOnlyWhenRequested(t *testing.T) {
	assets := newFakeAssets()
	b := New(&fakeGenerator{images: drawn(1)}, assets)
	ctx := context.Background()

	out, err := b.Generate(ctx, &v1.ImageGenerationRequest{Prompt: "猫", SenderID: "u"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out.GetData()[0].GetB64Json() != "" {
		t.Error("bytes were returned without being asked for")
	}

	for _, format := range []string{"b64_json", "base64"} {
		out, err := b.Generate(ctx, &v1.ImageGenerationRequest{
			Prompt: "猫", SenderID: "u", ResponseFormat: format,
		})
		if err != nil {
			t.Fatalf("Generate(%s): %v", format, err)
		}
		raw := out.GetData()[0].GetB64Json()
		if raw == "" {
			t.Fatalf("format %q returned no inline bytes", format)
		}
		if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
			t.Errorf("format %q returned something that is not base64: %v", format, err)
		}
	}
}

// TestGenerateKeepsWhatItCouldStore pins the partial failure.
//
// A batch that stored two of three is reported as two. Refusing every image
// because one could not be kept throws away work already paid for at the
// provider, and the alternative — reporting three when two exist — hands the
// caller a reference to nothing.
func TestGenerateKeepsWhatItCouldStore(t *testing.T) {
	assets := newFakeAssets()
	assets.storeErr[1] = errors.New("disk full")
	b := New(&fakeGenerator{images: drawn(3)}, assets)

	out, err := b.Generate(context.Background(), &v1.ImageGenerationRequest{Prompt: "猫", SenderID: "u"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(out.GetData()) != 2 {
		t.Fatalf("got %d images, want the 2 that were stored", len(out.GetData()))
	}
}

// TestGenerateFailsWhenNothingCouldBeStored pins the other end: a batch where
// every store failed is a failure, not an empty success.
func TestGenerateFailsWhenNothingCouldBeStored(t *testing.T) {
	assets := newFakeAssets()
	for i := range 2 {
		assets.storeErr[i] = errors.New("disk full")
	}
	b := New(&fakeGenerator{images: drawn(2)}, assets)

	if _, err := b.Generate(context.Background(), &v1.ImageGenerationRequest{Prompt: "猫", SenderID: "u"}); err == nil {
		t.Fatal("Generate reported success with nothing stored")
	}
}

// TestGeneratePropagatesAProviderFailure pins that the provider's own error
// reaches the caller: whether a refusal was a bad prompt, a missing model or a
// quota is only in their message.
func TestGeneratePropagatesAProviderFailure(t *testing.T) {
	b := New(&fakeGenerator{err: errors.New("content policy violation")}, newFakeAssets())

	_, err := b.Generate(context.Background(), &v1.ImageGenerationRequest{Prompt: "x", SenderID: "u"})
	if err == nil || !strings.Contains(err.Error(), "content policy violation") {
		t.Errorf("err = %v, want the provider's message", err)
	}
}

// TestUnconfiguredRefuses pins the deployment with no image model: it answers
// every other request and refuses a drawing with a message naming the missing
// configuration.
func TestUnconfiguredRefuses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		biz   *imageGenBiz
		label string
	}{
		{"no generator", New(nil, newFakeAssets()), "generator"},
		{"no store", New(&fakeGenerator{}, nil), "store"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.biz.Configured() {
				t.Fatal("reports itself configured without a " + tt.label)
			}
			_, err := tt.biz.Generate(context.Background(), &v1.ImageGenerationRequest{Prompt: "x"})
			if !errors.Is(err, ErrNotConfigured) {
				t.Errorf("err = %v, want ErrNotConfigured", err)
			}
		})
	}
	if (*imageGenBiz)(nil).Configured() {
		t.Error("a nil biz reports itself configured")
	}
}
