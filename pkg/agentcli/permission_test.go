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

package agentcli

import (
	"encoding/json"
	"testing"
)

// encodeAnswer renders one answer the way RespondPermission puts it on the wire,
// so the tests can assert the bytes an agent actually reads.
func encodeAnswer(t *testing.T, requestID string, result PermissionResult) map[string]any {
	t.Helper()
	line, err := json.Marshal(newControlResponse(requestID, result.decision()))
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("the answer is not valid JSON (%s): %v", line, err)
	}
	return got
}

func TestPermissionDecisionAllow(t *testing.T) {
	got := encodeAnswer(t, "req-1", PermissionResult{Allow: true})

	if got["type"] != "control_response" {
		t.Errorf("type = %v, want control_response", got["type"])
	}
	envelope, ok := got["response"].(map[string]any)
	if !ok {
		t.Fatalf("response is %T, want an object", got["response"])
	}
	// The decision travels nested under "response". This nesting is the part that
	// is easy to get wrong and impossible to notice: an agent that cannot parse
	// the answer stays blocked, and nothing reports why.
	if envelope["subtype"] != "success" {
		t.Errorf("response.subtype = %v, want success", envelope["subtype"])
	}
	if envelope["request_id"] != "req-1" {
		t.Errorf("response.request_id = %v, want req-1", envelope["request_id"])
	}
	decision, ok := envelope["response"].(map[string]any)
	if !ok {
		t.Fatalf("response.response is %T, want the decision object", envelope["response"])
	}
	if decision["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", decision["behavior"])
	}
	// An allow that changed nothing must not invent an updatedInput: it would
	// replace the agent's arguments with whatever was sent.
	if _, present := decision["updatedInput"]; present {
		t.Errorf("an approval with no edit still carries updatedInput: %v", decision)
	}
}

func TestPermissionDecisionAllowWithUpdatedInput(t *testing.T) {
	// "Allow, but with these arguments" is how a host narrows a tool call it is
	// not comfortable with, so the substitute arguments have to survive intact.
	got := encodeAnswer(t, "req-2", PermissionResult{
		Allow:        true,
		UpdatedInput: json.RawMessage(`{"command":"rm -rf /tmp/one"}`),
	})
	decision := got["response"].(map[string]any)["response"].(map[string]any)

	if decision["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", decision["behavior"])
	}
	updated, ok := decision["updatedInput"].(map[string]any)
	if !ok {
		t.Fatalf("updatedInput is %T, want the argument object", decision["updatedInput"])
	}
	if got, want := updated["command"], "rm -rf /tmp/one"; got != want {
		t.Errorf("updatedInput.command = %v, want %v", got, want)
	}
}

func TestPermissionDecisionDeny(t *testing.T) {
	got := encodeAnswer(t, "req-3", PermissionResult{Allow: false, Message: "that file is out of scope"})
	decision := got["response"].(map[string]any)["response"].(map[string]any)

	if decision["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", decision["behavior"])
	}
	if decision["message"] != "that file is out of scope" {
		t.Errorf("message = %v, want the reason given", decision["message"])
	}
	if _, present := decision["interrupt"]; present {
		t.Errorf("a plain denial must not abandon the turn: %v", decision)
	}

	// The CLI requires a deny to say why. An empty message would be rejected as
	// schema-invalid, leaving the run blocked on a decision it cannot read.
	got = encodeAnswer(t, "req-4", PermissionResult{})
	decision = got["response"].(map[string]any)["response"].(map[string]any)
	if decision["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", decision["behavior"])
	}
	if message, _ := decision["message"].(string); message == "" {
		t.Error("a denial with no reason encoded an empty message, which the CLI rejects")
	}
}

func TestPermissionDecisionInterrupt(t *testing.T) {
	got := encodeAnswer(t, "req-5", PermissionResult{Message: "stopping", Interrupt: true})
	decision := got["response"].(map[string]any)["response"].(map[string]any)

	if decision["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny: an interrupt denies this call and abandons the turn", decision["behavior"])
	}
	if decision["interrupt"] != true {
		t.Errorf("interrupt = %v, want true", decision["interrupt"])
	}
}

func TestRespondPermissionNeedsARunningAgent(t *testing.T) {
	r, err := New(Options{Bin: "claude"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Nothing has started. Reporting that is the point: a dropped answer would
	// leave the agent blocked with no sign of why.
	if err := r.RespondPermission(t.Context(), "req-1", PermissionResult{Allow: true}); err == nil {
		t.Error("answering a run that is not running must fail, not silently succeed")
	}
	// A request with no ID cannot be tied to anything the agent asked, so
	// answering it would be answering nothing.
	if err := r.RespondPermission(t.Context(), "", PermissionResult{Allow: true}); err == nil {
		t.Error("an answer with no request id must be rejected")
	}
}
