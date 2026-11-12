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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// The declared spec fields stay the spec view (the admin API writes them
// back), while the Resolved* fields carry what the controller made of the spec:
// selector matches and everything inherited from aggregated agents.
func TestAgentFromCRSeparatesDeclaredFromResolved(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "aggregate", Namespace: "default"},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec:      agentv1alpha1.CommonSpec{Enabled: true},
			Skills:          []agentv1alpha1.Reference{{Name: "own-skill"}},
			Agents:          []agentv1alpha1.Reference{{Name: "base"}},
			RequireApproval: []string{"own_*"},
		},
		Status: agentv1alpha1.AgentStatus{
			ObservedGeneration: 1,
			Resolved: agentv1alpha1.AgentResolved{
				Skills:          []string{"own-skill", "inherited-skill"},
				MCPs:            []string{"inherited-mcp"},
				KnowledgeBases:  []string{"inherited-kb"},
				Webhooks:        []string{"inherited-hook"},
				RequireApproval: []string{"own_*", "inherited_*"},
				CustomCommands:  []string{"inherited-cmd"},
			},
		},
	}

	row := agentFromCR(cr)

	assert.Equal(t, model.StringList{"default/own-skill"}, row.Skills, "the declared set is the spec view, qualified")
	assert.Equal(t, model.StringList{"default/own-skill", "default/inherited-skill"}, row.ResolvedSkills)
	assert.ElementsMatch(t, []string{"default/own-skill", "default/inherited-skill"}, row.EffectiveSkills())
	assert.ElementsMatch(t, []string{"default/inherited-mcp"}, row.EffectiveMCPs())
	assert.ElementsMatch(t, []string{"default/inherited-kb"}, row.EffectiveKnowledgeBases())
	assert.ElementsMatch(t, []string{"inherited-hook"}, row.EffectiveWebhooks())
	assert.ElementsMatch(t, []string{"own_*", "inherited_*"}, row.EffectiveRequireApproval())
	assert.ElementsMatch(t, []string{"inherited-cmd"}, row.EffectiveCustomCommands())
}

// An admin update must not silently drop a field the admin API cannot express.
func TestAgentToCRPreservesAggregationAndWebhooks(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "aggregate", Namespace: "default"},
		Spec: agentv1alpha1.AgentSpec{
			Agents:        []agentv1alpha1.Reference{{Name: "base"}},
			AgentSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}},
			Webhooks:      []string{"audit-hook"},
		},
	}
	row := &model.AgentM{Name: "aggregate"}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "base"}}, cr.Spec.Agents)
	assert.NotNil(t, cr.Spec.AgentSelector, "the selector must survive a write the admin API cannot express")
	assert.Equal(t, []string{"audit-hook"}, cr.Spec.Webhooks)
}

// The console can now express webhooks: an explicit empty list is the clear
// after the last hook was removed, and must reach the spec.
func TestAgentToCRClearsWebhooksOnExplicitEmpty(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       agentv1alpha1.AgentSpec{Webhooks: []string{"audit-hook"}},
	}
	row := &model.AgentM{Name: "worker", Webhooks: model.StringList{}}

	AgentToCR(row, cr)

	assert.Empty(t, cr.Spec.Webhooks)
	assert.NotNil(t, cr.Spec.Webhooks, "an explicit clear must not fall back to nil")
}

// A command that changes its type must not keep the previous type's body:
// validation rejects two bodies, and the rejection names a field the caller
// never wrote.
func TestCustomCommandToCRClearsThePreviousBody(t *testing.T) {
	cr := &agentv1alpha1.CustomCommand{
		ObjectMeta: metav1.ObjectMeta{Name: "triage", Namespace: "default"},
		Spec: agentv1alpha1.CustomCommandSpec{
			Type:   "tool",
			Tool:   &agentv1alpha1.ToolCommandSpec{MCPServer: agentv1alpha1.Reference{Name: "k8s"}, Tool: "get_pod"},
			Output: &agentv1alpha1.CommandOutputSpec{Format: "json"},
		},
	}
	row := &model.CustomCommandM{
		Name:   "triage",
		Type:   "prompt",
		Prompt: `{"template":"请处理 {{ .Params.service }}","description":"d"}`,
	}

	assert.NoError(t, CustomCommandToCR(row, cr))
	assert.Nil(t, cr.Spec.Tool, "the previous tool body must be cleared")
	assert.Nil(t, cr.Spec.Output, "the previous output block must be cleared")
	assert.NotNil(t, cr.Spec.Prompt)
	assert.Equal(t, "请处理 {{ .Params.service }}", cr.Spec.Prompt.Template)
}

// An empty reference list has to stay absent, not become an empty one: every
// spec list carries omitempty, so the API server writes nothing and reads the
// field back as nil. Producing a non-nil empty slice here would make the
// write-if-changed comparison in UpdateFunc see a difference that does not exist
// and rewrite the object on every request.
func TestNameRefsKeepsAbsenceAbsent(t *testing.T) {
	assert.Nil(t, nameRefsFrom("", nil))
	assert.Nil(t, nameRefsFrom("", []string{}))
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "onex-console"}}, nameRefsFrom("", []string{"onex-console"}))
}

// The fallback order is promptRef first, then prompts as listed; entries are
// qualified with their namespace, and duplicates collapse to their first
// position.
func TestPromptRefsFromCROrdersAndFilters(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			PromptRef: &agentv1alpha1.Reference{Name: "first"},
			Prompts: []agentv1alpha1.Reference{
				{Name: "second"},
				{Name: "first"},
				{Name: "elsewhere", Namespace: "other"},
				{Name: ""},
				{Name: agentv1alpha1.BuiltinPrompt},
				{Name: "third"},
			},
			// Selector matches are only visible to the controller; the aggregate
			// is what carries them to the runtime.
			PromptSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}},
		},
	}

	assert.Equal(t, model.StringList{"team/first", "team/second", "other/elsewhere", "team/third"}, promptRefsFromCR(cr))
}

