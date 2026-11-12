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
	"encoding/json"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// AgentTemplateV1ToAgentTemplateModel decodes the declarative spec carried by
// the REST message.
//
// The spec is JSON rather than a second set of proto fields: the behavior model
// is expected to grow, and a proto that re-modelled it would make every new
// field a two-place change that can drift. The same JSON is what the API server
// stores, so a REST round-trip preserves fields the console does not know yet.
func AgentTemplateV1ToAgentTemplateModel(p *v1.AgentTemplate) (*model.AgentTemplateM, error) {
	if p == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("agentTemplate is required")
	}

	var spec agentv1alpha1.AgentTemplateSpec
	raw := p.GetSpec()
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return nil, errno.ErrInvalidArgument.WithMessage("spec is not a valid AgentTemplateSpec: %s", err.Error())
		}
	}

	// The validator filled these from the decoded spec when the envelope
	// omitted them.
	displayName := p.GetDisplayName()
	description := p.GetDescription()

	status := p.GetStatus()
	// The declarative spec is the source of truth for enabled; the row's status
	// column is how the control-plane store spells it.
	if raw != "" {
		status = ""
		if !spec.Enabled {
			status = model.StatusDisabled
		}
	}

	return &model.AgentTemplateM{
		Name:        p.GetName(),
		DisplayName: displayName,
		Description: description,
		Status:      status,
		Spec:        raw,
		Namespace:   p.GetNamespace(),
		Labels:      p.GetLabels(),
		CreatedAt:   p.GetCreatedAt(),
		UpdatedAt:   p.GetUpdatedAt(),
	}, nil
}

// AgentTemplateModelToAgentTemplateV1 converts the domain model back to the wire
// shape.
func AgentTemplateModelToAgentTemplateV1(tmpl *model.AgentTemplateM) *v1.AgentTemplate {
	if tmpl == nil {
		return nil
	}
	return &v1.AgentTemplate{
		Name:        tmpl.Name,
		DisplayName: tmpl.DisplayName,
		Status:      StatusToV1(tmpl.Status),
		Description: tmpl.Description,
		Namespace:   tmpl.Namespace,
		Labels:      tmpl.Labels,
		CreatedAt:   tmpl.CreatedAt,
		UpdatedAt:   tmpl.UpdatedAt,
		Spec:        tmpl.Spec,
	}
}
