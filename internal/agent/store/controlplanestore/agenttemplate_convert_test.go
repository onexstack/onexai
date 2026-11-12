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

package controlplanestore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// A template round-trips through the row without losing the parts of the spec
// the REST surface does not model.
func TestAgentTemplateRoundTrip(t *testing.T) {
	cr := &agentv1alpha1.AgentTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "default"},
		Spec: agentv1alpha1.AgentTemplateSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "Base"},
			Skills:     []agentv1alpha1.Reference{{Name: "triage"}},
			ModelRef:   &agentv1alpha1.Reference{Name: "gpt"},
			MemoryRef:  &agentv1alpha1.Reference{Name: "embed-memory"},
		},
	}

	row := agentTemplateFromCR(cr)
	require.NotEmpty(t, row.Spec)
	assert.Empty(t, row.Status, "an enabled template has the empty status spelling")

	out := &agentv1alpha1.AgentTemplate{}
	require.NoError(t, AgentTemplateToCR(&row, out))

	assert.Equal(t, cr.Spec, out.Spec)
	assert.Equal(t, cr.Spec.DisplayName, out.Spec.DisplayName)
}

func TestAgentTemplateDisabledRoundTrip(t *testing.T) {
	cr := &agentv1alpha1.AgentTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "default"},
		Spec:       agentv1alpha1.AgentTemplateSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: false}},
	}

	row := agentTemplateFromCR(cr)
	assert.Equal(t, model.StatusDisabled, row.Status)

	out := &agentv1alpha1.AgentTemplate{}
	require.NoError(t, AgentTemplateToCR(&row, out))
	assert.False(t, out.Spec.Enabled)
}

// The resolved scalar fields are what the runtime reads; the declared fields
// stay the spec view so an admin write cannot copy template behavior into the
// agent's own spec.
func TestAgentFromCRCarriesResolvedTemplateBehavior(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "default"},
		Spec:       agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
		Status: agentv1alpha1.AgentStatus{
			ObservedGeneration: 1,
			Resolved: agentv1alpha1.AgentResolved{
				Templates:          []string{"base"},
				Model:              "template-model",
				Delegates:          []string{"worker"},
				SessionTools:       true,
				OutputSchema:       &apiextensionsv1.JSON{Raw: []byte(`{"type":"object"}`)},
				OutputSchemaStrict: true,
				Memory: &agentv1alpha1.AgentMemorySpec{
					Enabled:        true,
					EmbeddingModel: "embed",
					TTLDays:        7,
				},
			},
		},
	}

	row := agentFromCR(cr)

	// A legacy status carried a bare model name; it resolves to the agent's
	// namespace so the runtime never has to guess which namespace's row it is.
	assert.Equal(t, "default/template-model", row.EffectiveModel())
	assert.Empty(t, row.Model, "the declared model is still the spec view")
	assert.Equal(t, model.StringList{"base"}, row.EffectiveTemplates())
	assert.Equal(t, model.StringList{"worker"}, row.EffectiveDelegates())
	assert.True(t, row.EffectiveSessionTools())

	schema, strict := row.EffectiveOutputSchema()
	assert.Equal(t, `{"type":"object"}`, schema)
	assert.True(t, strict)

	memory := row.EffectiveMemory()
	assert.True(t, memory.Enabled)
	assert.Equal(t, "embed", memory.EmbeddingModel)
	assert.Equal(t, int32(7), memory.TTLDays)
}
