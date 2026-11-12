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
	"time"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"

	"github.com/onexstack/onexai/internal/agent/model"
)

// BuildAll builds one backend covering every usable skill record.
//
// It returns nil when no record produced a backend, which callers read as "no
// skills on this run" rather than as an error: a run whose agents declare no
// skills, or declare only disabled ones, is a normal run.
//
// A record that is disabled, names an unknown provider, or carries params its
// provider rejects is skipped with a log line. That is deliberate at this
// level: the alternative — failing the build — would let one stale record
// disable an agent that has several other working skills.
func BuildAll(ctx context.Context, skills []*model.SkillM, spec BuildSpec) adkskill.Backend {
	backends := make([]adkskill.Backend, 0, len(skills))
	for _, s := range skills {
		if s == nil || !s.Enabled() {
			continue
		}
		recordSpec := spec
		recordSpec.CacheTTL = cacheTTLFor(s, spec.CacheTTL)
		if b := buildBackend(ctx, s, recordSpec); b != nil {
			backends = append(backends, b)
		}
	}

	switch len(backends) {
	case 0:
		return nil
	case 1:
		return backends[0]
	default:
		return NewCombinedBackend(backends...)
	}
}

// cacheTTLFor resolves a record's cache lifetime: its own value when set,
// otherwise the deployment default. A negative TTL means "never cache", which
// the cache honours as a load-per-call.
func cacheTTLFor(s *model.SkillM, fallback time.Duration) time.Duration {
	if s.CacheTTLSeconds > 0 {
		return time.Duration(s.CacheTTLSeconds) * time.Second
	}
	if fallback <= 0 {
		return defaultCacheTTL
	}
	return fallback
}
