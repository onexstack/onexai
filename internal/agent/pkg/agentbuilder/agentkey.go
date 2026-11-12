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

package agentbuilder

import (
	"sort"
	"strings"

	"github.com/onexstack/onexai/internal/agent/pkg/skilldir"
)

// NormalizeAgentKey returns a deterministic identifier for a set of agent
// names: deduplicated, sorted, and dot-joined (e.g. "bke-agent.sre-agent").
// The result is stable regardless of input order or duplicate entries, so
// merging the same agent set via different call paths always yields the same
// key.
//
// This is used both as deep.Config.Name (the runner's display/log name) and
// as the Skill agentKey that isolates each agent-set's local skill working
// directory on disk (see skill.Manager.GetOrCreateBackend). Returns fallback
// when agentNames resolves to an empty set.
func NormalizeAgentKey(agentNames []string, fallback string) string {
	seen := make(map[string]struct{}, len(agentNames))
	names := make([]string, 0, len(agentNames))
	for _, n := range agentNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	sort.Strings(names)
	key := strings.Join(names, ".")
	if key == "" {
		return fallback
	}
	return key
}

// IsolatedSkillsDir returns the agent-set-specific skill working directory
// under skillsDir for the given agentKey (the agentKey produced by
// NormalizeAgentKey).
//
// It delegates to pkg/skilldir, which owns the layout: the same path is the
// LLM-facing project root, the sandbox allow-list, and where skill backends
// check out files, and the three must never drift apart. It is re-exported here
// because callers of the builder think in terms of the agent key rather than of
// the skills package.
func IsolatedSkillsDir(skillsDir, agentKey string) string {
	return skilldir.Isolated(skillsDir, agentKey)
}
