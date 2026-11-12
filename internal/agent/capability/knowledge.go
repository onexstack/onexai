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

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// KnowledgeBaseSource performs one retrieval across an agent's knowledge
// bases. It is the same tool the in-process agent calls, so grading, query
// rewriting, watermarking and audit are not reimplemented here.
type KnowledgeBaseSource interface {
	Search(ctx context.Context, kbs []*model.KnowledgeBaseM, query string) (string, error)
}

// KnowledgeBaseProvider exposes knowledge-base retrieval as an MCP tool.
type KnowledgeBaseProvider struct {
	Resources store.AIStore
	Source    KnowledgeBaseSource
}

// Namespace implements Provider.
func (p *KnowledgeBaseProvider) Namespace() string { return "kb" }

// Tools implements Provider.
func (p *KnowledgeBaseProvider) Tools() []Tool {
	return []Tool{{
		Namespace:   "kb",
		Name:        "search_knowledge_base",
		Description: "Search the agent's knowledge bases for material relevant to the question.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The question or search query"},
			},
		},
		Handler: p.search,
	}}
}

func (p *KnowledgeBaseProvider) search(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Query == "" {
		return nil, fmt.Errorf("search_knowledge_base needs a query")
	}
	agent, err := p.Resources.Agents().Get(ctx, where.F("name", claims.Agent))
	if err != nil {
		return nil, fmt.Errorf("resolve agent %q: %w", claims.Agent, err)
	}
	// A narrowed run may search exactly the knowledge bases it selected. An
	// empty narrowed list is a real "none", not a reason to fall back to the
	// agent's full set.
	names := agent.EffectiveKnowledgeBases()
	if claims.Narrowed {
		names = claims.KnowledgeBases
	}
	var kbs []*model.KnowledgeBaseM
	for _, name := range names {
		kb, err := p.Resources.KnowledgeBases().Get(ctx, model.EntryFilter(name))
		if err != nil || kb == nil {
			continue
		}
		kbs = append(kbs, kb)
	}
	if len(kbs) == 0 {
		return nil, fmt.Errorf("this run has no knowledge base to search")
	}
	text, err := p.Source.Search(ctx, kbs, input.Query)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": text}, nil
}
