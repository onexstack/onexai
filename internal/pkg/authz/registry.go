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

package authz

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// alwaysAllowAuthorizer is used for AgentM.AuthzBackends == ["none"], letting
// a specific Agent opt out of Agent-level authorization entirely without
// touching the process-wide AuthzOptions.Enabled switch.
var alwaysAllowAuthorizer = AuthorizerFunc(func(_ context.Context, _ AuthorizationRequest) (Decision, string, error) {
	return DecisionAllow, "authz_backends=none", nil
})

// Registry maps a backend name (as referenced by AgentM.AuthzBackends, e.g.
// "opa", "webhook:approval-desk", "casdoor") to the concrete Authorizer
// instance the Provider already built for the process-wide Coordinator. It
// lets a single Agent select a subset of those same backend instances instead
// of always using the full process-wide Union.
//
// Registry is filled once at startup (see ProvideAuthzCoordinator) and only
// read afterwards, so no locking is needed.
type Registry struct {
	backends map[string]Authorizer
	// defaultAgentAuthz is the fallback used when an Agent does not specify
	// AuthzBackends, or when none of its entries resolve to a registered
	// backend — this is what preserves today's behavior unchanged.
	defaultAgentAuthz Authorizer

	// resolveCache memoizes Resolve's result by its sorted, deduplicated
	// backend-name key, so the common case (many Agents sharing the same
	// AuthzBackends list, or one Agent authorized repeatedly) does not
	// allocate a new unionAuthorizer on every AuthorizeAgent call. Safe for
	// concurrent use: entries are immutable once written, and Registry itself
	// is only mutated during Provider construction before any concurrent
	// Resolve call can happen.
	resolveCache sync.Map // resolveCacheKey(names) -> Authorizer
}

// NewRegistry creates a Registry that falls back to defaultAgentAuthz when an
// Agent has no (or no resolvable) AuthzBackends configured.
func NewRegistry(defaultAgentAuthz Authorizer) *Registry {
	return &Registry{backends: make(map[string]Authorizer), defaultAgentAuthz: defaultAgentAuthz}
}

// Register adds a backend Authorizer under the given name. Call this once per
// enabled backend right after constructing it in ProvideAuthzCoordinator.
func (r *Registry) Register(name string, a Authorizer) {
	if r == nil || a == nil || name == "" {
		return
	}
	r.backends[name] = a
}

// Names returns the registered backend names, sorted.
//
// These are exactly the values an Agent's AuthzBackends may reference. The
// "none" sentinel is deliberately not included: it is a directive, not a
// registered backend, and callers that offer it do so as a separate option.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetDefault updates the fallback Authorizer used by Resolve.
// ProvideAuthzCoordinator only knows the final process-wide agentToolAuthorizer
// Union after every backend has been registered, so NewRegistry is called with
// a nil default up front and SetDefault fills it in once that Union is built.
func (r *Registry) SetDefault(defaultAgentAuthz Authorizer) {
	if r == nil {
		return
	}
	r.defaultAgentAuthz = defaultAgentAuthz
}

// Resolve returns the Authorizer a given Agent should use for Agent-level
// authorization, based on its parsed AuthzBackends list:
//
//   - names empty → defaultAgentAuthz (no override configured).
//   - names == ["none"] → an Authorizer that always Allows (Agent opts out of
//     Agent-level authorization entirely).
//   - otherwise → Union of every name that resolves to a registered backend;
//     unresolvable names are skipped and logged. If none resolve, falls back
//     to defaultAgentAuthz (fail-safe: a typo in the rule table should not
//     silently turn into "no authorizer configured" for that Agent).
func (r *Registry) Resolve(agentName string, names []string) Authorizer {
	if r == nil || len(names) == 0 {
		return r.defaultAgentAuthz
	}
	if len(names) == 1 && names[0] == "none" {
		return alwaysAllowAuthorizer
	}

	key := resolveCacheKey(names)
	if cached, ok := r.resolveCache.Load(key); ok {
		return cached.(Authorizer)
	}

	var selected []Authorizer
	for _, n := range names {
		if a, ok := r.backends[n]; ok {
			selected = append(selected, a)
		} else {
			slog.Warn("[authz] AgentM.authz_backends references unknown backend, skipping",
				"agent", agentName, "backend", n)
		}
	}
	if len(selected) == 0 {
		slog.Warn("[authz] AgentM.authz_backends had no resolvable backend, falling back to default",
			"agent", agentName, "backends", names)
		return r.defaultAgentAuthz
	}
	resolved := NewUnionAuthorizer(selected...)
	r.resolveCache.Store(key, resolved)
	return resolved
}

// resolveCacheKey builds a cache key that is stable regardless of the input
// slice's element order — two Agents configuring the same backend set in a
// different order should share one cached Union.
func resolveCacheKey(names []string) string {
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}
