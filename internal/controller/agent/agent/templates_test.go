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

package agent

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func fakeTemplate(name string, enabled bool) *agentv1alpha1.AgentTemplate {
	return &agentv1alpha1.AgentTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: agentv1alpha1.AgentTemplateSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled},
		},
	}
}

// A template contributes its behavior to the referencing agent: sets are
// unioned and the scalar fields fall back to it.
func TestResolveTemplatesContributesBehavior(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.TemplateRefs = []agentv1alpha1.Reference{{Name: "base"}}
	root.Spec.Skills = []agentv1alpha1.Reference{{Name: "root-skill"}}

	tmpl := fakeTemplate("base", true)
	tmpl.Spec.Prompts = []agentv1alpha1.Reference{{Name: "base-prompt"}}
	tmpl.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
	tmpl.Spec.MCPs = []agentv1alpha1.Reference{{Name: "base-mcp"}}
	tmpl.Spec.KnowledgeBases = []agentv1alpha1.Reference{{Name: "base-kb"}}
	tmpl.Spec.CustomCommands = []agentv1alpha1.Reference{{Name: "base-command"}}
	tmpl.Spec.Webhooks = []string{"base-hook"}
	tmpl.Spec.RequireApproval = []string{"base_*"}
	tmpl.Spec.ModelRef = &agentv1alpha1.Reference{Name: "base-model"}
	tmpl.Spec.Delegates = []agentv1alpha1.Reference{{Name: "worker"}}
	tmpl.Spec.SessionTools = true
	tmpl.Spec.OutputSchema = &apiextensionsv1.JSON{Raw: []byte(`{"type":"object"}`)}
	tmpl.Spec.MemoryRef = &agentv1alpha1.Reference{Name: "base-memory"}

	objs := []client.Object{
		root, tmpl,
		&agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{Name: "base-prompt", Namespace: "default"}, Spec: agentv1alpha1.PromptSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}, Template: "base persona"}},
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "root-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "base-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "base-mcp", Namespace: "default"}, Spec: agentv1alpha1.MCPServerSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.KnowledgeBase{ObjectMeta: metav1.ObjectMeta{Name: "base-kb", Namespace: "default"}, Spec: agentv1alpha1.KnowledgeBaseSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.CustomCommand{ObjectMeta: metav1.ObjectMeta{Name: "base-command", Namespace: "default"}, Spec: agentv1alpha1.CustomCommandSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: "base-hook", Namespace: "default"}, Spec: agentv1alpha1.WebhookSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "base-model", Namespace: "default"}, Spec: agentv1alpha1.ModelSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Memory{
			ObjectMeta: metav1.ObjectMeta{Name: "base-memory", Namespace: "default"},
			Spec: agentv1alpha1.MemorySpec{
				CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
				Embedding:  agentv1alpha1.MemoryEmbeddingSpec{ModelRef: &agentv1alpha1.Reference{Name: "base-model"}, Dimensions: 3, Version: "v1"},
			},
		},
	}

	result, err := newFakeReconciler(t, objs...).resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	assertStrings(t, "templates", result.Resolved.Templates, "base")
	assertStrings(t, "skills", result.Resolved.Skills, "base-skill", "root-skill")
	assertStrings(t, "mcps", result.Resolved.MCPs, "base-mcp")
	assertStrings(t, "knowledgeBases", result.Resolved.KnowledgeBases, "base-kb")
	assertStrings(t, "customCommands", result.Resolved.CustomCommands, "base-command")
	assertStrings(t, "webhooks", result.Resolved.Webhooks, "base-hook")
	assertStrings(t, "requireApproval", result.Resolved.RequireApproval, "base_*")
	assertStrings(t, "delegates", result.Resolved.Delegates, "worker")

	if result.Resolved.Model != "base-model" {
		t.Errorf("model = %q, want the template's model", result.Resolved.Model)
	}
	if !result.Resolved.SessionTools {
		t.Error("sessionTools = false, want the template's opt-in")
	}
	if result.Resolved.OutputSchema == nil {
		t.Fatal("outputSchema = nil, want the template's schema")
	}
	if result.Resolved.Memory == nil || result.Resolved.Memory.Name != "base-memory" ||
		result.Resolved.Memory.EmbeddingModel != "base-model" {
		t.Errorf("memory = %+v, want the template's setting", result.Resolved.Memory)
	}
	if got := result.Prompts.availableNames(); len(got) != 1 || got[0] != "base-prompt" {
		t.Errorf("prompt sources = %v, want the template's prompt", got)
	}
}

