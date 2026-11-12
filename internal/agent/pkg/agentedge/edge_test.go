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

package agentedge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsTextDocument pins which attachments are read as text and which are not:
// a document the model could not read must be refused, not silently pasted in
// as binary.
func TestIsTextDocument(t *testing.T) {
	cases := map[string]struct {
		mime string
		name string
		want bool
	}{
		"markdown":      {"text/markdown", "", true},
		"plain":         {"text/plain", "", true},
		"json":          {"application/json", "", true},
		"yaml":          {"application/yaml", "", true},
		"no mime, .md":  {"", "notes.md", true},
		"no mime, .txt": {"", "runbook.txt", true},
		"png":           {"image/png", "shot.png", false},
		"pdf":           {"application/pdf", "spec.pdf", false},
		"octet-stream":  {"application/octet-stream", "blob", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsTextDocument(tc.mime, tc.name))
		})
	}
}
