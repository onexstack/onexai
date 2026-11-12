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

import "testing"

// TestApprovalVocabularyIsWireValues pins the words this service sends to
// agent and reads back from it.
//
// The decisions are declared again in oai-agent-apiserver's pkg/agent, and the kinds in
// its interrupt handling. That duplication is deliberate: the two binaries are
// released separately, so a shared Go constant would let one release change the
// other's meaning without a compile error anywhere. The price is this test on
// each side — the mirror is only safe while the literals are held still.
//
// What breaks if one moves: a decision the engine does not recognize is an
// approval it cannot act on, which is the failure mode where the tool the user
// just refused runs anyway.
func TestApprovalVocabularyIsWireValues(t *testing.T) {
	got := map[string]string{
		"DecisionAllow":           DecisionAllow,
		"DecisionDeny":            DecisionDeny,
		"DecisionAllowAll":        DecisionAllowAll,
		"InterruptKindPermission": InterruptKindPermission,
		"InterruptKindQuestion":   InterruptKindQuestion,
	}
	want := map[string]string{
		"DecisionAllow":           "allow",
		"DecisionDeny":            "deny",
		"DecisionAllowAll":        "allow_all",
		"InterruptKindPermission": "permission",
		"InterruptKindQuestion":   "question",
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %q, want %q", name, got[name], wantValue)
		}
	}
}
