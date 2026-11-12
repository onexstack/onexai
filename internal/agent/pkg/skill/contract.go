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
	"strings"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"

	"github.com/onexstack/onexai/internal/agent/model"
)

// contractBackend serves a guide skill's contract without a provider backend.
//
// A guide skill exists to hand a compact contract to a caller that will do the
// work itself; its body is deliberately not loaded into the prompt. Requiring a
// repository for it would make an unreachable source look like a missing skill
// even though the contract is complete on its own.
type contractBackend struct {
	skill adkskill.Skill
}

// newContractBackend wraps a guide record in a FullBackend. The filesystem and
// shell methods come from CombinedBackend, which reports their absence rather
// than panicking; a guide has no files to serve.
func newContractBackend(s *model.SkillM) (FullBackend, error) {
	if s == nil {
		return nil, fmt.Errorf("guide: no skill record")
	}
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("guide: skill name is empty")
	}
	if strings.TrimSpace(s.Contract) == "" {
		return nil, fmt.Errorf("guide: contract is empty")
	}
	return NewCombinedBackend(&contractBackend{
		skill: adkskill.Skill{
			FrontMatter: adkskill.FrontMatter{
				Name:        s.Name,
				Description: s.Description,
			},
			Content: s.Contract,
		},
	}), nil
}

// List implements adkskill.Backend.
func (b *contractBackend) List(_ context.Context) ([]adkskill.FrontMatter, error) {
	return []adkskill.FrontMatter{b.skill.FrontMatter}, nil
}

// Get implements adkskill.Backend.
func (b *contractBackend) Get(_ context.Context, name string) (adkskill.Skill, error) {
	if name != b.skill.Name {
		return adkskill.Skill{}, fmt.Errorf("skill %q not found", name)
	}
	return b.skill, nil
}
