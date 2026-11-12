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

// PromptV1ToPromptModel converts a proto Prompt to the domain model.
func PromptV1ToPromptModel(p *v1.Prompt) *model.PromptM {
	if p == nil {
		return &model.PromptM{}
	}
	return &model.PromptM{
		Name:        p.GetName(),
		DisplayName: p.GetDisplayName(),
		Status:      p.GetStatus(),
		Description: p.GetDescription(),
		Content:     p.GetContent(),
		Namespace:   p.GetNamespace(),
		Labels:      p.GetLabels(),
		CreatedAt:   p.GetCreatedAt(),
		UpdatedAt:   p.GetUpdatedAt(),
	}
}

// PromptModelToPromptV1 converts the domain model back to the wire shape.
func PromptModelToPromptV1(m *model.PromptM) *v1.Prompt {
	if m == nil {
		return nil
	}
	return &v1.Prompt{
		Name:        m.Name,
		DisplayName: m.DisplayName,
		Status:      StatusToV1(m.Status),
		Description: m.Description,
		Content:     m.Content,
		Namespace:   m.Namespace,
		Labels:      m.Labels,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}
