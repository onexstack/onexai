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

package skill

import (
	"context"
	"fmt"

	"github.com/onexstack/onexai/internal/agent/model"
)

// probeAgentKey is the directory segment a health probe's checkout uses. It is
// reserved-looking on purpose: it must never collide with an agent set's key,
// because the two would then write to the same skill directory.
const probeAgentKey = "__health__"

// Probe resolves one skill record and lists what its source contains, returning
// how many skills were found.
//
// BuildAll deliberately swallows a per-record failure so one stale record cannot
// break a run; a health check has the opposite job, so this reports the reason.
//
// The backend is cached under probeAgentKey, which is what keeps the probe from
// being a download per interval: the archive the first probe fetched serves the
// following ones until its TTL lapses. A source that breaks after a successful
// download therefore keeps reporting what a run would see — the cached archive —
// rather than what a fresh download would, which is the same trade the run path
// makes.
func (m *Manager) Probe(ctx context.Context, repo model.SkillM) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("skill: no manager")
	}
	if !repo.Enabled() {
		return 0, fmt.Errorf("skill: %q is disabled", repo.Name)
	}

	key := backendCacheKey(probeAgentKey, repo.Name)
	fingerprint := fingerprintOf(repo)

	backend, ok := m.cached(key, fingerprint)
	if !ok {
		built, provider, err := buildBackendE(ctx, &repo, BuildSpec{
			SkillsDir: m.skillsDir,
			AgentKey:  probeAgentKey,
			CacheTTL:  cacheTTLFor(&repo, 0),
			Deps:      m.deps,
		})
		if err != nil {
			return 0, fmt.Errorf("skill: the %s source of %q could not be built: %w", provider, repo.Name, err)
		}
		backend = built
		m.mu.Lock()
		m.backends[key] = &backendEntry{backend: built, fingerprint: fingerprint}
		m.mu.Unlock()
	}

	skills, err := backend.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("skill: the %s source of %q did not answer: %w", repo.Provider, repo.Name, err)
	}
	return len(skills), nil
}
