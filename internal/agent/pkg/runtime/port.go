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

// Package runtime is the boundary between the agent gateway and an execution
// runtime. A runtime is addressed with A2A-shaped requests and streams the
// domain event type the rest of this service already understands.
//
// The domain core deliberately does not see A2A artifact/part types: those are
// the wire representation, and an adapter owns translating them. The one
// exception is the request, which is A2A because that is what the runtime
// ecosystem speaks.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/onexstack/onexai/internal/agent/pkg/a2aevent"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

const (
	// MetadataRunID carries the gateway's run id.
	MetadataRunID = "onexai.dev/a2a/run-id"
	// MetadataAgent carries the agent selection.
	MetadataAgent = "onexai.dev/a2a/agent"
	// MetadataHITLResponse carries an answer to a paused run.
	MetadataHITLResponse = "onexai.dev/a2a/hitl-response"
	// MetadataCapabilityToken is the per-run token a remote runtime presents to
	// the MCP capability gateway.
	MetadataCapabilityToken = "onexai.dev/a2a/capability-token"
)

// Request is one turn handed to a runtime.
//
// A2A is the wire contract. SessionKey, RunID, Options and History are the
// domain values the gateway resolved for this turn; a remote adapter sends the
// wire fields and ignores the rest, while an in-process adapter uses them
// directly. Keeping them here rather than hiding them in metadata is what lets
// the in-process path stay allocation-free and the remote path stay standard.
type Request struct {
	A2A        *a2a.SendMessageRequest
	SessionKey string
	RunID      string
	Options    agentapi.RunOptions
	// History is supplied only to adapters whose Agent Card declares
	// history=gateway. An adapter that owns its history ignores it.
	History []agentapi.Message
}

// Port is one execution runtime.
type Port interface {
	// Name identifies the runtime implementation (for logs and metrics). For a
	// remote runtime it is the profile's name, which is what an operator
	// recognizes in a log line.
	Name() string
	// Kind is the implementation's class — "inproc" or "a2a" — and is what a run
	// or session row records. It is deliberately not Name: a profile name is
	// chosen by an operator and can be any length, while the stored column
	// answers "which class of runtime executed this", a question with a fixed
	// set of answers.
	Kind() string
	// Card is the runtime's capability manifest. It is also the capability
	// negotiation input: the gateway refuses a run whose required feature the
	// card does not declare.
	Card(ctx context.Context) (*a2a.AgentCard, error)
	// Run executes one turn, or continues a paused task when the request
	// carries a HITL response.
	Run(ctx context.Context, req *Request) (<-chan agentapi.Event, error)
	// Cancel stops an in-flight run. It is idempotent.
	Cancel(ctx context.Context, runID string) error
}

// HITLResponse is the answer to a paused run, carried in A2A metadata.
type HITLResponse struct {
	RequestID string            `json:"requestId"`
	Decision  string            `json:"decision"`
	Answers   map[string]string `json:"answers,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	AllowAll  bool              `json:"allowAll,omitempty"`
}

// SetHITLResponse attaches an answer to a send request.
func SetHITLResponse(req *a2a.SendMessageRequest, response HITLResponse) error {
	if req == nil {
		return fmt.Errorf("runtime: nil send request")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("runtime: encode HITL response: %w", err)
	}
	if req.Metadata == nil {
		req.Metadata = make(map[string]any, 1)
	}
	req.Metadata[MetadataHITLResponse] = string(encoded)
	return nil
}

// HITLResponseFrom reads an answer attached to a send request.
func HITLResponseFrom(req *a2a.SendMessageRequest) (HITLResponse, bool) {
	if req == nil || req.Metadata == nil {
		return HITLResponse{}, false
	}
	raw, ok := req.Metadata[MetadataHITLResponse]
	if !ok {
		return HITLResponse{}, false
	}
	text, ok := raw.(string)
	if !ok {
		return HITLResponse{}, false
	}
	var response HITLResponse
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		return HITLResponse{}, false
	}
	return response, response.RequestID != ""
}

// UserText returns the text of the request's message, concatenated in part
// order. It is the input every runtime ultimately reasons over; images travel
// separately.
func UserText(req *a2a.SendMessageRequest) string { return a2aevent.UserText(req) }
