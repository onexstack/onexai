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

package agent

import (
	"context"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// fakeAIStore embeds the no-op empty store and overrides the agent,
// prompt and MCP accessors so instruction, MCP and approval resolution can be
// tested without a backend.
type fakeAIStore struct {
	store.AIStore
	agents  map[string]*model.AgentM
	prompts map[string]*model.PromptM
	mcps    map[string]*model.MCPM
}

func (f *fakeAIStore) Agents() store.AgentStore   { return &fakeAgentStore{f.agents} }
func (f *fakeAIStore) Prompts() store.PromptStore { return &fakePromptStore{f.prompts} }
func (f *fakeAIStore) MCPs() store.MCPStore       { return &fakeMCPStore{f.mcps} }

type fakeMCPStore struct{ m map[string]*model.MCPM }

func (s *fakeMCPStore) Get(_ context.Context, opts *where.Options) (*model.MCPM, error) {
	name, _ := opts.Filters["name"].(string)
	m, ok := s.m[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return m, nil
}
func (s *fakeMCPStore) List(_ context.Context, opts *where.Options) (int64, []*model.MCPM, error) {
	out := make([]*model.MCPM, 0, len(s.m))
	for _, m := range s.m {
		out = append(out, m)
	}
	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	total, page := store.Page(out, offset, limit)
	return total, page, nil
}
func (s *fakeMCPStore) Create(context.Context, *model.MCPM) error    { return nil }
func (s *fakeMCPStore) Update(context.Context, *model.MCPM) error    { return nil }
func (s *fakeMCPStore) Delete(context.Context, *where.Options) error { return nil }

type fakeAgentStore struct{ m map[string]*model.AgentM }

func (s *fakeAgentStore) Get(_ context.Context, opts *where.Options) (*model.AgentM, error) {
	name, _ := opts.Filters["name"].(string)
	a, ok := s.m[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return a, nil
}
func (s *fakeAgentStore) List(_ context.Context, opts *where.Options) (int64, []*model.AgentM, error) {
	out := make([]*model.AgentM, 0, len(s.m))
	for _, a := range s.m {
		out = append(out, a)
	}
	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	total, page := store.Page(out, offset, limit)
	return total, page, nil
}
func (s *fakeAgentStore) Create(context.Context, *model.AgentM) error  { return nil }
func (s *fakeAgentStore) Update(context.Context, *model.AgentM) error  { return nil }
func (s *fakeAgentStore) Delete(context.Context, *where.Options) error { return nil }
func (s *fakeAgentStore) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}

type fakePromptStore struct{ m map[string]*model.PromptM }

func (s *fakePromptStore) Get(_ context.Context, opts *where.Options) (*model.PromptM, error) {
	name, _ := opts.Filters["name"].(string)
	p, ok := s.m[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}
func (s *fakePromptStore) List(_ context.Context, opts *where.Options) (int64, []*model.PromptM, error) {
	out := make([]*model.PromptM, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	total, page := store.Page(out, offset, limit)
	return total, page, nil
}
func (s *fakePromptStore) Create(context.Context, *model.PromptM) error { return nil }
func (s *fakePromptStore) Update(context.Context, *model.PromptM) error { return nil }
func (s *fakePromptStore) Delete(context.Context, *where.Options) error { return nil }

func newFakeResources() *fakeAIStore {
	return &fakeAIStore{
		AIStore: store.NewEmptyStore(),
		mcps:    map[string]*model.MCPM{},
		agents: map[string]*model.AgentM{
			"builtin":    {Name: "builtin"},
			"empty":      {Name: "empty"},
			"named":      {Name: "named", PromptRefs: model.StringList{"ops_prompt"}},
			"aggregated": {Name: "aggregated", PromptRefs: model.StringList{"ops_prompt"}, AggregatedPrompt: "agg_prompt"},
			"ordered":    {Name: "ordered", PromptRefs: model.StringList{"missing", "ops_prompt"}},
			"disabled-first": {
				Name:       "disabled-first",
				PromptRefs: model.StringList{"disabled_prompt", "ops_prompt"},
			},
			"unavailable": {Name: "unavailable", PromptRefs: model.StringList{"missing"}},
		},
		prompts: map[string]*model.PromptM{
			"ops_prompt":      {Name: "ops_prompt", Content: "You are an SRE."},
			"agg_prompt":      {Name: "agg_prompt", Content: "You are the merged persona."},
			"disabled_prompt": {Name: "disabled_prompt", Status: model.StatusDisabled, Content: "You are disabled."},
		},
	}
}

func TestResolveInstruction(t *testing.T) {
	res := newFakeResources()
	ctx := context.Background()

	cases := []struct {
		name    string
		agents  []string
		want    string
		wantAny bool // when true, only assert non-empty (joined multi-agent case)
	}{
		{"nil resources, no agents", nil, "", false},
		{"no agents", []string{}, "", false},
		{"unknown agent", []string{"nope"}, "", false},
		{"builtin agent", []string{"builtin"}, "", false},
		{"no prompt refs", []string{"empty"}, "", false},
		{"named prompt", []string{"named"}, "You are an SRE.", false},
		{"aggregated prompt wins", []string{"aggregated"}, "You are the merged persona.", false},
		{"first resolvable ref wins", []string{"ordered"}, "You are an SRE.", false},
		{"disabled ref is skipped", []string{"disabled-first"}, "You are an SRE.", false},
		{"all refs unavailable", []string{"unavailable"}, "", false},
		{"multiple join", []string{"named", "aggregated"}, "You are an SRE.\n\nYou are the merged persona.", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewResolver(res, nil).Resolve(ctx, tc.agents).Instruction
			if got != tc.want {
				t.Fatalf("resolveInstruction(%v) = %q, want %q", tc.agents, got, tc.want)
			}
		})
	}
}

func TestResolveInstructionNilResources(t *testing.T) {
	if got := NewResolver(nil, nil).Resolve(context.Background(), []string{"named"}).Instruction; got != "" {
		t.Fatalf("expected empty instruction with nil resources, got %q", got)
	}
}

func TestPromptContent(t *testing.T) {
	res := newFakeResources()
	ctx := context.Background()

	if got := promptContent(ctx, res, "__builtin__"); got != "" {
		t.Fatalf("__builtin__ should resolve empty, got %q", got)
	}
	if got := promptContent(ctx, res, ""); got != "" {
		t.Fatalf("empty should resolve empty, got %q", got)
	}
	if got := promptContent(ctx, res, "ops_prompt"); got != "You are an SRE." {
		t.Fatalf("named prompt resolved to %q", got)
	}
	if got := promptContent(ctx, res, "some inline text"); got != "" {
		t.Fatalf("a name is not a prompt; got %q", got)
	}
}
