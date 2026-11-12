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
	"encoding/json"
	"testing"
)

func TestParseRefForms(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgo="

	tests := []struct {
		name    string
		in      string
		want    Ref
		wantErr bool
	}{
		{
			name: "asset scheme",
			in:   "asset:img_abc123",
			want: Ref{AssetID: "img_abc123"},
		},
		{
			name: "object scheme",
			in:   "media:images/u1/img_abc/a.png",
			want: Ref{ObjectKey: "images/u1/img_abc/a.png"},
		},
		{
			// The type is read out of the data URL rather than left empty: a
			// consumer that has only this string still knows what it holds.
			name: "legacy data URL",
			in:   png,
			want: Ref{URL: png, MimeType: "image/png", Source: SourceUpload},
		},
		{
			name: "absolute https URL",
			in:   "https://cdn.example.com/a.png",
			want: Ref{URL: "https://cdn.example.com/a.png", Source: SourceURL},
		},
		{
			// A relative path is this service's own static route, not a URL
			// someone else's host serves.
			name: "relative public path",
			in:   "/media/images/u1/img_abc/a.png",
			want: Ref{URL: "/media/images/u1/img_abc/a.png"},
		},
		{name: "empty", in: "", wantErr: true},
		{name: "bare word", in: "not-a-reference", wantErr: true},
		{name: "unsupported scheme", in: "ftp://example.com/a.png", wantErr: true},
		{name: "asset with no id", in: "asset:", wantErr: true},
		{name: "object with no key", in: "media:", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) returned %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseManyFailsOnOne pins the decision that a malformed entry fails the
// whole list. Skipping it would answer a question about three images while
// having seen two, and nothing downstream could tell that apart from a request
// that only mentioned two.
func TestParseManyFailsOnOne(t *testing.T) {
	if _, err := ParseMany([]string{"asset:img_ok", "garbage"}); err == nil {
		t.Fatal("ParseMany accepted a list with an unparseable entry")
	}
	refs, err := ParseMany([]string{"asset:img_a", "asset:img_b"})
	if err != nil {
		t.Fatalf("ParseMany returned %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("ParseMany returned %d refs, want 2", len(refs))
	}
	if empty, err := ParseMany(nil); err != nil || empty != nil {
		t.Errorf("ParseMany(nil) = %v, %v; want nil, nil", empty, err)
	}
}

// TestRefStringPrefersStrongestIdentity pins the precedence: an asset id is
// what authorisation is keyed by, so it must survive the round trip through the
// compact form even when a URL is also present.
func TestRefStringPrefersStrongestIdentity(t *testing.T) {
	tests := []struct {
		name string
		ref  Ref
		want string
	}{
		{"asset wins over url", Ref{AssetID: "img_a", URL: "/media/x"}, "asset:img_a"},
		{"key wins over url", Ref{ObjectKey: "images/a", URL: "/media/x"}, "media:images/a"},
		{"url alone", Ref{URL: "https://x/a.png"}, "https://x/a.png"},
		{"zero", Ref{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}

	// The compact form is a reference, not a copy: parsing it back yields the
	// identity it named.
	back, err := Parse((Ref{AssetID: "img_a", URL: "/media/x"}).String())
	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if back.AssetID != "img_a" {
		t.Errorf("round trip lost the asset id: %+v", back)
	}
}

// TestRefJSONAcceptsLegacyString is the migration test. Sessions already in
// Redis and chat rows already in the database hold bare strings; if this stops
// working, every conversation that has ever carried an image stops loading.
func TestRefJSONAcceptsLegacyString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Ref
	}{
		{"legacy data URL", `"data:image/png;base64,AAAA"`, Ref{URL: "data:image/png;base64,AAAA", MimeType: "image/png", Source: SourceUpload}},
		{"legacy http URL", `"https://cdn.example.com/a.png"`, Ref{URL: "https://cdn.example.com/a.png", Source: SourceURL}},
		{"legacy asset string", `"asset:img_a"`, Ref{AssetID: "img_a"}},
		{"legacy empty string", `""`, Ref{}},
		{"null", `null`, Ref{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Ref
			if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) returned %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Unmarshal(%s)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestRefJSONWritesObjectForm pins the asymmetry that drains the old shape
// away: reading tolerates a bare string, writing never produces one.
func TestRefJSONWritesObjectForm(t *testing.T) {
	raw, err := json.Marshal(Ref{AssetID: "img_a", MimeType: "image/png", Width: 10, Height: 20})
	if err != nil {
		t.Fatalf("Marshal returned %v", err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("Marshal produced %s, want an object", raw)
	}

	var back Ref
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal returned %v", err)
	}
	if back.AssetID != "img_a" || back.Width != 10 || back.Height != 20 {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestDataURLMime(t *testing.T) {
	tests := []struct{ in, want string }{
		{"data:image/png;base64,AAAA", "image/png"},
		{"data:IMAGE/JPEG;base64,AAAA", "image/jpeg"},
		{"data:image/webp;charset=binary;base64,AAAA", "image/webp"},
		{"data:,AAAA", ""},
	}
	for _, tt := range tests {
		if got := dataURLMime(tt.in); got != tt.want {
			t.Errorf("dataURLMime(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDecodeDataURL(t *testing.T) {
	// "hi" in base64, in both the standard and the URL-safe alphabet.
	for _, in := range []string{"data:image/png;base64,aGk=", "data:image/png;base64,aGk"} {
		got, err := DecodeDataURL(in)
		if err != nil {
			t.Fatalf("DecodeDataURL(%q) returned %v", in, err)
		}
		if string(got) != "hi" {
			t.Errorf("DecodeDataURL(%q) = %q, want %q", in, got, "hi")
		}
	}
	if _, err := DecodeDataURL("data:image/png,plain-text"); err == nil {
		t.Error("DecodeDataURL accepted a non-base64 payload")
	}
}
