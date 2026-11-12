// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package registry implements a namespace-aware tool registry for oai-mcpserver.
//
// A namespace groups related tools the same way a Kubernetes namespace groups
// related resources: it is a plain string label, tools are addressed on the
// wire as "<namespace>_<name>", and callers can list tools scoped to one
// namespace or across all of them.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Handler executes a single tool call and returns its result.
// args carries the raw "arguments" object from the "tools/call" request.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// Tool describes a single callable tool registered under a namespace.
type Tool struct {
	// Namespace groups related tools, analogous to a Kubernetes namespace.
	Namespace string
	// Name is the tool's short name, unique within its namespace.
	Name string
	// Description is a human-readable summary shown to MCP clients.
	Description string
	// InputSchema is the JSON Schema describing the tool's arguments.
	InputSchema map[string]any
	// Handler executes the tool.
	Handler Handler
}

// QualifiedName returns the wire-visible tool name in "<namespace>_<name>" form.
func (t Tool) QualifiedName() string {
	return t.Namespace + "_" + t.Name
}

// Registry is a namespace-aware collection of tools, safe for concurrent use.
//
// Tools are registered once at process startup (from each namespace
// package's init()) and never removed, so List and Namespaces cache their
// sorted output: sorted holds every tool sorted by qualified name, and
// namespaces holds the distinct namespace list sorted alphabetically.
// Register invalidates both by clearing them; the next List/Namespaces call
// rebuilds and repopulates. This turns the common case — many read-only
// tools/list requests against a tool set that stopped changing at startup —
// from an O(n log n) sort per request into one sort total.
type Registry struct {
	mu         sync.RWMutex
	tools      map[string]Tool // keyed by QualifiedName()
	sorted     []Tool          // cache of tools, sorted by qualified name; nil when invalidated
	namespaces []string        // cache of distinct namespaces, sorted; nil when invalidated
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// defaultRegistry is the process-wide registry populated by each namespace
// package's init() function, mirroring the handler.Register pattern used by
// internal/apiserver/handler.
var defaultRegistry = New()

// Register adds tool to the default, process-wide registry. It is intended
// to be called from a namespace package's init() function, e.g.:
//
//	func init() {
//		registry.Register(registry.Tool{Namespace: "echo", Name: "ping", ...})
//	}
func Register(tool Tool) {
	defaultRegistry.Register(tool)
}

// Default returns the process-wide registry populated via Register.
func Default() *Registry {
	return defaultRegistry
}

// Register adds tool to the registry. It panics if namespace or name is
// empty, or if the same qualified name is registered twice, since that
// indicates a programming error caught at process startup.
func (r *Registry) Register(tool Tool) {
	if tool.Namespace == "" || tool.Name == "" {
		panic("registry: tool namespace and name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := tool.QualifiedName()
	if _, exists := r.tools[key]; exists {
		panic(fmt.Sprintf("registry: tool %q already registered", key))
	}

	r.tools[key] = tool
	r.sorted = nil
	r.namespaces = nil
}

// Get looks up a single tool by namespace and short name.
func (r *Registry) Get(namespace, name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[namespace+"_"+name]
	return tool, ok
}

// List returns every tool registered under namespace, sorted by qualified
// name for deterministic output. An empty namespace returns every tool
// across all namespaces, mirroring `kubectl get --all-namespaces`.
//
// The returned slice is always freshly allocated, never the cache backing
// sortedTools/Namespaces: callers are free to mutate or retain it without
// risking corrupting the registry's cache.
func (r *Registry) List(namespace string) []Tool {
	sorted := r.sortedTools()

	tools := make([]Tool, 0, len(sorted))
	for _, tool := range sorted {
		if namespace == "" || tool.Namespace == namespace {
			tools = append(tools, tool)
		}
	}
	return tools
}

// Namespaces returns every distinct namespace currently registered, sorted
// alphabetically. The returned slice is a fresh copy of the cached one, so
// callers can't corrupt the cache by mutating what they get back.
func (r *Registry) Namespaces() []string {
	return append([]string(nil), r.cachedNamespaces()...)
}

// cachedNamespaces returns the cached, sorted distinct-namespace slice,
// rebuilding it if Register has invalidated it since the last call.
func (r *Registry) cachedNamespaces() []string {
	r.mu.RLock()
	if r.namespaces != nil {
		defer r.mu.RUnlock()
		return r.namespaces
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.namespaces == nil {
		seen := make(map[string]struct{})
		for _, tool := range r.tools {
			seen[tool.Namespace] = struct{}{}
		}

		namespaces := make([]string, 0, len(seen))
		for ns := range seen {
			namespaces = append(namespaces, ns)
		}
		sort.Strings(namespaces)

		r.namespaces = namespaces
	}

	return r.namespaces
}

// sortedTools returns every registered tool sorted by qualified name,
// rebuilding and caching the slice if Register has invalidated it since the
// last call.
func (r *Registry) sortedTools() []Tool {
	r.mu.RLock()
	if r.sorted != nil {
		defer r.mu.RUnlock()
		return r.sorted
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sorted == nil {
		tools := make([]Tool, 0, len(r.tools))
		for _, tool := range r.tools {
			tools = append(tools, tool)
		}

		sort.Slice(tools, func(i, j int) bool {
			return tools[i].QualifiedName() < tools[j].QualifiedName()
		})

		r.sorted = tools
	}

	return r.sorted
}
