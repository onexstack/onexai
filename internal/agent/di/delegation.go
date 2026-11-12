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

package di

import (
	"context"
	"sync"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// DelegationHolder breaks the cycle between the agent engine and the agent
// service.
//
// The engine needs a tool that calls the service (agent-as-tool), and the
// service is built from the engine: whichever one asked for the other first
// would deadlock the injector. The holder is the leaf both sides share — the
// engine gets the tool over it, and the service, once built, puts itself in.
//
// It is a pointer with a lock rather than a rebindable provider because the
// graph is built once and the tool instance is long-lived: the delegation tool
// is shared by every run and must see the invoker that arrived after it.
type DelegationHolder struct {
	mu      sync.RWMutex
	invoker capability.AgentInvoker
}

var _ capability.AgentInvoker = (*DelegationHolder)(nil)

// Set installs the invoker. It is called once, while the graph is being built.
func (h *DelegationHolder) Set(invoker capability.AgentInvoker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invoker = invoker
}

// Invoke implements capability.AgentInvoker.
func (h *DelegationHolder) Invoke(ctx context.Context, call capability.AgentCall) (capability.AgentResult, error) {
	h.mu.RLock()
	invoker := h.invoker
	h.mu.RUnlock()
	if invoker == nil {
		return capability.AgentResult{}, errno.ErrNotConfigured.WithMessage("delegation is not configured")
	}
	return invoker.Invoke(ctx, call)
}

// ProvideAgentProvider builds the agent-as-tool provider shared by the engine's
// in-process tool, the MCP gateway and the session tools.
//
// One instance serves all three: it is a pure policy object, and a second copy
// would only be a second place for the depth bound to drift.
func ProvideAgentProvider(
	cfg *Config, resources store.AIStore, delegation *DelegationHolder,
) *capability.AgentProvider {
	if resources == nil || delegation == nil {
		return nil
	}
	return &capability.AgentProvider{
		Lookup:   agentDelegates{resources: resources},
		Invoker:  delegation,
		MaxDepth: cfg.AgentOptions.Delegation.MaxDepth,
	}
}

// ProvideDelegationHolder is the leaf both the engine and the service share.
func ProvideDelegationHolder() *DelegationHolder {
	return &DelegationHolder{}
}

// agentDelegates reads an agent's effective delegates from the resource store.
type agentDelegates struct {
	resources store.AIStore
}

var _ capability.AgentLookup = agentDelegates{}

// Delegates implements capability.AgentLookup.
//
// It reads EffectiveDelegates, not the declared list: the in-process engine grants
// the agent tool from the controller-resolved union (templates included), and the
// MCP gateway must authorize against the same set. Using the declared list here
// would grant a tool call and then refuse it when the delegate came from a
// template — a permission check that disagrees with the grant it is checking.
func (a agentDelegates) Delegates(ctx context.Context, name string) ([]string, error) {
	if a.resources == nil {
		return nil, errno.ErrNotConfigured.WithMessage("no resource store is configured")
	}
	agent, err := a.resources.Agents().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(agent, err, "agent %q not found", name); err != nil {
		return nil, err
	}
	return agent.EffectiveDelegates(), nil
}
