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

package agentapi

import (
	"bytes"
	"encoding/gob"
	"strings"
	"testing"
)

// TestDecisionValuesAreWireValues pins the three words.
//
// They are the API rather than an implementation detail: chat maps a button
// click and a typed "允许" (Allow) onto these constants, and the value travels to the
// engine as the decision of an approval. The same three literals are declared
// again in oai-chat-apiserver's pkg/agent — deliberately, because the two services
// ship separately, and a shared Go constant would let one release silently change
// the other's meaning. The price of that choice is this test on each side: the
// mirror is only safe while the literals are pinned.
func TestDecisionValuesAreWireValues(t *testing.T) {
	got := map[string]string{
		"DecisionAllow":    DecisionAllow,
		"DecisionDeny":     DecisionDeny,
		"DecisionAllowAll": DecisionAllowAll,
	}
	want := map[string]string{
		"DecisionAllow":    "allow",
		"DecisionDeny":     "deny",
		"DecisionAllowAll": "allow_all",
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %q, want %q", name, got[name], wantValue)
		}
	}
}

// TestInterruptPayloadsSurviveGob pins the registration the pause path depends
// on, and the names it is registered under.
//
// An interrupt's Info is persisted with the checkpoint as an interface value, and
// gob can only encode one whose dynamic type is registered — under a name that is
// written into the stream, because that is what a later reader looks the type up
// by. Nothing about the payload's own code hints at this; it is discovered by
// gating a real tool, and it fails as a run that could not save its checkpoint
// rather than as a run that asked a question nobody can answer.
//
// So both halves are asserted here: a payload that cannot round-trip through gob,
// and a payload whose registered name has moved. Renaming the second does not
// break a fresh pause — it strands every pause already stored under the old name.
func TestInterruptPayloadsSurviveGob(t *testing.T) {
	tests := []struct {
		name    string
		gobName string
		payload any
	}{
		{
			name:    "a permission prompt",
			gobName: "onexai_permission_info",
			payload: &PermissionInfo{ToolName: "delete_cluster", CallID: "call_1", Arguments: `{"cluster":"test"}`},
		},
		{
			name:    "a question",
			gobName: "onexai_ask_question_info",
			payload: &AskQuestionInfo{Question: "选哪个？", Options: []string{"a", "b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := gob.NewEncoder(&buf).Encode(map[string]any{"info": tt.payload}); err != nil {
				t.Fatalf("gob cannot encode the interrupt payload, so the checkpoint would not save: %v", err)
			}
			if !strings.Contains(buf.String(), tt.gobName) {
				t.Errorf("the encoded checkpoint does not name %q, so a pause stored by an earlier build "+
					"could not be read back:\n%q", tt.gobName, buf.String())
			}

			var back map[string]any
			if err := gob.NewDecoder(&buf).Decode(&back); err != nil {
				t.Fatalf("gob cannot decode it back, so a resumed run could not read its own pause: %v", err)
			}
		})
	}
}
