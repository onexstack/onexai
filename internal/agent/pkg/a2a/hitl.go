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

package a2a

import (
	"encoding/json"
	"strings"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// HITL request/response types. They are the payload carried under
// HITLExtensionURI in a status message's metadata; the extension URI is the
// version, so these field names may be added to but not renamed.
const (
	HITLTypeToolApprovalRequest  = "tool_approval_request"
	HITLTypeAskUserRequest       = "ask_user_request"
	HITLTypeToolApprovalResponse = "tool_approval_response"
	HITLTypeAskUserResponse      = "ask_user_response"
)

// HITLTool describes one tool invocation awaiting a decision.
type HITLTool struct {
	ID     string         `json:"id"`
	CallID string         `json:"call_id,omitempty"`
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
}

// HITLQuestion is one question in an ask-user pause.
type HITLQuestion struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// HITLRequest is the public request payload carried by input-required.
type HITLRequest struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Hint      string         `json:"hint,omitempty"`
	Tools     []HITLTool     `json:"tools,omitempty"`
	Questions []HITLQuestion `json:"questions,omitempty"`
}

// HITLApproval is one decision in a tool-approval response.
type HITLApproval struct {
	ID              string `json:"id"`
	Approved        bool   `json:"approved"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

// HITLResponse is the public response payload.
type HITLResponse struct {
	Type      string              `json:"type"`
	ID        string              `json:"id,omitempty"`
	Approvals []HITLApproval      `json:"approvals,omitempty"`
	Answers   map[string][]string `json:"answers,omitempty"`
	// AllowAll answers this pause and every later one in the same run without
	// asking again. It is the peer's spelling of the engine's allow-all
	// decision, and it is deliberately separate from Approvals: approving each
	// pending tool says nothing about the next one.
	AllowAll bool `json:"allow_all,omitempty"`
}

// HITLRequestFromInterrupt maps the runtime's interrupt onto the public
// request payload.
func HITLRequestFromInterrupt(interrupt *agentapi.Interrupt) HITLRequest {
	request := HITLRequest{}
	if interrupt == nil {
		return request
	}
	request.ID = interrupt.RequestID
	for _, item := range interrupt.Requests {
		if item.Kind == agentapi.InterruptKindQuestion {
			request.Questions = append(request.Questions, HITLQuestion{
				ID:          item.Address,
				Question:    item.Question,
				Options:     item.Options,
				MultiSelect: item.MultiSelect,
			})
			continue
		}
		var args map[string]any
		if item.Arguments != "" {
			_ = json.Unmarshal([]byte(item.Arguments), &args)
		}
		request.Tools = append(request.Tools, HITLTool{
			ID:     item.Address,
			CallID: item.Address,
			Name:   item.ToolName,
			Args:   args,
		})
	}
	if len(request.Tools) > 0 {
		request.Type = HITLTypeToolApprovalRequest
		request.Hint = "Approval is required for tool(s): " + toolNames(request.Tools)
	} else {
		request.Type = HITLTypeAskUserRequest
		request.Hint = "Human input is required before the agent can continue."
	}
	return request
}

// Map returns the request in the JSON shape metadata carries.
//
// A struct cannot travel in task metadata: the store gob-encodes what it holds,
// and an unregistered concrete type fails the encode — the task is then reported
// as failed instead of pausing. The map is also what a peer receives after its
// own JSON round-trip, so producing it here means both sides read one shape.
func (r HITLRequest) Map() map[string]any {
	encoded, err := json.Marshal(r)
	if err != nil {
		return map[string]any{"type": r.Type, "id": r.ID}
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return map[string]any{"type": r.Type, "id": r.ID}
	}
	return out
}

func toolNames(tools []HITLTool) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ", ")
}

// DecodeHITLResponse decodes a response carried in message metadata.
func DecodeHITLResponse(raw any) (*HITLResponse, bool) {
	switch value := raw.(type) {
	case HITLResponse:
		return &value, value.Type == HITLTypeToolApprovalResponse || value.Type == HITLTypeAskUserResponse
	case *HITLResponse:
		if value == nil {
			return nil, false
		}
		return value, value.Type == HITLTypeToolApprovalResponse || value.Type == HITLTypeAskUserResponse
	case string:
		var response HITLResponse
		if err := json.Unmarshal([]byte(value), &response); err != nil {
			return nil, false
		}
		return &response, response.Type == HITLTypeToolApprovalResponse || response.Type == HITLTypeAskUserResponse
	case map[string]any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var response HITLResponse
		if err := json.Unmarshal(encoded, &response); err != nil {
			return nil, false
		}
		return &response, response.Type == HITLTypeToolApprovalResponse || response.Type == HITLTypeAskUserResponse
	default:
		return nil, false
	}
}

// DecodeHITLRequest decodes a request found in status message metadata.
func DecodeHITLRequest(raw any) (*HITLRequest, bool) {
	switch value := raw.(type) {
	case string:
		var request HITLRequest
		if err := json.Unmarshal([]byte(value), &request); err != nil {
			return nil, false
		}
		return &request, request.Type != ""
	case map[string]any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var request HITLRequest
		if err := json.Unmarshal(encoded, &request); err != nil {
			return nil, false
		}
		return &request, request.Type != ""
	default:
		return nil, false
	}
}
