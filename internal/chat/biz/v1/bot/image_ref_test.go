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

package bot

import (
	"strings"
	"testing"

	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// TestRefFromAssetCarriesNoBytes is the property the chat side exists to
// uphold: what it keeps about an image is a reference, never the image.
//
// The backend is where bytes live. This service receives them from Lark, hands
// them over once, and from then on the turn carries what the backend called
// them — so a screenshot costs the conversation nothing for the rest of its
// life.
func TestRefFromAssetCarriesNoBytes(t *testing.T) {
	asset := &agentv1.Asset{
		AssetId:  "img_abc",
		Url:      "/media/images/ou_1/img_abc/shot.png",
		MimeType: "image/png",
		Size:     1234,
		Width:    800,
		Height:   600,
		Name:     "shot.png",
	}

	ref := refFromAsset(asset, "ignored.png")
	if ref.AssetID != "img_abc" {
		t.Errorf("AssetID = %q, want img_abc", ref.AssetID)
	}
	if ref.Size != 1234 || ref.Width != 800 || ref.Height != 600 {
		t.Errorf("metadata was dropped: %+v", ref)
	}
	if ref.Source != media.SourceLark {
		t.Errorf("Source = %q, want %q", ref.Source, media.SourceLark)
	}
	// The compact form the ask request carries must name the asset rather than
	// the bytes: a data URL here would be the whole problem back again, one
	// layer further along.
	if got := ref.String(); got != "asset:img_abc" {
		t.Errorf("String() = %q, want asset:img_abc", got)
	}
	if strings.Contains(ref.String(), "base64") {
		t.Errorf("the reference carries bytes: %q", ref.String())
	}

	// A backend that answers with nothing must not produce a reference that
	// names the empty key.
	if got := refFromAsset(nil, "x.png"); !got.IsZero() {
		t.Errorf("refFromAsset(nil) = %+v, want the zero reference", got)
	}
}

// TestRefFromAssetKeepsTheBackendsName pins that the stored name wins over the
// one Lark supplied: the backend sanitised it, and a second naming would let the
// two disagree about what the object is called.
func TestRefFromAssetKeepsTheBackendsName(t *testing.T) {
	ref := refFromAsset(&agentv1.Asset{AssetId: "img_a", Name: "sanitised.png"}, "original (1).png")
	if ref.Name != "sanitised.png" {
		t.Errorf("Name = %q, want the backend's name", ref.Name)
	}
	// With no name from the backend the caller's is the only one there is.
	ref = refFromAsset(&agentv1.Asset{AssetId: "img_a"}, "original.png")
	if ref.Name != "original.png" {
		t.Errorf("Name = %q, want the fallback", ref.Name)
	}
}
