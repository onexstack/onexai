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

import "strings"

// imageFormat is one media type this service accepts and the filename extension
// that stands for it.
type imageFormat struct {
	Mime string
	Ext  string
}

// imageFormats are the formats accepted anywhere in this service.
//
// The table is the single statement of the set. The extension lookup, the
// reverse lookup that reads a filename, and the per-kind allow-lists all read
// it, so a new format is added in one place rather than in three that are free
// to disagree — which is how a stored object ends up with an extension its own
// allow-list would reject.
var imageFormats = []imageFormat{
	{Mime: "image/png", Ext: ".png"},
	{Mime: "image/jpeg", Ext: ".jpg"},
	{Mime: "image/webp", Ext: ".webp"},
	{Mime: "image/gif", Ext: ".gif"},
}

// imageMimes returns the media types of every format except those named.
//
// A kind that accepts fewer formats says so as "the list, without gif" rather
// than as a second literal list, because that is what the kinds mean: the
// generated and avatar ceilings are the image list without the animated format,
// which nothing here produces.
func imageMimes(except ...string) []string {
	skip := make(map[string]bool, len(except))
	for _, m := range except {
		skip[m] = true
	}
	out := make([]string, 0, len(imageFormats))
	for _, f := range imageFormats {
		if !skip[f.Mime] {
			out = append(out, f.Mime)
		}
	}
	return out
}

// ExtensionFor returns a filename extension for a media type, or "".
//
// It exists for the store keys of objects that arrived without a name — a
// Lark image is addressed by a key, not a filename — where the extension is
// what makes the stored object recognisable when someone lists the directory.
func ExtensionFor(contentType string) string {
	ct := NormalizeContentType(contentType)
	for _, f := range imageFormats {
		if f.Mime == ct {
			return f.Ext
		}
	}
	return ""
}

// mimeFromFilename returns a media type for a filename extension, or "".
//
// It exists only as the opaque fallback in SniffImageType, so it covers the
// formats whose signatures Go does not know rather than every extension a
// browser might produce.
func mimeFromFilename(name string) string {
	ext := strings.ToLower(extOf(name))
	if ext == ".jpeg" {
		// The other spelling of the same format. A browser or a phone sends
		// either, and refusing one would turn an ordinary upload into a 400.
		ext = ".jpg"
	}
	for _, f := range imageFormats {
		if f.Ext == ext {
			return f.Mime
		}
	}
	return ""
}

// extOf returns the extension of a filename including its dot, or "".
func extOf(name string) string {
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[i:]
	}
	return ""
}
