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

package agentapi

import (
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// fullRef carries a non-zero value in every field, so a round trip that loses
// one is caught rather than hidden behind a zero that happens to match.
func fullRef() media.Ref {
	return media.Ref{
		AssetID:   "img_abc",
		ObjectKey: "images/u/img_abc/shot.png",
		URL:       "/media/images/u/img_abc/shot.png",
		MimeType:  "image/png",
		Size:      1234,
		Width:     640,
		Height:    480,
		Name:      "shot.png",
		Source:    "upload",
	}
}

// TestImageRefRoundTrip pins that the two directions are inverses, which is the
// property that makes it safe to have one pair of functions instead of a
// hand-written mapping at every call site.
func TestImageRefRoundTrip(t *testing.T) {
	want := fullRef()
	got, ok := RefFromProto(RefToProto(want))
	if !ok {
		t.Fatal("RefFromProto(RefToProto(r)) reported the reference as empty")
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestRefFromProtoRefusesAnEmptyReference pins the bool every caller relies on:
// an entry that names nothing is a caller bug, and the call sites decide what to
// do about it, so this must not silently answer a zero value.
func TestRefFromProtoRefusesAnEmptyReference(t *testing.T) {
	if _, ok := RefFromProto(nil); ok {
		t.Error("RefFromProto(nil) reported a reference")
	}
	if _, ok := RefFromProto(&v1.ImageRef{}); ok {
		t.Error("RefFromProto(empty) reported a reference")
	}
}

// TestRefsToProtoIsNilForEmpty pins that an absent list stays absent rather than
// becoming an empty list on the wire.
func TestRefsToProtoIsNilForEmpty(t *testing.T) {
	if got := RefsToProto(nil); got != nil {
		t.Errorf("RefsToProto(nil) = %v, want nil", got)
	}
	refs := []media.Ref{fullRef()}
	got := RefsToProto(refs)
	if len(got) != 1 {
		t.Fatalf("RefsToProto(%d refs) returned %d", len(refs), len(got))
	}
	if got[0].GetAssetId() != "img_abc" {
		t.Errorf("RefsToProto lost the asset id: %v", got[0])
	}
}
