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

package media

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// TestResizeShrinksToTheCeiling pins the arithmetic: the result fits the limit
// and keeps the shape.
func TestResizeShrinksToTheCeiling(t *testing.T) {
	big := solidPNG(t, 100, 50) // 5000 pixels
	got, contentType, err := Resize(big, 1000)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if contentType != "image/png" {
		t.Errorf("contentType = %q, want image/png", contentType)
	}
	w, h := ImageSize(got)
	if w*h > 1000 {
		t.Errorf("the result is %dx%d = %d pixels, over the 1000 asked for", w, h, w*h)
	}
	// The shape survives: 2:1 in, 2:1 out, within a rounding pixel.
	if w == 0 || h == 0 || w/h != 2 {
		t.Errorf("the shape changed: %dx%d from 100x50", w, h)
	}
	if len(got) >= len(big) && w*h >= 100*50 {
		t.Error("nothing was shrunk")
	}
}

// TestResizeLeavesWhatItShouldAlone pins the three no-op cases, which are the
// common ones: scaling is off, the image already fits, or the bytes cannot be
// decoded. Each returns the input slice itself, so nothing is re-encoded — a
// loss with no benefit.
func TestResizeLeavesWhatItShouldAlone(t *testing.T) {
	small := solidPNG(t, 10, 10) // 100 pixels

	for _, tt := range []struct {
		name      string
		data      []byte
		maxPixels int
	}{
		{name: "scaling off", data: solidPNG(t, 100, 100), maxPixels: 0},
		{name: "already inside the limit", data: small, maxPixels: 1000},
		{name: "exactly at the limit", data: small, maxPixels: 100},
		{name: "not an image", data: []byte("not an image at all"), maxPixels: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := Resize(tt.data, tt.maxPixels)
			if err != nil {
				t.Fatalf("Resize: %v", err)
			}
			if &got[0] != &tt.data[0] {
				t.Error("the input was copied even though nothing needed changing")
			}
		})
	}
}

// solidPNG renders a single-colour PNG of the given size.
func solidPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
