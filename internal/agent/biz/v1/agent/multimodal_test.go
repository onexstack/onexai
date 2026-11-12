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

package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

func TestToUserSchemaMessage(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		m := toUserSchemaMessage(agentapi.Message{Role: agentapi.RoleUser, Content: "hi"})
		if m.Content != "hi" {
			t.Fatalf("Content = %q, want %q", m.Content, "hi")
		}
		if len(m.UserInputMultiContent) != 0 {
			t.Fatalf("UserInputMultiContent = %d, want 0", len(m.UserInputMultiContent))
		}
	})

	t.Run("with images", func(t *testing.T) {
		m := toUserSchemaMessage(agentapi.Message{Role: agentapi.RoleUser, Content: "look", Images: []media.Ref{{URL: "data:image/png;base64,AAA"}}})
		if len(m.UserInputMultiContent) != 2 {
			t.Fatalf("UserInputMultiContent = %d, want 2 (text + image)", len(m.UserInputMultiContent))
		}
		var imageParts int
		for _, p := range m.UserInputMultiContent {
			if p.Type == schema.ChatMessagePartTypeImageURL && p.Image != nil && p.Image.URL != nil {
				imageParts++
				if *p.Image.URL != "data:image/png;base64,AAA" {
					t.Errorf("image URL = %q", *p.Image.URL)
				}
			}
		}
		if imageParts != 1 {
			t.Fatalf("image parts = %d, want 1", imageParts)
		}
	})
}
