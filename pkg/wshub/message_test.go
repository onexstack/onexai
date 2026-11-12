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

package wshub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/onexstack/onexai/pkg/media"
)

// TestSendPayloadCarriesImageReferences pins the downstream direction of the
// image contract: a client uploads through the asset endpoint and sends what it
// was given, so what crosses this frame is an identity rather than bytes.
//
// It is also the compatibility test. A client built before this field existed
// sends a payload with no images at all, and that must decode to an empty list
// rather than an error — an old TUI must not stop being able to type.
func TestSendPayloadCarriesImageReferences(t *testing.T) {
	t.Run("with images", func(t *testing.T) {
		raw := `{"content":"看看这个","images":[{"asset_id":"img_a","url":"/media/x","mime_type":"image/png"}],"client_msg_id":"c1"}`
		var p SendPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("Unmarshal returned %v", err)
		}
		if p.Content != "看看这个" || p.ClientMsgID != "c1" {
			t.Fatalf("the text fields were lost: %+v", p)
		}
		if len(p.Images) != 1 || p.Images[0].AssetID != "img_a" {
			t.Fatalf("Images = %+v, want one reference to img_a", p.Images)
		}
		if p.Images[0].MimeType != "image/png" {
			t.Errorf("MimeType = %q, want image/png", p.Images[0].MimeType)
		}
	})

	t.Run("without images", func(t *testing.T) {
		var p SendPayload
		if err := json.Unmarshal([]byte(`{"content":"只发文字"}`), &p); err != nil {
			t.Fatalf("an image-less payload failed to decode: %v", err)
		}
		if len(p.Images) != 0 {
			t.Errorf("Images = %+v, want none", p.Images)
		}
	})

	t.Run("an image-only message", func(t *testing.T) {
		// A screenshot with nothing typed beside it is a question in itself,
		// so the frame has to be valid with the text left empty.
		var p SendPayload
		if err := json.Unmarshal([]byte(`{"images":[{"asset_id":"img_a"}]}`), &p); err != nil {
			t.Fatalf("an image-only payload failed to decode: %v", err)
		}
		if p.Content != "" || len(p.Images) != 1 {
			t.Errorf("decoded %+v, want one image and no text", p)
		}
	})
}

// TestMessagePayloadRendersReferences pins the upstream direction: what a
// subscriber receives is a reference it can resolve, never a data URL.
func TestMessagePayloadRendersReferences(t *testing.T) {
	payload := MessagePayload{
		ID:          "m1",
		ConvID:      "oc_1",
		Content:     "",
		ContentType: "image",
		Images:      []media.Ref{{AssetID: "img_a", URL: "/media/images/u/img_a/a.png", MimeType: "image/png"}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal returned %v", err)
	}
	if strings.Contains(string(raw), "base64") {
		t.Fatalf("the payload carries bytes: %s", raw)
	}

	var back MessagePayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal returned %v", err)
	}
	if len(back.Images) != 1 || back.Images[0].AssetID != "img_a" {
		t.Fatalf("round trip lost the reference: %+v", back.Images)
	}
}
