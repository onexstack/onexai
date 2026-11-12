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

// AgentBindingV1ToCR converts the wire shape to the control-plane object.
//
// Status is never written: the controller owns it, and an admin request that
// echoed it back would overwrite the observed state with whatever the client
// last saw.
func AgentBindingV1ToCR(p *v1.AgentBinding) *agentv1alpha1.AgentBinding {
	if p == nil {
		return &agentv1alpha1.AgentBinding{}
	}
	cr := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.GetName(),
			Namespace: p.GetNamespace(),
			Labels:    copyLabels(p.GetLabels()),
		},
	}
	spec := p.GetSpec()
	if spec == nil {
		return cr
	}
	// Enabled is tri-state: the pointer survives a round trip so an absent
	// value keeps meaning "enabled" rather than becoming an explicit false.
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
	cr.Spec.Selector = labelSelectorToCR(spec.GetSelector())
	cr.Spec.Agents = append([]string(nil), spec.GetAgents()...)
	cr.Spec.Prompts = referencesToCR(spec.GetPrompts())
	cr.Spec.Skills = referencesToCR(spec.GetSkills())
	cr.Spec.KnowledgeBases = referencesToCR(spec.GetKnowledgeBases())
	cr.Spec.MCPServers = referencesToCR(spec.GetMcpservers())
	cr.Spec.CustomCommands = referencesToCR(spec.GetCustomCommands())
	cr.Spec.CommandSelector = labelSelectorToCR(spec.GetCommandSelector())
	cr.Spec.Commands = append([]string(nil), spec.GetCommands()...)
	cr.Spec.ModelRef = referenceToCR(spec.GetModelRef())
	return cr
}

// AgentBindingCRToV1 converts the control-plane object to the wire shape,
// including the status the controller recorded.
func AgentBindingCRToV1(cr *agentv1alpha1.AgentBinding) *v1.AgentBinding {
	if cr == nil {
		return nil
	}
	// Both timestamps read the creation time, which is what the store's row
	// conversion always did: the object carries no separate update timestamp.
	createdAt := unixMilli(cr.CreationTimestamp)
	out := &v1.AgentBinding{
		Name:      cr.Name,
		Namespace: cr.Namespace,
		Labels:    copyLabels(cr.Labels),
		Spec: &v1.AgentBindingSpec{
			Enabled:         cr.Spec.Enabled,
			DisplayName:     cr.Spec.DisplayName,
			Description:     cr.Spec.Description,
			Selector:        labelSelectorFromCR(cr.Spec.Selector),
			Agents:          append([]string(nil), cr.Spec.Agents...),
			Prompts:         referencesFromCR(cr.Spec.Prompts),
			Skills:          referencesFromCR(cr.Spec.Skills),
			KnowledgeBases:  referencesFromCR(cr.Spec.KnowledgeBases),
			Mcpservers:      referencesFromCR(cr.Spec.MCPServers),
			CustomCommands:  referencesFromCR(cr.Spec.CustomCommands),
			CommandSelector: labelSelectorFromCR(cr.Spec.CommandSelector),
			Commands:        append([]string(nil), cr.Spec.Commands...),
			ModelRef:        referenceFromCR(cr.Spec.ModelRef),
		},
		Status: &v1.AgentBindingStatus{
			ObservedGeneration: cr.Status.ObservedGeneration,
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
	return out
}

func referenceToCR(ref *v1.ObjectReference) *agentv1alpha1.Reference {
	if ref == nil || ref.GetName() == "" {
		return nil
	}
	out := &agentv1alpha1.Reference{Name: ref.GetName()}
	if ref.GetNamespace() != "" {
		out.Namespace = ref.GetNamespace()
	}
	return out
}

func referenceFromCR(ref *agentv1alpha1.Reference) *v1.ObjectReference {
	if ref == nil || ref.Name == "" {
		return nil
	}
	out := &v1.ObjectReference{Name: ref.Name}
	if ref.Namespace != "" {
		namespace := ref.Namespace
		out.Namespace = &namespace
	}
	return out
}

func referencesToCR(refs []*v1.ObjectReference) []agentv1alpha1.Reference {
	if len(refs) == 0 {
		return nil
	}
	out := make([]agentv1alpha1.Reference, 0, len(refs))
	for _, ref := range refs {
		if ref.GetName() == "" {
			continue
		}
		item := agentv1alpha1.Reference{Name: ref.GetName()}
		if ref.GetNamespace() != "" {
			item.Namespace = ref.GetNamespace()
		}
		out = append(out, item)
	}
	return out
}

func referencesFromCR(refs []agentv1alpha1.Reference) []*v1.ObjectReference {
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

func labelSelectorToCR(selector *v1.AgentBindingLabelSelector) *metav1.LabelSelector {
	if selector == nil {
		return nil
	}
	out := &metav1.LabelSelector{MatchLabels: copyLabels(selector.GetMatchLabels())}
	for _, expr := range selector.GetMatchExpressions() {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      expr.GetKey(),
			Operator: metav1.LabelSelectorOperator(expr.GetOperator()),
			Values:   append([]string(nil), expr.GetValues()...),
		})
	}
	return out
}

func labelSelectorFromCR(selector *metav1.LabelSelector) *v1.AgentBindingLabelSelector {
	if selector == nil {
		return nil
	}
	out := &v1.AgentBindingLabelSelector{MatchLabels: copyLabels(selector.MatchLabels)}
	for _, expr := range selector.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, &v1.AgentBindingLabelSelectorRequirement{
			Key:      expr.Key,
			Operator: string(expr.Operator),
			Values:   append([]string(nil), expr.Values...),
		})
	}
	return out
}

// copyLabels detaches the map from the message or object it was read from.
func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// unixMilli renders a timestamp the wire shape carries as epoch milliseconds.
func unixMilli(t metav1.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
