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

package capability

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentInvoker runs one agent as a tool: a separate run, with its own prompt,
// session and runtime, returning its answer to the caller.
//
// It is an interface here and implemented by the business layer because the
// dependency only points one way: the business layer already imports this
// package for the token types, so the interface lives here and the
// implementation is wired in at startup. That also keeps the delegated run on
// exactly the path an ordinary request takes — same admission, same
// authorization, same event stream — rather than a second execution route that
// would drift from it.
type AgentInvoker interface {
	Invoke(ctx context.Context, call AgentCall) (AgentResult, error)
}

// AgentCall is one delegation.
type AgentCall struct {
	// Agent is the delegate's name.
	Agent string
	// Message is what the caller wants the delegate to do.
	Message string
	// Session continues an earlier conversation with the same delegate. Empty
	// starts a new one, derived from ParentSession so repeated calls from one
	// conversation share the delegate's context.
	Session string
	// ParentSession is the caller's conversation, used to derive a delegate
	// session when the call does not name one.
	ParentSession string
	// TaskID continues a paused delegate run. It is the id the pause returned.
	TaskID string
	// Answer resumes the paused run named by TaskID.
	Answer *AgentAnswer
	// ParentRunID, RootRunID and Depth are the lineage the delegated run
	// records. Depth is the delegate's own depth, not the caller's.
	ParentRunID string
	RootRunID   string
	Depth       int
	// Subject is the identity the delegate runs as. Delegation never escalates:
	// the caller's identity travels unchanged, so the delegate is authorized
	// for what the caller is authorized for and nothing more.
	Subject     string
	SubjectKind string
}

// AgentAnswer answers a paused delegate run.
type AgentAnswer struct {
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"`
	AllowAll  bool   `json:"allowAll,omitempty"`
}

// AgentResult is what a delegation returns.
type AgentResult struct {
	// Session is the delegate's conversation, for a follow-up call.
	Session string `json:"session,omitempty"`
	// TaskID is the delegate's run, which a pause is answered against.
	TaskID string `json:"taskId,omitempty"`
	// State is completed, input-required, failed or canceled.
	State string `json:"state"`
	// Text is the delegate's answer. Empty when the run paused or failed.
	Text string `json:"text,omitempty"`
	// Interrupts describes what the run is waiting on, when State is
	// input-required. Every entry carries the same requestId; the answers are
	// submitted together against it.
	Interrupts []AgentInterrupt `json:"interrupts,omitempty"`
	// Error explains a failed or canceled run.
	Error string `json:"error,omitempty"`
}

// AgentInterrupt is a paused delegate's pending request.
type AgentInterrupt struct {
	RequestID string   `json:"requestId"`
	Kind      string   `json:"kind,omitempty"`
	ToolName  string   `json:"toolName,omitempty"`
	Question  string   `json:"question,omitempty"`
	Options   []string `json:"options,omitempty"`
}

// AgentLookup resolves the agents a run may delegate to.
//
// It is one method rather than the whole resource store because that is all
// delegation needs, and because the check has to be testable without building
// a store.
type AgentLookup interface {
	Delegates(ctx context.Context, agent string) ([]string, error)
}

// AgentProvider exposes delegation as the agent_invoke_agent tool.
//
// Delegation is a declared authority, not an ambient one: the token carries the
// agent scope only when the agent lists delegates, and the handler re-checks
// the target against that list. A tool call cannot name an agent the caller was
// not configured to call.
type AgentProvider struct {
	// Lookup resolves the caller's declared delegates.
	Lookup AgentLookup
	// Invoker runs the delegate.
	Invoker AgentInvoker
	// MaxDepth bounds the delegation chain. A call that would exceed it is
	// refused, which is what stops two agents that delegate to each other from
	// recursing until the process runs out of memory.
	MaxDepth int
}

// DefaultAgentMaxDepth is the chain length a deployment gets without saying.
const DefaultAgentMaxDepth = 3

// Namespace implements Provider.
func (p *AgentProvider) Namespace() string { return "agent" }

// Tools implements Provider.
func (p *AgentProvider) Tools() []Tool {
	if p.Invoker == nil {
		return nil
	}
	return []Tool{{
		Namespace:   "agent",
		Name:        "invoke_agent",
		Description: "Delegate a self-contained task to another agent and return its answer. The other agent runs with its own instructions, tools and conversation.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"agent", "message"},
			"properties": map[string]any{
				"agent":   map[string]any{"type": "string", "description": "The name of the agent to call."},
				"message": map[string]any{"type": "string", "description": "What the agent should do. Include every detail it needs; it does not see this conversation."},
				"session": map[string]any{"type": "string", "description": "Continue an earlier call to the same agent by passing the session id it returned."},
				"taskId": map[string]any{
					"type":        "string",
					"description": "Answer a paused call. Pass the taskId it returned together with an answer.",
				},
				"answer": map[string]any{
					"type":        "object",
					"description": "The answer to a paused call. Valid with taskId.",
					"required":    []string{"requestId", "decision"},
					"properties": map[string]any{
						"requestId": map[string]any{"type": "string"},
						"decision":  map[string]any{"type": "string", "enum": []string{"allow", "deny", "allow_all"}},
						"allowAll":  map[string]any{"type": "boolean"},
					},
				},
			},
		},
		Handler: p.Invoke,
	}}
}

