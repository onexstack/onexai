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
	"slices"
	"testing"
)

// TestImageFormatsRoundTrip pins that the two directions of the filename
// mapping are inverses of one table rather than two lists that happen to agree
// today. A format added to one side and not the other is the bug this catches:
// a stored object whose extension its own allow-list would reject.
func TestImageFormatsRoundTrip(t *testing.T) {
	for _, f := range imageFormats {
		if got := ExtensionFor(f.Mime); got != f.Ext {
			t.Errorf("ExtensionFor(%q) = %q, want %q", f.Mime, got, f.Ext)
		}
		if got := mimeFromFilename("screenshot" + f.Ext); got != f.Mime {
			t.Errorf("mimeFromFilename(%q) = %q, want %q", "screenshot"+f.Ext, got, f.Mime)
		}
	}

	// The other spelling of jpeg maps to the same type; a phone or a browser
	// sends either and neither should be refused.
	if got := mimeFromFilename("photo.jpeg"); got != "image/jpeg" {
		t.Errorf("mimeFromFilename(.jpeg) = %q, want image/jpeg", got)
	}
	// A type with parameters is normalized before the lookup, as an upload's
	// declaration is.
	if got := ExtensionFor("image/JPEG; charset=binary"); got != ".jpg" {
		t.Errorf("ExtensionFor(a parameterized type) = %q, want .jpg", got)
	}
	// An unknown type and an unknown extension name nothing.
	if got := ExtensionFor("image/avif"); got != "" {
		t.Errorf("ExtensionFor(image/avif) = %q, want empty", got)
	}
	if got := mimeFromFilename("notes.avif"); got != "" {
		t.Errorf("mimeFromFilename(.avif) = %q, want empty", got)
	}
}

// TestDefaultKindsComeFromTheFormatTable pins that the per-kind allow-lists are
// derived from imageFormats rather than written out a second time, which is what
// makes adding a format a one-line change.
func TestDefaultKindsComeFromTheFormatTable(t *testing.T) {
	kinds := DefaultKinds()
	all := imageMimes()
	noGIF := imageMimes("image/gif")

	if !slices.Equal(kinds[KindImage].ContentTypes, all) {
		t.Errorf("KindImage accepts %v, want the whole table %v", kinds[KindImage].ContentTypes, all)
	}
	for _, kind := range []string{KindGenerated, KindAvatar} {
		if slices.Contains(kinds[kind].ContentTypes, "image/gif") {
			t.Errorf("%s accepts image/gif, which nothing here produces", kind)
		}
		if !slices.Equal(kinds[kind].ContentTypes, noGIF) {
			t.Errorf("%s accepts %v, want %v", kind, kinds[kind].ContentTypes, noGIF)
		}
	}
}
