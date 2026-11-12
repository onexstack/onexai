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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ClusterAgentBindingV1ToCR converts the wire shape to the control-plane
// object. Status is never written: the controller owns it.
func ClusterAgentBindingV1ToCR(p *v1.ClusterAgentBinding) *agentv1alpha1.ClusterAgentBinding {
	if p == nil {
		return &agentv1alpha1.ClusterAgentBinding{}
	}
	cr := &agentv1alpha1.ClusterAgentBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   p.GetName(),
			Labels: copyLabels(p.GetLabels()),
		},
	}
	spec := p.GetSpec()
	if spec == nil {
		return cr
	}
	cr.Spec.Enabled = spec.Enabled
	cr.Spec.DisplayName = spec.GetDisplayName()
	cr.Spec.Description = spec.GetDescription()
	if owner := spec.GetOwner(); owner != nil && owner.GetName() != "" {
		cr.Spec.Owner = &rbacv1.Subject{
			Kind:     owner.GetKind(),
			Name:     owner.GetName(),
			APIGroup: owner.GetApiGroup(),
		}
	}
	cr.Spec.Namespaces = append([]string(nil), spec.GetNamespaces()...)
	cr.Spec.AgentSelector = labelSelectorToCR(spec.GetAgentSelector())
	cr.Spec.Agents = clusterReferencesToCR(spec.GetAgents())
	cr.Spec.Prompts = clusterReferencesToCR(spec.GetPrompts())
	cr.Spec.Skills = clusterReferencesToCR(spec.GetSkills())
	cr.Spec.KnowledgeBases = clusterReferencesToCR(spec.GetKnowledgeBases())
	cr.Spec.MCPServers = clusterReferencesToCR(spec.GetMcpservers())
	cr.Spec.CustomCommands = clusterReferencesToCR(spec.GetCustomCommands())
	cr.Spec.CommandSelector = labelSelectorToCR(spec.GetCommandSelector())
	cr.Spec.Commands = append([]string(nil), spec.GetCommands()...)
	cr.Spec.ModelRef = referenceToCR(spec.GetModelRef())
	if memory := spec.GetMemoryRef(); memory != nil && memory.GetName() != "" {
		cr.Spec.MemoryRef = &agentv1alpha1.Reference{
			Name:      memory.GetName(),
			Namespace: memory.GetNamespace(),
		}
	}
	return cr
}

// ClusterAgentBindingCRToV1 converts the control-plane object to the wire
// shape, including the status the controller recorded.
func ClusterAgentBindingCRToV1(cr *agentv1alpha1.ClusterAgentBinding) *v1.ClusterAgentBinding {
	if cr == nil {
		return nil
	}
	createdAt := unixMilli(cr.CreationTimestamp)
	out := &v1.ClusterAgentBinding{
		Name:   cr.Name,
		Labels: copyLabels(cr.Labels),
		Spec: &v1.ClusterAgentBindingSpec{
			Enabled:         cr.Spec.Enabled,
			DisplayName:     cr.Spec.DisplayName,
			Description:     cr.Spec.Description,
			Namespaces:      append([]string(nil), cr.Spec.Namespaces...),
			AgentSelector:   labelSelectorFromCR(cr.Spec.AgentSelector),
			Agents:          clusterReferencesFromCR(cr.Spec.Agents),
			Prompts:         clusterReferencesFromCR(cr.Spec.Prompts),
			Skills:          clusterReferencesFromCR(cr.Spec.Skills),
			KnowledgeBases:  clusterReferencesFromCR(cr.Spec.KnowledgeBases),
			Mcpservers:      clusterReferencesFromCR(cr.Spec.MCPServers),
			CustomCommands:  clusterReferencesFromCR(cr.Spec.CustomCommands),
			CommandSelector: labelSelectorFromCR(cr.Spec.CommandSelector),
			Commands:        append([]string(nil), cr.Spec.Commands...),
		},
		Status: &v1.ClusterAgentBindingStatus{
			ObservedGeneration: cr.Status.ObservedGeneration,
			MatchedNamespaces:  cr.Status.MatchedNamespaces,
			MatchedAgents:      cr.Status.MatchedAgents,
			Missing:            append([]string(nil), cr.Status.Missing...),
			Resolved: &v1.AgentBindingResolved{
				Prompts:        append([]string(nil), cr.Status.Resolved.Prompts...),
				Skills:         append([]string(nil), cr.Status.Resolved.Skills...),
				Mcps:           append([]string(nil), cr.Status.Resolved.MCPs...),
				KnowledgeBases: append([]string(nil), cr.Status.Resolved.KnowledgeBases...),
				CustomCommands: append([]string(nil), cr.Status.Resolved.CustomCommands...),
				Commands:       append([]string(nil), cr.Status.Resolved.Commands...),
			},
		},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	if owner := cr.Spec.Owner; owner != nil && owner.Name != "" {
		out.Spec.Owner = &v1.AgentBindingSubject{
			Kind:     owner.Kind,
			Name:     owner.Name,
			ApiGroup: owner.APIGroup,
		}
	}
	if memory := cr.Spec.MemoryRef; memory != nil && memory.Name != "" {
		item := &v1.ObjectReference{Name: memory.Name}
		if memory.Namespace != "" {
			namespace := memory.Namespace
			item.Namespace = &namespace
		}
		out.Spec.MemoryRef = item
	}
	out.Spec.ModelRef = referenceFromCR(cr.Spec.ModelRef)
	return out
}

func clusterReferencesToCR(refs []*v1.ObjectReference) []agentv1alpha1.Reference {
	if len(refs) == 0 {
		return nil
	}
	out := make([]agentv1alpha1.Reference, 0, len(refs))
	for _, ref := range refs {
		if ref.GetName() == "" {
			continue
		}
		out = append(out, agentv1alpha1.Reference{Name: ref.GetName(), Namespace: ref.GetNamespace()})
	}
	return out
}

func clusterReferencesFromCR(refs []agentv1alpha1.Reference) []*v1.ObjectReference {
	if len(refs) == 0 {
		return nil
	}
	out := make([]*v1.ObjectReference, 0, len(refs))
	for _, ref := range refs {
		if ref.Name == "" {
			continue
		}
		item := &v1.ObjectReference{Name: ref.Name}
		if ref.Namespace != "" {
			namespace := ref.Namespace
			item.Namespace = &namespace
		}
		out = append(out, item)
	}
	return out
}
