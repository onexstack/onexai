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
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"

	skillbiz "github.com/onexstack/onexai/internal/agent/biz/v1/skill"
	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// ProvideSkillManager builds the skill subsystem, or nil when it is off.
//
// It is a provider rather than a helper called from the two places that need
// one, because those two are the engine and the read surface and they must
// share a manager: the backends it caches are keyed by (agent, skill) and hold
// a checked-out copy on disk, so a second manager would download and unpack the
// same repository again behind the first one's back.
func ProvideSkillManager(cfg *Config, resources store.AIStore) *skill.Manager {
	return buildSkillManager(cfg, resources)
}

// ProvideContextSkillSource answers which skills an agent has exposed as
// commands.
//
// It returns the interface rather than the concrete type and is nil when skills
// are off: a deployment with no skills directory has no skills to enumerate,
// and the route that reads this answers "not configured" rather than an empty
// list — an empty list would say the agent has none, which is a different
// statement.
func ProvideContextSkillSource(resources store.AIStore, manager *skill.Manager) skillbiz.ContextSkillSource {
	if manager == nil {
		return nil
	}
	return &contextSkillSource{resources: resources, manager: manager}
}

// contextSkillSource enumerates an agent's skills from the skill subsystem.
type contextSkillSource struct {
	resources store.AIStore
	manager   *skill.Manager
}

var _ skillbiz.ContextSkillSource = (*contextSkillSource)(nil)

// ContextSkills implements skillbiz.ContextSkillSource.
func (s *contextSkillSource) ContextSkills(ctx context.Context, agentName string) ([]skillbiz.ContextSkill, error) {
	record, err := s.resources.Agents().Get(ctx, where.F("name", agentName))
	if err != nil || record == nil {
		return nil, errno.ErrNotFound.WithMessage("agent %q not found", agentName)
	}

	records := resolveSkillRecords(s.resources)(ctx, record.EffectiveSkills())
	repos := make([]model.SkillM, 0, len(records))
	// The repository kind is not on the concrete skill — it is a property of
	// the record the skill was found in — so it is carried alongside, keyed by
	// the record name the concrete skill reports.
	providerOf := make(map[string]string, len(records))
	for _, r := range records {
		if r == nil {
			continue
		}
		// A command is a way to ask for a turn, and the turn only names the
		// skill in its instruction. tool/guide skills have no body in the
		// prompt, so a command for one would name something the model cannot
		// see. Filtering here also spares those repositories the download
		// ListContextSkill would do just to enumerate commands.
		if !commandCapableMode(r.Mode) {
			continue
		}
		repos = append(repos, *r)
		providerOf[r.Name] = r.Provider
	}
	if len(repos) == 0 {
		return nil, nil
	}

	// The key has to be the one a run of this agent would use: the skills are
	// read out of the directory that run checks them out into, and a different
	// key would look in a directory nobody has populated.
	agentKey := agentbuilder.NormalizeAgentKey([]string{agentName}, agentbuilder.DefaultAgentKey)
	concrete := s.manager.ListContextSkill(ctx, agentKey, repos)

	out := make([]skillbiz.ContextSkill, 0, len(concrete))
	for _, c := range concrete {
		// Only the skills whose repository record says so are commands. The
		// rest are still available to the model; they are simply not something
		// a person model.
		if c.AsCmd != cmdEnabled {
			continue
		}
		// The menu shows the skill's own summary, or the repository's when the
		// skill carries none: a command with no description is one a person has
		// to try to understand.
		description := c.FrontMatter.Description
		if strings.TrimSpace(description) == "" {
			description = c.RepoDescription
		}
		out = append(out, skillbiz.ContextSkill{
			Name:        c.FrontMatter.Name,
			Description: description,
			Repo:        c.RepoName,
			Provider:    providerOf[c.RepoName],
		})
	}
	return out, nil
}

// cmdEnabled is the skill record's as_cmd value that exposes a skill as a
// command.
const cmdEnabled = "Yes"

// commandCapableMode reports whether a skill record's mode can produce a chat
// command. An empty mode is a record written before the field existed, which
// behaves like hosted.
func commandCapableMode(mode string) bool {
	return mode == "" || mode == "hosted"
}