func TestPromptRefsFromCREmptyMeansBuiltin(t *testing.T) {
	cr := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"}}
	assert.Nil(t, promptRefsFromCR(cr))
}

func TestAgentToCRWritesPromptList(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			PromptRef:      &agentv1alpha1.Reference{Name: "old"},
			Prompts:        []agentv1alpha1.Reference{{Name: "legacy"}},
			PromptSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}},
		},
	}
	row := &model.AgentM{Name: "agent", PromptRefs: model.StringList{"one", "two"}}

	AgentToCR(row, cr)

	assert.Nil(t, cr.Spec.PromptRef, "the list replaces the deprecated single reference")
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "one"}, {Name: "two"}}, cr.Spec.Prompts)
	assert.NotNil(t, cr.Spec.PromptSelector, "the management API cannot express a selector")
}

func TestAgentToCRPromptListNilIsNoOpinion(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			Prompts: []agentv1alpha1.Reference{{Name: "keep"}},
		},
	}
	row := &model.AgentM{Name: "agent"}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "keep"}}, cr.Spec.Prompts,
		"a nil list means no opinion, not clear the bindings")
}

func TestAgentFromCRCarriesTemplateRefs(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			TemplateRefs: []agentv1alpha1.Reference{{Name: "base"}, {Name: "ops"}},
		},
	}

	row := agentFromCR(cr)

	assert.Equal(t, model.StringList{"team/base", "team/ops"}, row.TemplateRefs)
}

func TestAgentToCRWritesTemplateRefsInOrder(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
	}
	row := &model.AgentM{Name: "agent", TemplateRefs: model.StringList{"base", "ops"}}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "base"}, {Name: "ops"}}, cr.Spec.TemplateRefs)
}

func TestAgentToCRTemplateRefsNilIsNoOpinion(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			TemplateRefs: []agentv1alpha1.Reference{{Name: "keep"}},
		},
	}
	row := &model.AgentM{Name: "agent"}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "keep"}}, cr.Spec.TemplateRefs,
		"a nil list means no opinion, not clear the bindings")
}

func TestAgentToCRTemplateRefsEmptyClears(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			TemplateRefs: []agentv1alpha1.Reference{{Name: "old"}},
		},
	}
	row := &model.AgentM{Name: "agent", TemplateRefs: model.StringList{}}

	AgentToCR(row, cr)

	assert.Empty(t, cr.Spec.TemplateRefs, "an explicit empty list clears the bindings")
}

func TestAgentFromCRCarriesDelegates(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			Delegates: []agentv1alpha1.Reference{{Name: "researcher"}, {Name: "reviewer"}},
		},
	}

	row := agentFromCR(cr)

	assert.Equal(t, model.StringList{"team/researcher", "team/reviewer"}, row.Delegates)
}

func TestAgentToCRWritesDelegatesInOrder(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
	}
	row := &model.AgentM{Name: "agent", Delegates: model.StringList{"researcher", "reviewer"}}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "researcher"}, {Name: "reviewer"}}, cr.Spec.Delegates)
}

func TestAgentToCRDelegatesNilIsNoOpinion(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			Delegates: []agentv1alpha1.Reference{{Name: "keep"}},
		},
	}
	row := &model.AgentM{Name: "agent"}

	AgentToCR(row, cr)

	assert.Equal(t, []agentv1alpha1.Reference{{Name: "keep"}}, cr.Spec.Delegates,
		"a nil list means no opinion, not clear the delegation")
}

func TestAgentToCRDelegatesEmptyClears(t *testing.T) {
	cr := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "team"},
		Spec: agentv1alpha1.AgentSpec{
			Delegates: []agentv1alpha1.Reference{{Name: "old"}},
		},
	}
	row := &model.AgentM{Name: "agent", Delegates: model.StringList{}}

	AgentToCR(row, cr)

	assert.Empty(t, cr.Spec.Delegates, "an explicit empty list clears the delegation")
}

// A prompt's namespace lives in the object's metadata, so it must survive the
// trip from the control plane back to the row the console reads.
func TestPromptFromCRCarriesNamespace(t *testing.T) {
	cr := &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "helper-public"},
		Spec:       agentv1alpha1.PromptSpec{Template: "hello"},
	}

	row := promptFromCR(cr)

	assert.Equal(t, "helper-public", row.Namespace)
}

// The wire Prompt cannot express dataSources, so a console round-trip sends
// none. That must not erase what the object already carries.
func TestPromptToCRPreservesDataSources(t *testing.T) {
	cr := &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec: agentv1alpha1.PromptSpec{
			Template:    "hi {{include \"facts\"}}",
			DataSources: map[string]string{"facts": "the facts"},
		},
	}

	PromptToCR(&model.PromptM{Name: "p", Content: "hi"}, cr)

	assert.Equal(t, "hi", cr.Spec.Template)
	assert.Equal(t, map[string]string{"facts": "the facts"}, cr.Spec.DataSources,
		"an empty row must not clear the object's dataSources")
}

func TestPromptToCRWritesDataSourcesWhenPresent(t *testing.T) {
	cr := &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       agentv1alpha1.PromptSpec{Template: "hi", DataSources: map[string]string{"old": "x"}},
	}

	PromptToCR(&model.PromptM{
		Name:        "p",
		Content:     "hi",
		DataSources: model.StringMap{"new": "y"},
	}, cr)

	assert.Equal(t, map[string]string{"new": "y"}, cr.Spec.DataSources)
}
