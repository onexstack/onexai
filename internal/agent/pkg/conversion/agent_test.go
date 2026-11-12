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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// AgentV1ToAgentModel reads the ordered prompts list, and an absent list means
// "no opinion" rather than "clear the bindings".
func TestAgentV1ToAgentModelPromptRefs(t *testing.T) {
	tests := []struct {
		name  string
		agent *v1.Agent
		want  model.StringList
	}{
		{
			name:  "ordered list",
			agent: &v1.Agent{Prompts: objectRefs("one", "two")},
			want:  model.StringList{"helper-public/one", "helper-public/two"},
		},
		{
			name:  "absent is nil",
			agent: &v1.Agent{},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := AgentV1ToAgentModel(tt.agent)
			assert.Equal(t, tt.want, row.PromptRefs)
		})
	}
}

// The response carries the ordered list as prompts; there is no single-value
// projection any more.
func TestAgentModelToAgentV1PromptRefs(t *testing.T) {
	row := &model.AgentM{PromptRefs: model.StringList{"helper-public/one", "helper-public/two"}}
	got := AgentModelToAgentV1(row)
	assert.Equal(t, objectRefs("one", "two"), got.GetPrompts())

	empty := AgentModelToAgentV1(&model.AgentM{})
	assert.Empty(t, empty.GetPrompts())
}

// Template refs keep the nil/empty distinction: nil means "no opinion" (leave
// the spec alone), while an explicit empty list means "clear the binding".
func TestAgentV1ToAgentModelTemplateRefs(t *testing.T) {
	tests := []struct {
		name  string
		agent *v1.Agent
		want  model.StringList
	}{
		{
			name:  "ordered list",
			agent: &v1.Agent{TemplateRefs: objectRefs("base", "ops")},
			want:  model.StringList{"helper-public/base", "helper-public/ops"},
		},
		{
			name:  "absent is nil",
			agent: &v1.Agent{},
			want:  nil,
		},
		{
			name:  "explicit empty is not nil",
			agent: &v1.Agent{TemplateRefs: []*v1.ObjectReference{}},
			want:  model.StringList{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := AgentV1ToAgentModel(tt.agent)
			assert.Equal(t, tt.want, row.TemplateRefs)
		})
	}
}

func TestAgentModelToAgentV1TemplateRefs(t *testing.T) {
	row := &model.AgentM{TemplateRefs: model.StringList{"helper-public/base", "helper-public/ops"}}
	assert.Equal(t, objectRefs("base", "ops"), AgentModelToAgentV1(row).GetTemplateRefs())

	empty := AgentModelToAgentV1(&model.AgentM{})
	assert.Empty(t, empty.GetTemplateRefs())
}

// Delegates follow the same nil/empty contract as templateRefs: nil means "no
// opinion" (leave the spec alone), while an explicit empty list clears the
// delegation.
func TestAgentV1ToAgentModelDelegates(t *testing.T) {
	tests := []struct {
		name  string
		agent *v1.Agent
		want  model.StringList
	}{
		{
			name:  "ordered list",
			agent: &v1.Agent{Delegates: objectRefs("researcher", "reviewer")},
			want:  model.StringList{"helper-public/researcher", "helper-public/reviewer"},
		},
		{
			name:  "absent is nil",
			agent: &v1.Agent{},
			want:  nil,
		},
		{
			name:  "explicit empty is not nil",
			agent: &v1.Agent{Delegates: []*v1.ObjectReference{}},
			want:  model.StringList{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := AgentV1ToAgentModel(tt.agent)
			assert.Equal(t, tt.want, row.Delegates)
		})
	}
}

func TestAgentModelToAgentV1Delegates(t *testing.T) {
	row := &model.AgentM{Delegates: model.StringList{"helper-public/researcher", "helper-public/reviewer"}}
	assert.Equal(t, objectRefs("researcher", "reviewer"), AgentModelToAgentV1(row).GetDelegates())

	empty := AgentModelToAgentV1(&model.AgentM{})
	assert.Empty(t, empty.GetDelegates())
}

// Webhooks are bare names and follow the same nil/empty contract as the
// reference lists: nil leaves the spec alone, an explicit empty list clears it.
func TestAgentV1ToAgentModelWebhooks(t *testing.T) {
	tests := []struct {
		name  string
		agent *v1.Agent
		want  model.StringList
	}{
		{
			name:  "ordered list",
			agent: &v1.Agent{Webhooks: []string{"audit", "quota"}},
			want:  model.StringList{"audit", "quota"},
		},
		{
			name:  "absent is nil",
			agent: &v1.Agent{},
			want:  nil,
		},
		{
			name:  "explicit empty is not nil",
			agent: &v1.Agent{Webhooks: []string{}},
			want:  model.StringList{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := AgentV1ToAgentModel(tt.agent)
			assert.Equal(t, tt.want, row.Webhooks)
			if tt.want == nil {
				assert.Nil(t, row.Webhooks)
				return
			}
			assert.NotNil(t, row.Webhooks)
		})
	}
}

func TestAgentModelToAgentV1Webhooks(t *testing.T) {
	row := &model.AgentM{Webhooks: model.StringList{"audit", "quota"}}
	assert.Equal(t, []string{"audit", "quota"}, AgentModelToAgentV1(row).GetWebhooks())

	// An empty list must stay non-nil, otherwise a GET-then-PUT round trip
	// would turn "clear the hooks" into "no opinion".
	assert.NotNil(t, AgentModelToAgentV1(&model.AgentM{Webhooks: model.StringList{}}).GetWebhooks())
	assert.Nil(t, AgentModelToAgentV1(&model.AgentM{}).GetWebhooks())
}

func TestAgentA2AClientConfigRoundTrip(t *testing.T) {
	in := &v1.Agent{
		Name: "remote",
		Execution: &v1.AgentExecution{
			Mode: "a2a",
			A2A: &v1.A2AExecution{
				ClientConfig: &v1.ClientConfig{
					Url:            "https://a2a.example.com",
					TimeoutSeconds: 15,
				},
			},
		},
	}

	row := AgentV1ToAgentModel(in)
	require.NotEmpty(t, row.ExecutionJSON)
	var stored model.AgentExecutionM
	require.NoError(t, json.Unmarshal([]byte(row.ExecutionJSON), &stored))
	require.NotNil(t, stored.A2A)
	require.NotEmpty(t, stored.A2A.ClientConfig)

	out := AgentModelToAgentV1(row)
	require.NotNil(t, out.Execution)
	require.NotNil(t, out.Execution.A2A)
	require.NotNil(t, out.Execution.A2A.ClientConfig)
	assert.Equal(t, "https://a2a.example.com", out.Execution.A2A.ClientConfig.Url)
	assert.Equal(t, int32(15), out.Execution.A2A.ClientConfig.TimeoutSeconds)
}

func objectRefs(names ...string) []*v1.ObjectReference {
	out := make([]*v1.ObjectReference, 0, len(names))
	for _, name := range names {
		out = append(out, &v1.ObjectReference{Name: name})
	}
	return out
}
