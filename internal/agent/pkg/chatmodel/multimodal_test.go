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

package chatmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestOpenAIContentMultimodal(t *testing.T) {
	img := "data:image/png;base64,AAA"

	t.Run("text only", func(t *testing.T) {
		in := schema.UserMessage("hi")
		if got := openAIContent(in); got != "hi" {
			t.Fatalf("openAIContent() = %v, want %q", got, "hi")
		}
	})

	t.Run("with image", func(t *testing.T) {
		in := schema.UserMessage("look")
		in.UserInputMultiContent = []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "look"},
			{
				Type:  schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &img}},
			},
		}

		raw, err := json.Marshal(openAIContent(in))
		if err != nil {
			t.Fatalf("marshal content: %v", err)
		}
		var parts []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			t.Fatalf("unmarshal content parts: %v", err)
		}
		var urls []string
		for _, p := range parts {
			if p.Type == "image_url" {
				urls = append(urls, p.ImageURL.URL)
			}
		}
		if len(urls) != 1 || urls[0] != img {
			t.Fatalf("image urls = %v, want [%s]", urls, img)
		}
	})
}

// TestOpenAIContentKeepsExtraTextParts pins the shape the vision-extraction
// middleware produces: the images are gone and the description arrived as a
// text part.
//
// It used to be dropped — the array was built from Content and the image parts
// alone — so the primary model was given the user's question and nothing about
// the picture, and answered "I did not receive any image" while the description
// sat in the message it was handed. A text part equal to Content is the mirror
// toUserSchemaMessage writes and must not be doubled.
func TestOpenAIContentKeepsExtraTextParts(t *testing.T) {
	in := schema.UserMessage("这张图是什么颜色？")
	in.UserInputMultiContent = []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: in.Content},
		{Type: schema.ChatMessagePartTypeText, Text: "[图片内容：红色]"},
	}

	got, ok := openAIContent(in).(string)
	if !ok {
		t.Fatalf("openAIContent() = %T, want a string when no image survives", openAIContent(in))
	}
	want := "这张图是什么颜色？\n\n[图片内容：红色]"
	if got != want {
		t.Fatalf("openAIContent() = %q, want %q", got, want)
	}
	if n := strings.Count(got, in.Content); n != 1 {
		t.Fatalf("the user's own text appears %d times, want 1: %q", n, got)
	}
}
