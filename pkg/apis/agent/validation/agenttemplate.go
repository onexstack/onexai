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
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// ValidateAgentTemplate checks a template before it is persisted.
//
// A template declares the same behavior fields as an agent, with the same
// meaning, so the rules are the agent rules rather than a second copy of them.
// The fields an agent-only concept would need (identity, runtime, authz) are
// absent from the template type, and the zero values ValidateAgent sees for
// them produce no errors.
func ValidateAgentTemplate(spec *agent.AgentTemplateSpec, fldPath *field.Path) field.ErrorList {
	as := agent.AgentSpec{
		CommonSpec:            spec.CommonSpec,
		PromptRef:             spec.PromptRef,
		Prompts:               spec.Prompts,
		PromptSelector:        spec.PromptSelector,
		ModelRef:              spec.ModelRef,
		Skills:                spec.Skills,
		SkillSelector:         spec.SkillSelector,
		MCPs:                  spec.MCPs,
		MCPServerSelector:     spec.MCPServerSelector,
		KnowledgeBases:        spec.KnowledgeBases,
		KnowledgeBaseSelector: spec.KnowledgeBaseSelector,
		CustomCommands:        spec.CustomCommands,
		CustomCommandSelector: spec.CustomCommandSelector,
		Webhooks:              spec.Webhooks,
		RequireApproval:       spec.RequireApproval,
		Delegates:             spec.Delegates,
		OutputSchema:          spec.OutputSchema,
		OutputSchemaStrict:    spec.OutputSchemaStrict,
		SessionTools:          spec.SessionTools,
		MemoryRef:             spec.MemoryRef,
	}
	allErrs := ValidateAgent(&as, fldPath)

	// A template is expanded into an agent in the same namespace, and the
	// runtime row carries only the name, so a cross-namespace model would be
	// admitted and then looked up in the wrong place.
	if spec.ModelRef != nil && spec.ModelRef.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("modelRef", "namespace"),
			spec.ModelRef.Namespace, "a template model must be in the template's namespace"))
	}

	return allErrs
}
