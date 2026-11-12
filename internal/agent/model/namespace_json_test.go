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

package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNamespaceJSONTags guards the wire spelling of Namespace. The scheduled-run
// executions endpoint serializes ScheduledExecutionM directly, and the other
// rows are converted to/from proto messages whose field is also namespace; a
// regression to json:"workspace" would be a silent API change.
func TestNamespaceJSONTags(t *testing.T) {
	values := []struct {
		name  string
		value any
	}{
		{"AgentM", &AgentM{Namespace: "ns"}},
		{"SkillM", &SkillM{Namespace: "ns"}},
		{"MCPM", &MCPM{Namespace: "ns"}},
		{"KnowledgeBaseM", &KnowledgeBaseM{Namespace: "ns"}},
		{"PromptM", &PromptM{Namespace: "ns"}},
		{"CustomCommandM", &CustomCommandM{Namespace: "ns"}},
		{"SessionM", &SessionM{Namespace: "ns"}},
		{"RevisionM", &RevisionM{Namespace: "ns"}},
		{"RuntimeInstanceM", &RuntimeInstanceM{Namespace: "ns"}},
		{"RuntimeProfileM", &RuntimeProfileM{Namespace: "ns"}},
		{"RuntimeHostingM", &RuntimeHostingM{Namespace: "ns"}},
		{"ScheduledRunM", &ScheduledRunM{Namespace: "ns"}},
		{"ScheduledExecutionM", &ScheduledExecutionM{Namespace: "ns"}},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(raw)
			if !strings.Contains(got, `"namespace":"ns"`) {
				t.Fatalf("expected namespace field, got %s", got)
			}
			if strings.Contains(got, `"workspace"`) {
				t.Fatalf("unexpected workspace field, got %s", got)
			}
		})
	}
}
