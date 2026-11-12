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

package conversion

import (
	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// SkillV1ToSkillModel converts a proto Skill to the domain model.
func SkillV1ToSkillModel(p *v1.Skill) *model.SkillM {
	if p == nil {
		return &model.SkillM{}
	}
	return &model.SkillM{
		Name:            p.GetName(),
		DisplayName:     p.GetDisplayName(),
		Status:          p.GetStatus(),
		Description:     p.GetDescription(),
		Provider:        p.GetProvider(),
		Params:          p.GetParams(),
		CacheTTLSeconds: int(p.GetCacheTtlSeconds()),
		AsCmd:           p.GetAsCmd(),
		Namespace:       p.GetNamespace(),
		Mode:            p.GetMode(),
		Contract:        p.GetContract(),
		Entrypoint:      p.GetEntrypoint(),
		Labels:          p.GetLabels(),
		CreatedAt:       p.GetCreatedAt(),
		UpdatedAt:       p.GetUpdatedAt(),
	}
}

// SkillModelToSkillV1 converts the domain model back to the wire shape.
//
// A guide or tool skill never hands its body (or the repository it lives in) to
// a caller. Params is where the repository URL and token live, so blanking it is
// part of the mode, not an admin convenience toggle.
func SkillModelToSkillV1(s *model.SkillM) *v1.Skill {
	if s == nil {
		return nil
	}
	params := s.Params
	if s.Mode == "tool" || s.Mode == "guide" {
		params = ""
	}
	return &v1.Skill{
		Name:            s.Name,
		DisplayName:     s.DisplayName,
		Status:          StatusToV1(s.Status),
		Description:     s.Description,
		Provider:        s.Provider,
		Params:          params,
		CacheTtlSeconds: int32(s.CacheTTLSeconds),
		AsCmd:           s.AsCmd,
		Namespace:       s.Namespace,
		Mode:            s.Mode,
		Contract:        s.Contract,
		Entrypoint:      s.Entrypoint,
		Labels:          s.Labels,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}
