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

package v1

import (
	"encoding/json"
	"testing"
)

// TestChatCompletionMessageContentShapes is the interop test for the relaxed
// field: a stock OpenAI SDK sends the array form as soon as an image is
// attached, and against a plain string that request used to fail the bind
// entirely.
func TestChatCompletionMessageContentShapes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantText   string
		wantImages []string
		wantErr    bool
	}{
		{
			name:     "the contract's own string",
			body:     `{"role":"user","content":"你好"}`,
			wantText: "你好",
		},
		{
			name:     "an empty content",
			body:     `{"role":"user"}`,
			wantText: "",
		},
		{
			name:     "the native array with text and an image",
			body:     `{"role":"user","content":[{"type":"text","text":"这是什么"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]}`,
			wantText: "这是什么",
			wantImages: []string{
				"https://x/a.png",
			},
		},
		{
			name:     "an array with several text parts",
			body:     `{"role":"user","content":[{"type":"text","text":"第一句"},{"type":"text","text":"第二句"}]}`,
			wantText: "第一句\n第二句",
		},
		{
			// A part type this service does not use must not fail the request:
			// the schema has grown fields over time, and a caller's slightly
			// newer SDK should still deliver what this service can read.
			name:     "an unknown part type is skipped",
			body:     `{"role":"user","content":[{"type":"text","text":"看这个"},{"type":"input_audio","input_audio":{"data":"xxx"}}]}`,
			wantText: "看这个",
		},
		{
			// Both spellings at once: the SDK's array and this service's own
			// images field. Neither may be dropped.
			name:       "the array merges with the declared images",
			body:       `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/a.png"}}],"images":["asset:img_b"]}`,
			wantText:   "",
			wantImages: []string{"asset:img_b", "https://x/a.png"},
		},
		{
			name:    "a shape that is neither",
			body:    `{"role":"user","content":42}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var message ChatCompletionMessage
			err := json.Unmarshal([]byte(tt.body), &message)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal accepted %s", tt.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal returned %v", err)
			}
			if message.GetContent() != tt.wantText {
				t.Errorf("Content = %q, want %q", message.GetContent(), tt.wantText)
			}
			if len(message.GetImages()) != len(tt.wantImages) {
				t.Fatalf("Images = %v, want %v", message.GetImages(), tt.wantImages)
			}
			for i, want := range tt.wantImages {
				if message.GetImages()[i] != want {
					t.Errorf("Images[%d] = %q, want %q", i, message.GetImages()[i], want)
				}
			}
		})
	}
}

// TestChatCompletionRequestBindsTheArrayForm pins that the relaxation reaches
// the whole request, which is what the handler actually binds.
func TestChatCompletionRequestBindsTheArrayForm(t *testing.T) {
	body := `{
		"sessionID":"s1","model":"gpt-4o-mini","stream":false,
		"messages":[{"role":"system","content":"你是助手"},{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]
	}`
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("the request did not bind: %v", err)
	}
	if req.GetSessionID() != "s1" || req.GetModel() != "gpt-4o-mini" {
		t.Errorf("the top-level fields were lost: sessionID=%q model=%q", req.GetSessionID(), req.GetModel())
	}
	if len(req.GetMessages()) != 2 {
		t.Fatalf("got %d messages, want 2", len(req.GetMessages()))
	}
	if req.GetMessages()[0].GetContent() != "你是助手" {
		t.Errorf("the plain message was mangled: %+v", req.GetMessages()[0])
	}
	last := req.GetMessages()[1]
	if last.GetContent() != "看图" || len(last.GetImages()) != 1 {
		t.Errorf("the array message decoded to %+v", last)
	}
}