// AgentArguments is the tool's argument shape.
type AgentArguments struct {
	Agent   string       `json:"agent"`
	Message string       `json:"message"`
	Session string       `json:"session"`
	TaskID  string       `json:"taskId"`
	Answer  *AgentAnswer `json:"answer"`
}

// Invoke executes an MCP tool call: decode, check the target against the
// caller's declared delegates, then run.
func (p *AgentProvider) Invoke(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	input, err := decodeInvokeArguments(args)
	if err != nil {
		return nil, err
	}
	if err := p.authorize(ctx, claims.Agent, input.Agent); err != nil {
		return nil, err
	}
	return p.Call(ctx, AgentRequest{
		Arguments:     input,
		ParentSession: claims.SessionKey,
		RunID:         claims.RunID,
		RootRunID:     rootRunOf(claims),
		Depth:         claims.Depth,
		Subject:       claims.Subject,
		SubjectKind:   claims.SubjectKind,
	})
}

// AgentRequest is a delegation whose authority the caller has established.
//
// Call is exported separately because the two callers establish authority
// differently: the MCP path reads it from a signed token, and the in-process
// path from the run's resolved selection. Everything after that point — the
// depth bound and the invoker call — is identical, so it lives here once.
type AgentRequest struct {
	Arguments     AgentArguments
	ParentSession string
	RunID         string
	RootRunID     string
	// Depth is the caller's depth; the delegate runs one deeper.
	Depth       int
	Subject     string
	SubjectKind string
}

// Call runs the delegate. The caller is responsible for having checked that the
// target is one this run may call.
func (p *AgentProvider) Call(ctx context.Context, req AgentRequest) (AgentResult, error) {
	if p.Invoker == nil {
		return AgentResult{}, fmt.Errorf("invoke_agent: delegation is not configured")
	}
	depth := req.Depth + 1
	if maxDepth := p.maxDepth(); depth > maxDepth {
		return AgentResult{}, fmt.Errorf("invoke_agent: delegation depth %d exceeds the limit of %d", depth, maxDepth)
	}
	root := req.RootRunID
	if root == "" {
		root = req.RunID
	}
	return p.Invoker.Invoke(ctx, AgentCall{
		Agent:         req.Arguments.Agent,
		Message:       req.Arguments.Message,
		Session:       req.Arguments.Session,
		ParentSession: req.ParentSession,
		TaskID:        req.Arguments.TaskID,
		Answer:        req.Arguments.Answer,
		ParentRunID:   req.RunID,
		RootRunID:     root,
		Depth:         depth,
		Subject:       req.Subject,
		SubjectKind:   req.SubjectKind,
	})
}

// Allows reports whether name is one of the given delegation targets.
func (p *AgentProvider) Allows(agents []string, name string) bool {
	for _, agent := range agents {
		if agent == name {
			return true
		}
	}
	return false
}

func decodeInvokeArguments(args json.RawMessage) (AgentArguments, error) {
	var input AgentArguments
	if err := json.Unmarshal(args, &input); err != nil {
		return input, fmt.Errorf("invoke_agent: decode arguments: %w", err)
	}
	if input.Agent == "" {
		return input, fmt.Errorf("invoke_agent needs an agent")
	}
	if input.Message == "" && input.Answer == nil {
		return input, fmt.Errorf("invoke_agent needs a message or an answer")
	}
	if input.Answer != nil && input.TaskID == "" {
		return input, fmt.Errorf("invoke_agent: an answer needs the taskId it answers")
	}
	return input, nil
}

// authorize checks the target against the caller's declared delegates.
//
// The check is repeated here rather than trusted to the token scope because the
// scope only says the agent may delegate somewhere; it does not say where.
func (p *AgentProvider) authorize(ctx context.Context, caller, target string) error {
	allowed, err := p.Lookup.Delegates(ctx, caller)
	if err != nil {
		return fmt.Errorf("invoke_agent: resolve agent %q: %w", caller, err)
	}
	for _, name := range allowed {
		if name == target {
			return nil
		}
	}
	return fmt.Errorf("invoke_agent: agent %q is not allowed to call %q", caller, target)
}

func (p *AgentProvider) maxDepth() int {
	if p.MaxDepth > 0 {
		return p.MaxDepth
	}
	return DefaultAgentMaxDepth
}

// rootRunOf returns the run at the top of the delegation chain.
func rootRunOf(claims Claims) string {
	if claims.RootRunID != "" {
		return claims.RootRunID
	}
	return claims.RunID
}