// An explicit agent field beats every template, whatever the template order.
func TestResolveTemplatesAgentScalarsWin(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.TemplateRefs = []agentv1alpha1.Reference{{Name: "base"}, {Name: "overlay"}}
	root.Spec.ModelRef = &agentv1alpha1.Reference{Name: "agent-model"}
	root.Spec.OutputSchema = &apiextensionsv1.JSON{Raw: []byte(`{"type":"string"}`)}
	root.Spec.MemoryRef = &agentv1alpha1.Reference{Name: "agent-memory"}

	base := fakeTemplate("base", true)
	base.Spec.ModelRef = &agentv1alpha1.Reference{Name: "base-model"}
	base.Spec.OutputSchema = &apiextensionsv1.JSON{Raw: []byte(`{"type":"object"}`)}
	base.Spec.MemoryRef = &agentv1alpha1.Reference{Name: "base-memory"}

	overlay := fakeTemplate("overlay", true)
	overlay.Spec.ModelRef = &agentv1alpha1.Reference{Name: "overlay-model"}

	objs := []client.Object{
		root, base, overlay,
		&agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: "default"}, Spec: agentv1alpha1.ModelSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "base-model", Namespace: "default"}, Spec: agentv1alpha1.ModelSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "overlay-model", Namespace: "default"}, Spec: agentv1alpha1.ModelSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Memory{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-memory", Namespace: "default"},
			Spec: agentv1alpha1.MemorySpec{
				CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
				Embedding:  agentv1alpha1.MemoryEmbeddingSpec{ModelRef: &agentv1alpha1.Reference{Name: "agent-model"}, Dimensions: 3, Version: "v1"},
			},
		},
		&agentv1alpha1.Memory{
			ObjectMeta: metav1.ObjectMeta{Name: "base-memory", Namespace: "default"},
			Spec: agentv1alpha1.MemorySpec{
				CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
				Embedding:  agentv1alpha1.MemoryEmbeddingSpec{ModelRef: &agentv1alpha1.Reference{Name: "base-model"}, Dimensions: 3, Version: "v1"},
			},
		},
	}

	result, err := newFakeReconciler(t, objs...).resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if result.Resolved.Model != "agent-model" {
		t.Errorf("model = %q, want the agent's own", result.Resolved.Model)
	}
	if string(result.Resolved.OutputSchema.Raw) != `{"type":"string"}` {
		t.Errorf("outputSchema = %s, want the agent's own", result.Resolved.OutputSchema.Raw)
	}
	if result.Resolved.Memory == nil || result.Resolved.Memory.Name != "agent-memory" ||
		result.Resolved.Memory.EmbeddingModel != "agent-model" {
		t.Errorf("memory = %+v, want the agent's own", result.Resolved.Memory)
	}
}

// A missing or disabled template contributes nothing and fails RefsResolved,
// the same way a missing skill does.
func TestResolveTemplatesMissingAndDisabled(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.TemplateRefs = []agentv1alpha1.Reference{{Name: "gone"}, {Name: "off"}}
	off := fakeTemplate("off", false)

	result, err := newFakeReconciler(t, root, off).resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(result.Resolved.Templates) != 0 {
		t.Errorf("templates = %v, want none applied", result.Resolved.Templates)
	}
	if len(result.Prompts.available) != 0 {
		t.Errorf("prompts = %v, want none", result.Prompts.available)
	}
	if got := len(result.Conditions); got == 0 {
		t.Fatal("no conditions reported")
	}

	var found bool
	for _, condition := range result.Conditions {
		if condition.Type == agentv1alpha1.ConditionTemplatesApplied {
			found = true
			if condition.Status != metav1.ConditionFalse {
				t.Errorf("TemplatesApplied = %s, want False", condition.Status)
			}
		}
		if condition.Type == agentv1alpha1.ConditionRefsResolved && condition.Status != metav1.ConditionFalse {
			t.Error("RefsResolved = True, want False when a template is missing")
		}
	}
	if !found {
		t.Error("no TemplatesApplied condition reported")
	}
}

// Aggregating an agent carries the behavior its templates contributed, the
// same way it carries the capabilities its bindings contributed. Dropping them
// would make aggregation a way to inherit the source's spec without its
// behavior.
func TestResolveAggregatedSourceTemplatesTravel(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.Agents = []agentv1alpha1.Reference{{Name: "base"}}

	base := fakeAgent("base")
	base.Spec.TemplateRefs = []agentv1alpha1.Reference{{Name: "shared"}}

	shared := fakeTemplate("shared", true)
	shared.Spec.Skills = []agentv1alpha1.Reference{{Name: "shared-skill"}}
	shared.Spec.Prompts = []agentv1alpha1.Reference{{Name: "shared-prompt"}}

	objs := []client.Object{
		root, base, shared,
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "shared-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{Name: "shared-prompt", Namespace: "default"}, Spec: agentv1alpha1.PromptSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}, Template: "shared persona"}},
	}

	result, err := newFakeReconciler(t, objs...).resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	assertStrings(t, "skills", result.Resolved.Skills, "shared-skill")
	names := result.Prompts.availableNames()
	if len(names) != 1 || names[0] != "shared-prompt" {
		t.Errorf("prompt sources = %v, want the source agent's template prompt", names)
	}
}

// An explicit agent field wins even when no template declares the scalar: the
// agent's value is simply the resolved value.
func TestResolveTemplatesEmptyContributesNothing(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.TemplateRefs = []agentv1alpha1.Reference{{Name: "empty"}}
	root.Spec.ModelRef = &agentv1alpha1.Reference{Name: "agent-model"}

	result, err := newFakeReconciler(t, root,
		fakeTemplate("empty", true),
		&agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: "default"}, Spec: agentv1alpha1.ModelSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
	).resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if result.Resolved.Model != "agent-model" {
		t.Errorf("model = %q, want the agent's own", result.Resolved.Model)
	}
}
