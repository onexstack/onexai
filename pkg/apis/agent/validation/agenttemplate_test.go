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

package validation

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateAgentTemplateAcceptsACompleteSpec(t *testing.T) {
	spec := &agent.AgentTemplateSpec{
		CommonSpec:      agent.CommonSpec{Enabled: true, DisplayName: "base"},
		Prompts:         []agent.Reference{{Name: "persona"}},
		Skills:          []agent.Reference{{Name: "triage"}},
		MCPs:            []agent.Reference{{Name: "lark"}},
		KnowledgeBases:  []agent.Reference{{Name: "runbooks"}},
		CustomCommands:  []agent.Reference{{Name: "deploy"}},
		Webhooks:        []string{"audit"},
		RequireApproval: []string{"delete_*"},
		Delegates:       []agent.Reference{{Name: "worker"}},
		ModelRef:        &agent.Reference{Name: "gpt"},
		OutputSchema:    &apiextensionsv1.JSON{Raw: []byte(`{"type":"object"}`)},
		SessionTools:    true,
		MemoryRef:       &agent.Reference{Name: "embed"},
	}

	if errs := ValidateAgentTemplate(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

// A template is expanded in its own namespace, so a cross-namespace model ref
// would pass admission and then be resolved in the wrong namespace.
func TestValidateAgentTemplateRejectsACrossNamespaceModel(t *testing.T) {
	spec := &agent.AgentTemplateSpec{
		ModelRef: &agent.Reference{Name: "gpt", Namespace: "other"},
	}

	errs := ValidateAgentTemplate(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("expected an error for a cross-namespace model")
	}
}

// The template rules are the agent rules, so an empty memoryRef is rejected
// here too.
func TestValidateAgentTemplateRejectsAnEmptyMemoryRef(t *testing.T) {
	spec := &agent.AgentTemplateSpec{
		MemoryRef: &agent.Reference{},
	}

	errs := ValidateAgentTemplate(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("expected an error for an empty memoryRef")
	}
}

// A template is a flat behavior unit: it cannot reference another template,
// and an agent's templateRefs are same-namespace by construction.
func TestValidateAgentRejectsACrossNamespaceTemplate(t *testing.T) {
	spec := &agent.AgentSpec{
		TemplateRefs: []agent.Reference{{Name: "base", Namespace: "other"}},
	}

	errs := ValidateAgent(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("expected an error for a cross-namespace template")
	}
}
