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

package runtime

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps a runtime profile name to its port. A name that is not
// registered falls back to the default port, which is the in-process eino
// runtime; an agent that names an unknown profile must be refused by the
// resolver before it reaches this map.
type Registry struct {
	mu       sync.RWMutex
	ports    map[string]Port
	agents   map[string]map[string]Port
	fallback Port
}

// NewRegistry creates an empty registry with fallback as the default port.
func NewRegistry(fallback Port) *Registry {
	return &Registry{ports: make(map[string]Port), agents: make(map[string]map[string]Port), fallback: fallback}
}

// Register adds a port under name. Registering the same name twice replaces the
// previous port, which is what a reload of a runtime profile does.
func (r *Registry) Register(name string, port Port) {
	if name == "" || port == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ports[name] = port
}

// RegisterFor adds a port for one profile and agent. A hosting pool gives each
// agent its own instance, so the profile's single endpoint is not enough.
func (r *Registry) RegisterFor(profile, agent string, port Port) {
	if profile == "" || agent == "" || port == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agents[profile] == nil {
		r.agents[profile] = make(map[string]Port)
	}
	r.agents[profile][agent] = port
}

// Unregister removes a port and every agent-specific port under the name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ports, name)
	delete(r.agents, name)
}

// LookupFor returns the agent-specific port when one is registered, otherwise
// the profile's shared port. An empty profile means the default port.
func (r *Registry) LookupFor(profile, agent string) (Port, error) {
	r.mu.RLock()
	if byAgent, ok := r.agents[profile]; ok {
		if port, ok := byAgent[agent]; ok {
			r.mu.RUnlock()
			return port, nil
		}
	}
	r.mu.RUnlock()
	return r.Lookup(profile)
}

// Lookup returns the port for name, falling back to the default port when name
// is empty. It returns an error for an unknown non-empty name: silently running
// on a different runtime is the failure this lookup exists to prevent.
func (r *Registry) Lookup(name string) (Port, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		if r.fallback == nil {
			return nil, fmt.Errorf("runtime: no default port registered")
		}
		return r.fallback, nil
	}
	if port, ok := r.ports[name]; ok {
		return port, nil
	}
	return nil, fmt.Errorf("runtime: profile %q is not registered", name)
}

// Default returns the fallback port.
func (r *Registry) Default() Port {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fallback
}

// Names returns the registered names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.ports))
	for name := range r.ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
