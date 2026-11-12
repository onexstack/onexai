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
)

// TestParsePostContentKeepsWordsAndPictures is the regression test for the bug
// this change fixes: a rich-text post used to keep only its text, so a user who
// wrote a sentence and attached a screenshot had the sentence answered and the
// screenshot silently dropped.
func TestParsePostContentKeepsWordsAndPictures(t *testing.T) {
	t.Run("both", func(t *testing.T) {
		// The bare shape: {title, content}, content being lines of nodes.
		raw := `{"title":"","content":[[{"tag":"text","text":"这个报错怎么解决？"}],[{"tag":"img","image_key":"img_v2_abc"}]]}`
		mc := parsePostContent(raw)
		if mc == nil {
			t.Fatal("parsePostContent returned nil")
		}
		if !strings.Contains(mc.Text, "这个报错怎么解决？") {
			t.Errorf("Text = %q, want the question", mc.Text)
		}
		if len(mc.ImageKeys) != 1 || mc.ImageKeys[0] != "img_v2_abc" {
			t.Errorf("ImageKeys = %v, want the screenshot", mc.ImageKeys)
		}
	})

	t.Run("image only", func(t *testing.T) {
		// A picture with no words is still a message. Returning nil here would
		// drop it at the parser, before any of the funnel's own checks.
		raw := `{"title":"","content":[[{"tag":"img","image_key":"img_v2_only"}]]}`
		mc := parsePostContent(raw)
		if mc == nil {
			t.Fatal("a post with only a picture was dropped")
		}
		if len(mc.ImageKeys) != 1 {
			t.Errorf("ImageKeys = %v, want one", mc.ImageKeys)
		}
	})

	t.Run("language-wrapped", func(t *testing.T) {
		raw := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"看这个"}],[{"tag":"img","image_key":"img_v2_zh"}]]}}`
		mc := parsePostContent(raw)
		if mc == nil {
			t.Fatal("parsePostContent returned nil")
		}
		if len(mc.ImageKeys) != 1 || mc.ImageKeys[0] != "img_v2_zh" {
			t.Errorf("ImageKeys = %v, want the wrapped picture", mc.ImageKeys)
		}
	})

	t.Run("links and files are not pictures", func(t *testing.T) {
		// Only tag "img" carries an image_key a download could be asked for. A
		// link node has an href, and a media node an uploaded file; treating
		// either as an image would produce a download for a key that is not one.
		raw := `{"content":[[{"tag":"a","text":"详情","href":"https://x"},{"tag":"media","file_key":"file_v1"}]]}`
		mc := parsePostContent(raw)
		if mc == nil {
			t.Fatal("the text was dropped")
		}
		if len(mc.ImageKeys) != 0 {
			t.Errorf("ImageKeys = %v, want none", mc.ImageKeys)
		}
		if !strings.Contains(mc.Text, "详情") {
			t.Errorf("Text = %q, want the link text", mc.Text)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if mc := parsePostContent(`{"title":"","content":[]}`); mc != nil {
			t.Errorf("an empty post produced %+v, want nil", mc)
		}
	})
}
