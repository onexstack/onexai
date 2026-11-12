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
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// templateSet is the behavior contributed by an agent's spec.templateRefs.
//
// Resolved sets are already "references ∪ selector matches", so the caller can
// union them with the agent's own resolved sets without knowing whether a name
// came from a ref or a selector.
type templateSet struct {
	names   []string
	missing []string

	prompts         []agentv1alpha1.Reference
	promptSelectors []*metav1.LabelSelector

	model          string
	skills         []string
	customCommands []string
	mcps           []string
	knowledgeBases []string
	webhooks       []string
	approval       []string
	delegates      []string

	sessionTools       bool
	outputSchema       *apiextensionsv1.JSON
	outputSchemaStrict bool
	memoryRef          *agentv1alpha1.Reference
}

// resolveTemplates expands spec.templateRefs in declaration order.
//
// Precedence is the contract: sets are unions, while a scalar field the agent
// does not set falls back to the first template that declares it. A missing or
// disabled template contributes nothing and is reported, so RefsResolved turns
// false rather than the agent silently losing half its behavior.
func (r *Reconciler) resolveTemplates(ctx context.Context, agent *agentv1alpha1.Agent) (templateSet, error) {
	var set templateSet
	seen := make(map[string]struct{}, len(agent.Spec.TemplateRefs))

	for _, ref := range agent.Spec.TemplateRefs {
		if _, ok := seen[ref.Name]; ok {
			continue
		}
		seen[ref.Name] = struct{}{}

		// Validation forbids a cross-namespace template; treating one as
		// missing keeps a hand-written object from resolving somewhere the
		// runtime cannot read.
		if ref.Namespace != "" && ref.Namespace != agent.Namespace {
			set.missing = append(set.missing, "AgentTemplate/"+ref.Namespace+"/"+ref.Name)
			continue
		}

		tmpl := &agentv1alpha1.AgentTemplate{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: ref.Name}, tmpl); err != nil {
			if client.IgnoreNotFound(err) == nil {
				set.missing = append(set.missing, "AgentTemplate/"+ref.Name)
				continue
			}
			return set, err
		}
		if !tmpl.Spec.Enabled {
			set.missing = append(set.missing, "AgentTemplate/"+ref.Name+" (disabled)")
			continue
		}
		set.names = append(set.names, ref.Name)

		if tmpl.Spec.PromptRef != nil {
			set.prompts = append(set.prompts, *tmpl.Spec.PromptRef)
		}
		set.prompts = append(set.prompts, tmpl.Spec.Prompts...)
		if tmpl.Spec.PromptSelector != nil {
			set.promptSelectors = append(set.promptSelectors, tmpl.Spec.PromptSelector)
		}

		skills, skillMissing, err := r.resolveSkillsIn(ctx, agent.Namespace, tmpl.Spec.Skills, tmpl.Spec.SkillSelector)
		if err != nil {
			return set, err
		}
		set.skills = unionNames(set.skills, skills)
		set.missing = append(set.missing, skillMissing...)

		commands, commandMissing, err := r.resolveCustomCommandsIn(
			ctx, agent.Namespace, tmpl.Spec.CustomCommands, tmpl.Spec.CustomCommandSelector)
		if err != nil {
			return set, err
		}
		set.customCommands = unionNames(set.customCommands, commands)
		set.missing = append(set.missing, commandMissing...)

		mcps, mcpMissing, err := r.resolveMCPsIn(ctx, agent.Namespace, tmpl.Spec.MCPs, tmpl.Spec.MCPServerSelector)
		if err != nil {
			return set, err
		}
		set.mcps = unionNames(set.mcps, mcps)
		set.missing = append(set.missing, mcpMissing...)

		kbs, kbMissing, err := r.resolveKnowledgeBasesIn(
			ctx, agent.Namespace, tmpl.Spec.KnowledgeBases, tmpl.Spec.KnowledgeBaseSelector)
		if err != nil {
			return set, err
		}
		set.knowledgeBases = unionNames(set.knowledgeBases, kbs)
		set.missing = append(set.missing, kbMissing...)

		for _, name := range tmpl.Spec.Webhooks {
			_, ok, err := r.exists(ctx, agent.Namespace, name, &agentv1alpha1.Webhook{})
			if err != nil {
				return set, err
			}
			if !ok {
				set.missing = append(set.missing, "Webhook/"+name)
				continue
			}
			set.webhooks = unionNames(set.webhooks, []string{name})
		}

		set.approval = unionNames(set.approval, tmpl.Spec.RequireApproval)
		set.delegates = unionNames(set.delegates, referenceNames(tmpl.Spec.Delegates))
		set.sessionTools = set.sessionTools || tmpl.Spec.SessionTools

		// Scalars: first declaring template wins. The agent itself is merged
		// ahead of this set by the caller, so an explicit agent field always
		// beats every template.
		if set.model == "" && tmpl.Spec.ModelRef != nil {
			_, ok, err := r.exists(ctx, agent.Namespace, tmpl.Spec.ModelRef.Name, &agentv1alpha1.Model{})
			if err != nil {
				return set, err
			}
			if ok {
				set.model = tmpl.Spec.ModelRef.Name
			} else {
				set.missing = append(set.missing, "Model/"+tmpl.Spec.ModelRef.Name)
			}
		}
		if set.outputSchema == nil && tmpl.Spec.OutputSchema != nil {
			set.outputSchema = tmpl.Spec.OutputSchema.DeepCopy()
			set.outputSchemaStrict = tmpl.Spec.OutputSchemaStrict
		}
		if set.memoryRef == nil && tmpl.Spec.MemoryRef != nil {
			set.memoryRef = tmpl.Spec.MemoryRef.DeepCopy()
		}
	}

	return set, nil
}

// templatesCondition reports whether every named template was applied.
func templatesCondition(agent *agentv1alpha1.Agent, set templateSet) metav1.Condition {
	ok := len(set.missing) == 0
	message := fmt.Sprintf("applied %d template(s)", len(set.names))
	if !ok {
		message = missingMessage(set.missing)
	}
	return metav1.Condition{
		Type:               agentv1alpha1.ConditionTemplatesApplied,
		Status:             boolCondition(ok),
		Reason:             reasonFor(ok, "TemplatesApplied", "TemplateUnavailable"),
		Message:            message,
		ObservedGeneration: agent.Generation,
	}
}

// referenceNames returns the names of a reference list in declaration order.
func referenceNames(refs []agentv1alpha1.Reference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}
