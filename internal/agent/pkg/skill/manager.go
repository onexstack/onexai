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
// The original repo for this file is https://github.com/onexstack/onex.

package skill

import (
	"context"
	"log/slog"
	"sync"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"golang.org/x/sync/singleflight"

	"github.com/onexstack/onexai/internal/agent/model"
)

// ConcreteSkill is one skill found inside a repository record, together with
// the record it came from. It is what a /cmd surface needs to enumerate the
// skills an agent set can run.
type ConcreteSkill struct {
	FrontMatter     adkskill.FrontMatter
	RepoName        string
	RepoDescription string
	// AsCmd mirrors the record's as_cmd column: only "Yes" exposes the skill as
	// a slash command.
	AsCmd string
}

// backendFingerprint captures the record fields that decide whether a cached
// backend is still valid. It is comparable with ==, so validity is one
// comparison rather than a field-by-field walk that a new field can silently
// fall out of.
type backendFingerprint struct {
	provider        string
	params          string
	mode            string
	contract        string
	cacheTTLSeconds int
	status          string
	updatedAt       int64
}

func fingerprintOf(repo model.SkillM) backendFingerprint {
	return backendFingerprint{
		provider:        repo.Provider,
		params:          repo.Params,
		mode:            repo.Mode,
		contract:        repo.Contract,
		cacheTTLSeconds: int(repo.CacheTTLSeconds),
		status:          repo.Status,
		updatedAt:       repo.UpdatedAt,
	}
}

type backendEntry struct {
	backend     adkskill.Backend
	fingerprint backendFingerprint
}

// Manager owns the lifetime of skill backends.
//
// Backends are keyed by (agentKey, skill name) rather than by skill alone.
// A backend checks each skill out under its own agent-isolated directory, so
// keying by skill alone would let two unrelated agent sets — which may be
// serving concurrent sessions — write to and read from the same files. The
// downloaded-archive cache inside each backend is still shared in effect,
// because rebuilding for a new agent key re-fetches only once per TTL.
//
// It implements agentbuilder.SkillMiddleware, which is how the agent builder
// asks for the middleware without knowing anything about repositories.
type Manager struct {
	skillsDir   string
	skillPrefix string
	deps        ProviderDeps

	mu       sync.RWMutex
	backends map[string]*backendEntry

	// sf collapses concurrent builds of the same key, so a burst of first
	// requests for one agent set downloads one archive rather than several.
	sf singleflight.Group
}

// NewManager returns a Manager. skillsDir is the root skills are checked out
// under; skillPrefix, when non-empty, replaces the built-in skill system
// prompt.
func NewManager(skillsDir, skillPrefix string, deps ProviderDeps) *Manager {
	return &Manager{
		skillsDir:   skillsDir,
		skillPrefix: skillPrefix,
		deps:        deps,
		backends:    make(map[string]*backendEntry),
	}
}

// GetOrCreateBackend returns the cached backend for the record, rebuilding it
// when the record has changed. It returns nil when the record cannot produce
// one.
func (m *Manager) GetOrCreateBackend(ctx context.Context, agentKey string, repo model.SkillM) adkskill.Backend {
	key := backendCacheKey(agentKey, repo.Name)
	fp := fingerprintOf(repo)

	if entry, ok := m.cached(key, fp); ok {
		return entry
	}

	result, _, _ := m.sf.Do(key, func() (any, error) {
		// Another goroutine may have built it while this one waited.
		if entry, ok := m.cached(key, fp); ok {
			return entry, nil
		}
		backend := BuildAll(ctx, []*model.SkillM{&repo}, BuildSpec{
			SkillsDir: m.skillsDir,
			AgentKey:  agentKey,
			Deps:      m.deps,
		})
		if backend == nil {
			return nil, nil
		}
		m.mu.Lock()
		m.backends[key] = &backendEntry{backend: backend, fingerprint: fp}
		m.mu.Unlock()
		return backend, nil
	})

	if result == nil {
		return nil
	}
	return result.(adkskill.Backend)
}

// cached returns the stored backend when its fingerprint still matches.
func (m *Manager) cached(key string, fp backendFingerprint) (adkskill.Backend, bool) {
	m.mu.RLock()
	entry, ok := m.backends[key]
	m.mu.RUnlock()
	if !ok || entry.fingerprint != fp {
		return nil, false
	}
	return entry.backend, true
}

// ListContextSkill enumerates every skill the records contain, deduplicated by
// name with the earliest record winning. It backs the /cmd surface; the agent
// build path uses Middleware instead.
func (m *Manager) ListContextSkill(ctx context.Context, agentKey string, repos []model.SkillM) []ConcreteSkill {
	var result []ConcreteSkill
	seen := make(map[string]struct{})
	for _, repo := range repos {
		backend := m.GetOrCreateBackend(ctx, agentKey, repo)
		if backend == nil {
			continue
		}
		frontMatters, err := backend.List(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "skill: list backend failed", "skill", repo.Name, "error", err)
			continue
		}
		for _, fm := range frontMatters {
			if _, dup := seen[fm.Name]; dup {
				continue
			}
			seen[fm.Name] = struct{}{}
			result = append(result, ConcreteSkill{
				FrontMatter:     fm,
				RepoName:        repo.Name,
				RepoDescription: repo.Description,
				AsCmd:           repo.AsCmd,
			})
		}
	}
	return result
}

// backendCacheKey builds the map key from an agent key and a skill name. The
// NUL separator cannot occur in either part, so no escaping is needed and
// ("a.b", "c") cannot collide with ("a", "b.c").
func backendCacheKey(agentKey, skillName string) string {
	return agentKey + "\x00" + skillName
}
