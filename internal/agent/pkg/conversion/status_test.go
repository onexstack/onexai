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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestStatusToV1(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"legacy enabled spelling", "", model.StatusEnabled},
		{"already enabled", model.StatusEnabled, model.StatusEnabled},
		{"disabled", model.StatusDisabled, model.StatusDisabled},
		{"unknown is not disabled", "pending", model.StatusEnabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, StatusToV1(tc.in))
		})
	}
}

// Every resource response must carry the wire spelling, not the store spelling.
func TestToV1NormalizesEnabledStatus(t *testing.T) {
	assert.Equal(t, model.StatusEnabled, AgentModelToAgentV1(&model.AgentM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, AgentTemplateModelToAgentTemplateV1(&model.AgentTemplateM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, AuthzRuleModelToAuthzRuleV1(&model.AuthzRuleM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, KnowledgeBaseModelToKnowledgeBaseV1(&model.KnowledgeBaseM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, MCPModelToMCPV1(&model.MCPM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, ModelModelToModelV1(&model.ModelM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, PromptModelToPromptV1(&model.PromptM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, RuntimeProfileModelToRuntimeProfileV1(&model.RuntimeProfileM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, RuntimeHostingModelToRuntimeHostingV1(&model.RuntimeHostingM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, SandboxTemplateModelToSandboxTemplateV1(&model.SandboxTemplateM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, ScheduledRunModelToScheduledRunV1(&model.ScheduledRunM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, SkillModelToSkillV1(&model.SkillM{}).GetStatus())
	assert.Equal(t, model.StatusEnabled, WebhookModelToWebhookV1(&model.WebhookM{}).GetStatus())

	assert.Equal(t, model.StatusDisabled, ModelModelToModelV1(&model.ModelM{Status: model.StatusDisabled}).GetStatus())
	assert.Equal(t, model.StatusDisabled, AgentModelToAgentV1(&model.AgentM{Status: model.StatusDisabled}).GetStatus())
}

func TestScheduledRunNextScheduledAtRoundTrip(t *testing.T) {
	row := &model.ScheduledRunM{Name: "daily", NextScheduledAt: 1762500000000}
	assert.Equal(t, int64(1762500000000), ScheduledRunModelToScheduledRunV1(row).GetNextScheduledAt())

	// The field is computed on read; an incoming body must not be able to
	// overwrite it through the write conversion.
	assert.Zero(t, ScheduledRunV1ToScheduledRunModel(ScheduledRunModelToScheduledRunV1(row)).NextScheduledAt)
}
