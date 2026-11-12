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

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/pkg/memory"
	"github.com/onexstack/onexai/internal/agent/store"
)

// MemoryService is the long-term memory subsystem as the gateway needs it.
type MemoryService interface {
	Add(ctx context.Context, memoryName, agent, agentNamespace, subject, sessionID, runID, content, source string) error
	SearchByName(ctx context.Context, memoryName, agent, subject, sessionID, query string, limit int) ([]memory.Hit, error)
	Prefetch(ctx context.Context, memoryName, agent, subject, sessionID, query string, maxChars int) (string, error)
}

// MemoryProvider exposes save/load/prefetch as MCP tools.
type MemoryProvider struct {
	Resources store.AIStore
	Service   MemoryService
}

// Namespace implements Provider.
func (p *MemoryProvider) Namespace() string { return "memory" }

// Tools implements Provider.
func (p *MemoryProvider) Tools() []Tool {
	return []Tool{
		{
			Namespace:   "memory",
			Name:        "save_memory",
			Description: "Save a specific fact, preference or finding for future conversations.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"content"},
				"properties": map[string]any{
					"content": map[string]any{"type": "string"},
				},
			},
			Handler: p.save,
		},
		{
			Namespace:   "memory",
			Name:        "load_memory",
			Description: "Search long-term memory for entries relevant to a question.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
			},
			Handler: p.load,
		},
		{
			Namespace:   "memory",
			Name:        "prefetch_memory",
			Description: "Return a compact block of memories relevant to the first message of a conversation.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			},
			Handler: p.prefetch,
		},
	}
}

// Invoke runs one of this provider's tools by name.
//
// It exists so the in-process runtime can call the same three handlers the MCP
// transport does, instead of a second implementation that would drift from it.
func (p *MemoryProvider) Invoke(ctx context.Context, claims Claims, name string, args json.RawMessage) (any, error) {
	switch name {
	case "save_memory", "memory_save_memory":
		return p.save(ctx, claims, args)
	case "load_memory", "memory_load_memory":
		return p.load(ctx, claims, args)
	case "prefetch_memory", "memory_prefetch_memory":
		return p.prefetch(ctx, claims, args)
	default:
		return nil, fmt.Errorf("memory: unknown tool %q", name)
	}
}

// config returns the agent's memory reference and the agent's own namespace.
// The namespace is the attribution written on every entry: a shared Memory is
// written in place, so it cannot be inferred from the Memory.
func (p *MemoryProvider) config(ctx context.Context, claims Claims) (string, string, error) {
	agent, err := p.Resources.Agents().Get(ctx, where.F("name", claims.Agent))
	if err != nil {
		return "", "", fmt.Errorf("resolve agent %q: %w", claims.Agent, err)
	}
	if !agent.MemoryEnabled || agent.MemoryRef == "" {
		return "", "", fmt.Errorf("agent %q does not have memory enabled", claims.Agent)
	}
	return agent.MemoryRef, agent.Namespace, nil
}

func (p *MemoryProvider) save(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Content == "" {
		return nil, fmt.Errorf("save_memory needs content")
	}
	memoryName, agentNamespace, err := p.config(ctx, claims)
	if err != nil {
		return nil, err
	}
	if err := p.Service.Add(ctx, memoryName, claims.Agent, agentNamespace, claims.Subject,
		claims.SessionKey, claims.RunID, input.Content, "tool"); err != nil {
		return nil, err
	}
	return map[string]any{"status": "saved"}, nil
}

func (p *MemoryProvider) load(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Query == "" {
		return nil, fmt.Errorf("load_memory needs a query")
	}
	memoryName, _, err := p.config(ctx, claims)
	if err != nil {
		return nil, err
	}
	hits, err := p.Service.SearchByName(ctx, memoryName, claims.Agent, claims.Subject, claims.SessionKey, input.Query, input.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		out = append(out, map[string]any{
			"id":      hit.Entry.ID,
			"content": hit.Entry.Content,
			"score":   hit.Score,
		})
	}
	return map[string]any{"memories": out}, nil
}

func (p *MemoryProvider) prefetch(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Query == "" {
		return nil, fmt.Errorf("prefetch_memory needs a query")
	}
	memoryName, _, err := p.config(ctx, claims)
	if err != nil {
		return nil, err
	}
	text, err := p.Service.Prefetch(ctx, memoryName, claims.Agent, claims.Subject, claims.SessionKey, input.Query, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"memories": text}, nil
}
