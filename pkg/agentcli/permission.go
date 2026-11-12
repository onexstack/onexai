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

import "encoding/json"

// The permission handshake, as the CLI defines it.
//
// With --permission-prompt-tool stdio the CLI does not guess and does not skip:
// when a tool call needs approval it stops the turn, writes a control_request on
// stdout and blocks until a control_response arrives on stdin. Nothing else will
// unblock it — no timeout, no default — so a host that opens the stream without
// answering hangs the session on the first tool that needs permission. That is
// the whole reason RespondPermission exists and the reason PermissionStdio is
// not a mode to enable casually.
//
// The wire shapes below are transcribed from the CLI's own schema:
//
//	{"type":"control_request","request_id":"...","request":{
//	   "subtype":"can_use_tool","tool_name":"Bash","input":{...},"tool_use_id":"..."}}
//
//	{"type":"control_response","response":{
//	   "subtype":"success","request_id":"...",
//	   "response":{"behavior":"allow"}}}
//
// A decision is exactly one of two shapes, and the CLI validates it: an allow
// carries an optional updatedInput (allow, but with these arguments instead),
// a deny carries a required message. A decision that is neither is rejected as
// schema-invalid, so it is better to model the two cases than to send a loose
// map and hope.

// PermissionRequest is one tool call the agent has stopped and asked about.
type PermissionRequest struct {
	// RequestID identifies this request; the answer echoes it.
	RequestID string
	// ToolName is the tool the agent wants to run.
	ToolName string
	// Input is the tool's arguments, verbatim, for a human to judge.
	Input json.RawMessage
	// ToolUseID ties the request to the block that produced it.
	ToolUseID string
}

// PermissionResult is the host's answer to a PermissionRequest.
//
// Allow and Message are mutually exclusive in the wire format: Allow chooses the
// "allow" decision, and anything else is a "deny" carrying Message. That is why
// they are not two independent booleans — an "allow" with a message is not a
// shape the CLI accepts.
type PermissionResult struct {
	// Allow approves the call. When false the call is denied and Message is
	// shown to the agent as the reason.
	Allow bool
	// Message explains a denial, and is required for one.
	Message string
	// UpdatedInput replaces the tool's arguments when Allow is set. Leave it nil
	// to approve the call as asked.
	UpdatedInput json.RawMessage
	// Interrupt abandons the whole turn instead of denying this one call.
	Interrupt bool
}

// decision renders the result as the decision object the CLI validates.
func (r PermissionResult) decision() map[string]any {
	if r.Allow {
		d := map[string]any{"behavior": "allow"}
		if len(r.UpdatedInput) > 0 {
			// Passed through verbatim: it is the agent's own argument JSON, and
			// re-encoding it here would only risk losing a field.
			d["updatedInput"] = json.RawMessage(r.UpdatedInput)
		}
		return d
	}

	message := r.Message
	if message == "" {
		// The CLI requires a deny to say why. An empty reason would be rejected
		// as schema-invalid, which would leave the run blocked on a decision the
		// agent cannot read.
		message = "denied by the operator"
	}
	d := map[string]any{"behavior": "deny", "message": message}
	if r.Interrupt {
		d["interrupt"] = true
	}
	return d
}

// controlResponse is the envelope the CLI expects on stdin. The decision travels
// nested under "response", matching the request: an envelope, not a bare object.
type controlResponse struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  any    `json:"response,omitempty"`
		Error     string `json:"error,omitempty"`
	} `json:"response"`
}

// newControlResponse builds a successful answer to requestID.
func newControlResponse(requestID string, decision any) controlResponse {
	var r controlResponse
	r.Type = "control_response"
	r.Response.Subtype = "success"
	r.Response.RequestID = requestID
	r.Response.Response = decision
	return r
}
