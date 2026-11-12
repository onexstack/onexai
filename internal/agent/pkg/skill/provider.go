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
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	larkv3 "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
)

// defaultCacheTTL is how long a downloaded skill archive is served before it
// is re-fetched, for a record that does not set its own.
const defaultCacheTTL = 10 * time.Minute

// Provider names as they appear in a skill record's Provider field.
const (
	ProviderGitHub  = "github"
	ProviderGitLab  = "gitlab"
	ProviderLarkDoc = "lark_doc"
)

// ProviderDeps are the collaborators a provider factory may need. A provider
// that needs none simply ignores it, which keeps the factory signature uniform
// across providers that reach very different systems.
type ProviderDeps struct {
	// LarkClient is used by the lark_doc provider to read a Feishu document.
	LarkClient *larkv3.Client
	// Secrets resolves a record's token_secret_id. Nil means only inline tokens
	// can be used.
	Secrets SecretLookup
}

// ProviderSpec is one skill source: how to recognize it and how to build a
// backend for it. A new source is one file plus one Register call — nothing
// else in the package needs to change.
type ProviderSpec struct {
	// Name is the canonical provider name, matched case-insensitively.
	Name string
	// Aliases are additional accepted spellings (e.g. "feishu_doc" for
	// "lark_doc") so an existing record keeps working under a renamed provider.
	Aliases []string
	// Build constructs the backend. It receives the record's decoded params so
	// each provider can define its own shape; an empty or malformed params
	// string is an error the caller reports and skips.
	Build func(ctx context.Context, skill *model.SkillM, spec BuildSpec) (FullBackend, error)
}

// BuildSpec is what every provider needs to place a skill on disk.
type BuildSpec struct {
	// SkillsDir is the root skills are persisted under.
	SkillsDir string
	// AgentKey isolates this agent set's checked-out files.
	AgentKey string
	// CacheTTL is how long the downloaded content is served.
	CacheTTL time.Duration
	// Deps are the shared collaborators.
	Deps ProviderDeps
}

var (
	providersMu sync.RWMutex
	providers   = map[string]ProviderSpec{}
)

// Register adds a provider. It is called from each provider file's init, so
// importing the package is what makes its providers available.
//
// It panics on a duplicate name: two implementations claiming the same
// provider would make which one wins depend on init order, which is exactly
// the kind of bug that only appears in one binary.
func Register(spec ProviderSpec) {
	if spec.Name == "" || spec.Build == nil {
		panic("skill: Register requires a name and a Build function")
	}
	providersMu.Lock()
	defer providersMu.Unlock()
	for _, name := range append([]string{spec.Name}, spec.Aliases...) {
		key := normalizeProvider(name)
		if _, dup := providers[key]; dup {
			panic("skill: duplicate provider registration for " + key)
		}
		providers[key] = spec
	}
}

// lookupProvider finds the spec for a provider name, or false when the name is
// unknown. Callers report an unknown provider rather than silently skipping the
// skill, because a typo in a record is otherwise indistinguishable from a
// repository with no skills in it.
func lookupProvider(name string) (ProviderSpec, bool) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	spec, ok := providers[normalizeProvider(name)]
	return spec, ok
}

// RegisteredProviders lists the canonical provider names, sorted. It exists so
// a startup log can tell an operator what this build supports.
func RegisteredProviders() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	seen := make(map[string]struct{}, len(providers))
	names := make([]string, 0, len(providers))
	for _, spec := range providers {
		if _, ok := seen[spec.Name]; ok {
			continue
		}
		seen[spec.Name] = struct{}{}
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}

// normalizeProvider lowercases and trims a provider name for lookup.
func normalizeProvider(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// decodeParams unmarshals a record's params JSON into shape.
func decodeParams(params string, shape any) error {
	if strings.TrimSpace(params) == "" {
		return fmt.Errorf("params is empty")
	}
	if err := json.Unmarshal([]byte(params), shape); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

// buildBackend turns one skill record into its backend, or nil when the record
// cannot produce one. Every failure is logged with enough context to fix the
// record, and none of them aborts the caller: one unusable skill must not
// deprive a run of the others.
func buildBackend(ctx context.Context, s *model.SkillM, spec BuildSpec) FullBackend {
	backend, provider, err := buildBackendE(ctx, s, spec)
	if err != nil {
		if provider != "" {
			metrics.M.ObserveSkillBuild(provider, "error")
		}
		slog.WarnContext(ctx, "skill: failed to build backend, skipping skill",
			"skill", s.Name, "provider", s.Provider, "error", err)
		return nil
	}
	metrics.M.ObserveSkillBuild(provider, "ok")
	return backend
}

// buildBackendE is buildBackend with the reason kept.
//
// It exists for the health probe: the run path must swallow a per-record failure
// so one bad row cannot disable an agent, while a probe has to report it. The
// provider name is returned alongside the error so the caller can attribute a
// failure even when the build did not get far enough to name it.
func buildBackendE(ctx context.Context, s *model.SkillM, spec BuildSpec) (FullBackend, string, error) {
	// A guide skill hands out only its contract. The contract is the source, so
	// it must not require a provider backend: a repository the caller cannot
	// reach must not turn a complete guide into a missing skill.
	if s.Mode == "guide" {
		backend, err := newContractBackend(s)
		if err != nil {
			return nil, "guide", err
		}
		return backend, "guide", nil
	}

	registered, ok := lookupProvider(s.Provider)
	if !ok {
		return nil, "", fmt.Errorf("unsupported provider %q (supported: %v)",
			s.Provider, RegisteredProviders())
	}
	backend, err := registered.Build(ctx, s, spec)
	if err != nil {
		return nil, registered.Name, err
	}
	return backend, registered.Name, nil
}
