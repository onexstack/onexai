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
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

// testPNG renders a width×height PNG. Real bytes matter here: the type is
// sniffed rather than declared, so a test fixture of "png-ish" text would not
// exercise the path it is testing.
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

func TestBuildObjectKeyRejectsTraversal(t *testing.T) {
	kind := Kinds{Prefix: "images"}

	tests := []struct {
		name             string
		owner, asset, fn string
		wantErr          bool
	}{
		{name: "ordinary", owner: "ou_abc", asset: "img_1", fn: "photo.png"},
		{name: "dots are fine inside a segment", owner: "go.kong", asset: "img_1", fn: "photo.png"},
		{name: "owner escapes", owner: "..", asset: "img_1", fn: "a.png", wantErr: true},
		{name: "owner has a slash", owner: "a/b", asset: "img_1", fn: "a.png", wantErr: true},
		{name: "asset escapes", owner: "ou_abc", asset: "..", fn: "a.png", wantErr: true},
		{name: "asset empty", owner: "ou_abc", asset: "", fn: "a.png", wantErr: true},
		{name: "owner empty", owner: "", asset: "img_1", fn: "a.png", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := BuildObjectKey(kind, tt.owner, tt.asset, tt.fn)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidObjectKey) {
					t.Fatalf("BuildObjectKey(%q,%q,%q) = %q, %v; want ErrInvalidObjectKey",
						tt.owner, tt.asset, tt.fn, key, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildObjectKey returned %v", err)
			}
			if !strings.HasPrefix(key, "images/") {
				t.Errorf("key %q does not carry the kind prefix", key)
			}
			// The asset id is a directory of its own, which is what keeps two
			// uploads of the same filename from overwriting each other.
			if !strings.Contains(key, "/"+tt.asset+"/") {
				t.Errorf("key %q has no per-asset directory", key)
			}
		})
	}
}

func TestSanitiseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"photo.png", "photo.png"},
		{"IMG_0001 (1).jpg", "IMG_0001_1_.jpg"},
		{"my photo.png", "my_photo.png"},
		// Every multi-byte rune and the space become one underscore run: the
		// name is decoration, and the asset id above it is the identity.
		{"屏幕截图 2026-10-03.png", "_2026-10-03.png"},
		{"/tmp/photo.png", "photo.png"},
		{"../../etc/passwd", "passwd"},
		{"..", "file"},
		{".", "file"},
		{"", "file"},
		{".png", "png"},
		{strings.Repeat("a", 200) + ".png", strings.Repeat("a", maxNameBytes)},
	}
	for _, tt := range tests {
		got := sanitiseName(tt.in)
		if got != tt.want {
			t.Errorf("sanitiseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.Contains(got, "/") || got == ".." || got == "." {
			t.Errorf("sanitiseName(%q) = %q, which is not a safe single segment", tt.in, got)
		}
	}
}

func TestKindsValidate(t *testing.T) {
	rules := Kinds{
		MaxBytes:     100,
		ContentTypes: []string{"image/png", "image/jpeg"},
		Prefix:       "images",
	}

	tests := []struct {
		name    string
		ct      string
		size    int64
		wantErr error
	}{
		{name: "accepted", ct: "image/png", size: 10},
		{name: "parameters are ignored", ct: "image/JPEG; charset=binary", size: 10},
		{name: "wrong type", ct: "text/html", size: 10, wantErr: ErrUnsupportedType},
		{name: "no type", ct: "", size: 10, wantErr: ErrUnsupportedType},
		{name: "over the ceiling", ct: "image/png", size: 101, wantErr: ErrTooLarge},
		{name: "negative size", ct: "image/png", size: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rules.Validate(tt.ct, tt.size)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Validate(%q,%d) = %v, want %v", tt.ct, tt.size, err, tt.wantErr)
				}
			case tt.name == "negative size":
				if err == nil {
					t.Fatal("Validate accepted a negative size")
				}
			default:
				if err != nil {
					t.Fatalf("Validate(%q,%d) returned %v", tt.ct, tt.size, err)
				}
			}
		})
	}
}

// TestSniffImageTypePrefersBytes pins the security property: a file whose bytes
// say one thing and whose name says another is typed by its bytes.
func TestSniffImageTypePrefersBytes(t *testing.T) {
	pngBytes := testPNG(t, 2, 2)

	if got := SniffImageType(pngBytes, "text/html"); got != "image/png" {
		t.Errorf("SniffImageType sniffed %q, want image/png (the bytes must win over the declaration)", got)
	}

	// A text file named .png is called what it is. This is the case the
	// declaration-as-equal design would have got wrong.
	if got := SniffImageType([]byte("<html>not an image</html>"), "image/png"); got != "text/html" {
		t.Errorf("SniffImageType sniffed %q, want text/html", got)
	}

	// TIFF is a real image format Go has no signature for, so its bytes sniff
	// as octet-stream and the declared type is the only answer available.
	// Falling back here is what keeps a real image from being refused.
	tiff := []byte("II*\x00\x08\x00\x00\x00")
	if got := SniffImageType(tiff, "image/tiff"); got != "image/tiff" {
		t.Errorf("SniffImageType fell back to %q, want image/tiff", got)
	}
	// With nothing declared either, the octet-stream answer is handed back for
	// the kind's allow-list to reject by name.
	if got := SniffImageType(tiff, ""); got != "application/octet-stream" {
		t.Errorf("SniffImageType with no declaration = %q, want application/octet-stream", got)
	}
}

func TestImageSize(t *testing.T) {
	w, h := ImageSize(testPNG(t, 7, 11))
	if w != 7 || h != 11 {
		t.Errorf("ImageSize = %dx%d, want 7x11", w, h)
	}
	// Dimensions are metadata, not a gate: an unreadable header is not an error.
	if w, h := ImageSize([]byte("nonsense")); w != 0 || h != 0 {
		t.Errorf("ImageSize of nonsense = %dx%d, want 0x0", w, h)
	}
}
