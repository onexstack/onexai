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
	"context"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// maxClusterBindingNamespaces caps the tenants one cluster binding may address.
const maxClusterBindingNamespaces = 64

// ValidateCreateClusterAgentBindingRequest validates a create request.
func (v *Validator) ValidateCreateClusterAgentBindingRequest(
	_ context.Context, rq *v1.CreateClusterAgentBindingRequest,
) error {
	if err := validatePayload(rq.GetClusterAgentBinding(), "clusterAgentBinding", v.ValidateAgentBindingRules()); err != nil {
		return err
	}
	return validateClusterAgentBinding(rq.GetClusterAgentBinding())
}

// ValidateUpdateClusterAgentBindingRequest validates an update request.
func (v *Validator) ValidateUpdateClusterAgentBindingRequest(
	_ context.Context, rq *v1.UpdateClusterAgentBindingRequest,
) error {
	if err := validatePayload(rq.GetClusterAgentBinding(), "clusterAgentBinding", v.ValidateAgentBindingRules()); err != nil {
		return err
	}
	return validateClusterAgentBinding(rq.GetClusterAgentBinding())
}

// ValidateGetClusterAgentBindingRequest validates a get request.
func (v *Validator) ValidateGetClusterAgentBindingRequest(
	_ context.Context, rq *v1.GetClusterAgentBindingRequest,
) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentBindingRules())
}

// ValidateDeleteClusterAgentBindingRequest validates a delete request.
func (v *Validator) ValidateDeleteClusterAgentBindingRequest(
	_ context.Context, rq *v1.DeleteClusterAgentBindingRequest,
) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentBindingRules())
}

// ValidateListClusterAgentBindingRequest validates a list request.
func (v *Validator) ValidateListClusterAgentBindingRequest(
	_ context.Context, rq *v1.ListClusterAgentBindingRequest,
) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateClusterAgentBinding mirrors the declarative rules in
// pkg/apis/agent/validation/clusteragentbinding.go so a binding is refused at
// the REST edge for the same reasons the control plane would refuse it.
func validateClusterAgentBinding(binding *v1.ClusterAgentBinding) error {
	spec := binding.GetSpec()
	if spec == nil {
		return errno.ErrInvalidArgument.WithMessage("clusterAgentBinding.spec is required")
	}

	if err := validateClusterBindingNamespaces(spec.GetNamespaces()); err != nil {
		return err
	}
	if err := validateClusterBindingSelection(spec); err != nil {
		return err
	}
	if err := validateClusterBindingAgents(spec.GetAgents()); err != nil {
		return err
	}
	if err := validateClusterBindingReferences(spec); err != nil {
		return err
	}
	if memory := spec.GetMemoryRef(); memory != nil {
		if err := validateClusterReference("memoryRef", memory); err != nil {
			return err
		}
	}
	if err := validateBindingCommands(spec.GetCommands()); err != nil {
		return err
	}
	return validateBindingCommandSelector(spec.GetCommandSelector())
}

func validateClusterBindingNamespaces(namespaces []string) error {
	if len(namespaces) == 0 {
		return errno.ErrInvalidArgument.WithMessage("a cluster binding must name at least one namespace")
	}
	if len(namespaces) > maxClusterBindingNamespaces {
		return errno.ErrInvalidArgument.WithMessage("namespaces: must hold at most %d entries", maxClusterBindingNamespaces)
	}
	wildcard := false
	for _, namespace := range namespaces {
		if namespace == agent.AllNamespaces {
			wildcard = true
			continue
		}
		if namespace == "" {
			return errno.ErrInvalidArgument.WithMessage("namespaces: a namespace must not be empty")
		}
		if msgs := k8svalidation.IsDNS1123Label(namespace); len(msgs) > 0 {
			return errno.ErrInvalidArgument.WithMessage("namespaces: %q: %s", namespace, msgs[0])
		}
	}
	if wildcard && len(namespaces) > 1 {
		return errno.ErrInvalidArgument.WithMessage(`namespaces: the wildcard "*" must be the only entry`)
	}
	return nil
}

func validateClusterBindingSelection(spec *v1.ClusterAgentBindingSpec) error {
	if spec.GetAgentSelector() == nil && len(spec.GetAgents()) == 0 {
		return errno.ErrInvalidArgument.WithMessage(
			"a binding must select at least one agent: set agentSelector, agents, or both")
	}
	if selector := spec.GetAgentSelector(); selector != nil {
		if _, err := metav1.LabelSelectorAsSelector(toLabelSelector(selector)); err != nil {
			return errno.ErrInvalidArgument.WithMessage("agentSelector: %s", err.Error())
		}
	}
	return nil
}

func validateClusterBindingAgents(agents []*v1.ObjectReference) error {
	if len(agents) > maxBindingAgents {
		return errno.ErrInvalidArgument.WithMessage("agents: must hold at most %d entries", maxBindingAgents)
	}
	for _, ref := range agents {
		if err := validateClusterReference("agents", ref); err != nil {
			return err
		}
	}
	return nil
}

func validateClusterBindingReferences(spec *v1.ClusterAgentBindingSpec) error {
	refLists := []struct {
		field string
		refs  []*v1.ObjectReference
	}{
		{"prompts", spec.GetPrompts()},
		{"skills", spec.GetSkills()},
		{"knowledgeBases", spec.GetKnowledgeBases()},
		{"mcpservers", spec.GetMcpservers()},
		{"customCommands", spec.GetCustomCommands()},
	}
	for _, list := range refLists {
		if len(list.refs) > maxBindingRefsPerKind {
			return errno.ErrInvalidArgument.WithMessage("%s: must hold at most %d entries", list.field, maxBindingRefsPerKind)
		}
		for _, ref := range list.refs {
			if err := validateClusterReference(list.field, ref); err != nil {
				return err
			}
		}
	}
	if ref := spec.GetModelRef(); ref != nil {
		if err := validateClusterReference("modelRef", ref); err != nil {
			return err
		}
	}
	return nil
}

// validateClusterReference checks one reference. Unlike a namespaced binding,
// the namespace is required: a cluster-scoped object has no namespace to
// default to.
func validateClusterReference(field string, ref *v1.ObjectReference) error {
	if ref.GetName() == "" {
		return errno.ErrInvalidArgument.WithMessage("%s: a reference must name its target", field)
	}
	if msgs := k8svalidation.IsDNS1123Subdomain(ref.GetName()); len(msgs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("%s: %q: %s", field, ref.GetName(), msgs[0])
	}
	if ref.GetNamespace() == "" {
		return errno.ErrInvalidArgument.WithMessage("%s: %q must name its namespace", field, ref.GetName())
	}
	if msgs := k8svalidation.IsDNS1123Label(ref.GetNamespace()); len(msgs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("%s: namespace %q: %s", field, ref.GetNamespace(), msgs[0])
	}
	return nil
}
