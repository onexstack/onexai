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

// Package capability is the MCP service a runtime calls for onexai-owned work:
// knowledge bases, skills, memory, custom commands and sandboxes. It is a
// domain service; the MCP transport in this package is one adapter over it.
package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/onexstack/onexai/internal/agent/model"
)

// Tool is one callable capability.
type Tool struct {
	// Namespace is the scope a token must carry to see and call the tool.
	Namespace string
	// Name is the wire-visible name, without the namespace prefix.
	Name string
	// Description is what the model reads.
	Description string
	// InputSchema is the JSON Schema for the arguments.
	InputSchema map[string]any
	// Handler executes the call. It receives the verified claims so the
	// handler can enforce per-run limits and attribute the work.
	Handler func(ctx context.Context, claims Claims, args json.RawMessage) (any, error)
}

// QualifiedName is the name the MCP client sees.
func (t Tool) QualifiedName() string { return t.Namespace + "_" + t.Name }

// Provider contributes a namespace of tools.
type Provider interface {
	Namespace() string
	Tools() []Tool
}

// Registry holds the providers registered at startup.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider. Registering the same namespace twice replaces the
// previous one, which is what a reload does.
func (r *Registry) Register(provider Provider) {
	if provider == nil || provider.Namespace() == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.Namespace()] = provider
}

// Tools returns every tool, sorted by qualified name.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var tools []Tool
	for _, provider := range r.providers {
		tools = append(tools, provider.Tools()...)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].QualifiedName() < tools[j].QualifiedName() })
	return tools
}

// Lookup returns the tool with the given qualified name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, provider := range r.providers {
		for _, tool := range provider.Tools() {
			if tool.QualifiedName() == name {
				return tool, true
			}
		}
	}
	return Tool{}, false
}

// Service authorizes and executes capability calls.
type Service struct {
	registry *Registry
	issuer   *Issuer
	// runs answers "is the run behind this token still in flight". Nil skips
	// the check, which is what a deployment without a run store has to do.
	runs RunLookup
}

// ErrRunNotActive reports a token whose run has already ended.
//
// The token is short-lived but not instantly dead: a run that finishes while
// holding a one-hour token would otherwise leave a credential usable long after
// the work it was minted for is over.
var ErrRunNotActive = errors.New("capability: the run behind this token is not active")

// NewService builds the service.
func NewService(registry *Registry, issuer *Issuer, runs RunLookup) *Service {
	return &Service{registry: registry, issuer: issuer, runs: runs}
}

// Issuer returns the token issuer, so the run path can mint a token.
func (s *Service) Issuer() *Issuer { return s.issuer }

// VisibleTools returns the tools the claims may see.
func (s *Service) VisibleTools(claims Claims) []Tool {
	all := s.registry.Tools()
	visible := make([]Tool, 0, len(all))
	for _, tool := range all {
		if claims.HasScope(tool.Namespace) {
			visible = append(visible, tool)
		}
	}
	return visible
}

// Call executes one tool call under the claims.
func (s *Service) Call(ctx context.Context, claims Claims, name string, args json.RawMessage) (any, error) {
	tool, ok := s.registry.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("capability: unknown tool %q", name)
	}
	if !claims.HasScope(tool.Namespace) {
		return nil, fmt.Errorf("capability: token is not scoped for %q", tool.Namespace)
	}
	if err := s.checkActive(ctx, claims); err != nil {
		return nil, err
	}
	return tool.Handler(ctx, claims, args)
}

// checkActive refuses a call whose run has already settled.
//
// A run that no longer exists is allowed: the token is signed and unexpired, and
// turning a missing row (a store that failed to record, a database that lost a
// write) into "every tool call fails" would trade a narrow replay window for a
// broad outage. A run that exists and has finished is a different answer — that
// is a credential outliving its purpose, and it is refused.
func (s *Service) checkActive(ctx context.Context, claims Claims) error {
	if s.runs == nil || claims.RunID == "" {
		return nil
	}
	run, err := s.runs.GetRun(ctx, claims.RunID)
	if err != nil || run == nil {
		return nil
	}
	switch run.State {
	case model.RunStateRunning, model.RunStateInterrupted:
		return nil
	default:
		return ErrRunNotActive
	}
}
