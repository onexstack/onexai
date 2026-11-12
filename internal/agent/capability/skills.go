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

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
)

// SkillSource is the skill subsystem as the capability gateway needs it: list
// what an agent can load, and load one. It is an interface so the gateway does
// not depend on the concrete manager and can be tested without a filesystem.
type SkillSource interface {
	List(ctx context.Context, agentKey string, repos []*model.SkillM) []skill.ConcreteSkill
	Get(ctx context.Context, agentKey string, repos []*model.SkillM, name string) (adkskill.Skill, bool, error)
}

// ManagerSource adapts skill.Manager to SkillSource.
type ManagerSource struct {
	Manager *skill.Manager
}

// List implements SkillSource.
func (s ManagerSource) List(ctx context.Context, agentKey string, repos []*model.SkillM) []skill.ConcreteSkill {
	values := make([]model.SkillM, 0, len(repos))
	for _, repo := range repos {
		if repo != nil {
			values = append(values, *repo)
		}
	}
	return s.Manager.ListContextSkill(ctx, agentKey, values)
}

// Get implements SkillSource.
func (s ManagerSource) Get(
	ctx context.Context, agentKey string, repos []*model.SkillM, name string,
) (adkskill.Skill, bool, error) {
	for _, repo := range repos {
		if repo == nil {
			continue
		}
		backend := s.Manager.GetOrCreateBackend(ctx, agentKey, *repo)
		if backend == nil {
			continue
		}
		loaded, err := backend.Get(ctx, name)
		if err == nil {
			return loaded, true, nil
		}
	}
	return adkskill.Skill{}, false, nil
}

// SkillsProvider exposes an agent's skills as MCP tools.
type SkillsProvider struct {
	Resources store.AIStore
	Source    SkillSource
}

// Namespace implements Provider.
func (p *SkillsProvider) Namespace() string { return "skill" }

// Tools implements Provider.
func (p *SkillsProvider) Tools() []Tool {
	return []Tool{
		{
			Namespace:   "skill",
			Name:        "list_skill",
			Description: "List the skills this agent can use, with their names and descriptions.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			Handler:     p.list,
		},
		{
			Namespace:   "skill",
			Name:        "load_skill",
			Description: "Load the full instructions of one skill by name.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The skill name to load"},
				},
			},
			Handler: p.load,
		},
	}
}

func (p *SkillsProvider) list(ctx context.Context, claims Claims, _ json.RawMessage) (any, error) {
	repos, err := p.repos(ctx, claims)
	if err != nil {
		return nil, err
	}
	items := p.Source.List(ctx, claims.Agent, repos)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"name":        item.FrontMatter.Name,
			"description": item.FrontMatter.Description,
			"repo":        item.RepoName,
		})
	}
	return map[string]any{"skills": out}, nil
}

func (p *SkillsProvider) load(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Name == "" {
		return nil, fmt.Errorf("load_skill needs a skill name")
	}
	repos, err := p.repos(ctx, claims)
	if err != nil {
		return nil, err
	}
	loaded, ok, err := p.Source.Get(ctx, claims.Agent, repos, input.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("skill %q not found", input.Name)
	}
	return map[string]any{
		"name":        loaded.Name,
		"description": loaded.Description,
		"content":     loaded.Content,
	}, nil
}

func (p *SkillsProvider) repos(ctx context.Context, claims Claims) ([]*model.SkillM, error) {
	agent, err := p.Resources.Agents().Get(ctx, where.F("name", claims.Agent))
	if err != nil {
		return nil, fmt.Errorf("resolve agent %q: %w", claims.Agent, err)
	}
	// A narrowed debug run serves exactly what it selected. The token carries
	// the list; without this the gateway would answer with the agent's full
	// effective set and the narrowing would only have applied in-process.
	names := agent.EffectiveSkills()
	if claims.Narrowed {
		names = claims.Skills
	}
	repos := make([]*model.SkillM, 0, len(names))
	for _, name := range names {
		repo, err := p.Resources.Skills().Get(ctx, model.EntryFilter(name))
		if err != nil || repo == nil {
			continue
		}
		repos = append(repos, repo)
	}
	return repos, nil
}
