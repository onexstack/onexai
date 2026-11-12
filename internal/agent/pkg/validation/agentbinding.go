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
	"slices"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

const (
	// maxBindingAgents caps the explicitly named agents. Selector matches are
	// bounded by the namespace instead, which is why they are not counted here.
	maxBindingAgents = 64
	// maxBindingRefsPerKind caps each reference list. One binding is meant to
	// carry a platform-level preset, not to become a bulk-edit tool.
	maxBindingRefsPerKind = 64
	// maxBindingCommands caps the named commands.
	maxBindingCommands = 128
)

// ValidateAgentBindingRules returns the rules for an agent-binding payload.
func (v *Validator) ValidateAgentBindingRules() genericvalidation.Rules { return nameRules }

// ValidateCreateAgentBindingRequest validates a create request.
func (v *Validator) ValidateCreateAgentBindingRequest(_ context.Context, rq *v1.CreateAgentBindingRequest) error {
	if err := validatePayload(rq.GetAgentBinding(), "agentBinding", v.ValidateAgentBindingRules()); err != nil {
		return err
	}

	return validateAgentBinding(rq.GetAgentBinding())
}

// ValidateUpdateAgentBindingRequest validates an update request.
func (v *Validator) ValidateUpdateAgentBindingRequest(_ context.Context, rq *v1.UpdateAgentBindingRequest) error {
	if err := validatePayload(rq.GetAgentBinding(), "agentBinding", v.ValidateAgentBindingRules()); err != nil {
		return err
	}

	return validateAgentBinding(rq.GetAgentBinding())
}

// ValidateGetAgentBindingRequest validates a get request.
func (v *Validator) ValidateGetAgentBindingRequest(_ context.Context, rq *v1.GetAgentBindingRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentBindingRules())
}

// ValidateDeleteAgentBindingRequest validates a delete request.
func (v *Validator) ValidateDeleteAgentBindingRequest(_ context.Context, rq *v1.DeleteAgentBindingRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentBindingRules())
}

// ValidateListAgentBindingRequest validates a list request.
func (v *Validator) ValidateListAgentBindingRequest(_ context.Context, rq *v1.ListAgentBindingRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// ValidatePreviewAgentBindingRequest runs the declarative binding rules against
// the preview spec. The dry run is deliberately as strict as the control plane:
// a binding that would be rejected on write should be rejected here, where the
// caller can still change it.
func (v *Validator) ValidatePreviewAgentBindingRequest(_ context.Context, rq *v1.PreviewAgentBindingRequest) error {
	var internal agent.AgentBindingSpec

	if err := agentv1alpha1.Convert_v1alpha1_AgentBindingSpec_To_agent_AgentBindingSpec(&rq.Spec, &internal, nil); err != nil {
		return errno.ErrInvalidArgument.WithMessage("spec is not a valid AgentBindingSpec: %s", err.Error())
	}

	if errs := agentvalidation.ValidateAgentBinding(&internal, field.NewPath("spec")); len(errs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("spec is invalid: %s", errs.ToAggregate().Error())
	}

	return nil
}

// validateAgentBinding mirrors the declarative rules in
// pkg/apis/agent/validation/agentbinding.go so a binding is refused at the REST
// edge for the same reasons the control plane would refuse it.
func validateAgentBinding(binding *v1.AgentBinding) error {
	spec := binding.GetSpec()
	if spec == nil {
		return errno.ErrInvalidArgument.WithMessage("agentBinding.spec is required")
	}

	if err := validateBindingSelection(spec); err != nil {
		return err
	}

	if err := validateBindingAgents(spec.GetAgents()); err != nil {
		return err
	}

	if err := validateBindingReferences(spec); err != nil {
		return err
	}

	if err := validateBindingCommands(spec.GetCommands()); err != nil {
		return err
	}

	return validateBindingCommandSelector(spec.GetCommandSelector())
}

// validateBindingCommandSelector checks the batch axis of a command grant.
//
// An explicitly empty selector is refused: metav1 treats {} as Everything(), so
// accepting it would grant every command in the catalogue. The agent selector
// deliberately reads {} as "every agent"; "every command" has no use case, so
// this spelling can only be a half-filled form.
func validateBindingCommandSelector(selector *v1.AgentBindingLabelSelector) error {
	if selector == nil {
		return nil
	}
	if len(selector.GetMatchLabels()) == 0 && len(selector.GetMatchExpressions()) == 0 {
		return errno.ErrInvalidArgument.WithMessage(
			"commandSelector: must not be empty; an empty selector matches every command")
	}
	if _, err := metav1.LabelSelectorAsSelector(toLabelSelector(selector)); err != nil {
		return errno.ErrInvalidArgument.WithMessage("commandSelector: %s", err.Error())
	}
	return nil
}

// validateBindingSelection checks that a binding selects something and that
// its selector, if any, is usable.
func validateBindingSelection(spec *v1.AgentBindingSpec) error {
	// A binding must select something. A selector or a name list satisfies
	// that; requiring a selector even when every agent is named would force
	// every hand-written audience binding to carry a placeholder selector.
	if spec.GetSelector() == nil && len(spec.GetAgents()) == 0 {
		return errno.ErrInvalidArgument.WithMessage(
			"a binding must select at least one agent: set selector, agents, or both")
	}

	if selector := spec.GetSelector(); selector != nil {
		if _, err := metav1.LabelSelectorAsSelector(toLabelSelector(selector)); err != nil {
			return errno.ErrInvalidArgument.WithMessage("selector: %s", err.Error())
		}
	}

	return nil
}

// validateBindingAgents checks the explicitly named agents.
func validateBindingAgents(agents []string) error {
	if len(agents) > maxBindingAgents {
		return errno.ErrInvalidArgument.WithMessage("agents: must hold at most %d entries", maxBindingAgents)
	}

	for _, name := range agents {
		if name == "" {
			return errno.ErrInvalidArgument.WithMessage("agents: an agent name must not be empty")
		}

		if msgs := k8svalidation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			return errno.ErrInvalidArgument.WithMessage("agents: %q: %s", name, msgs[0])
		}
	}

	return nil
}

// validateBindingReferences checks every reference list.
func validateBindingReferences(spec *v1.AgentBindingSpec) error {
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
		if err := validateReferenceList(list.field, list.refs); err != nil {
			return err
		}
	}
	if ref := spec.GetModelRef(); ref != nil {
		if err := validateReference("modelRef", ref); err != nil {
			return err
		}
	}

	return nil
}

// validateReferenceList checks one reference list's size and entries.
func validateReferenceList(field string, refs []*v1.ObjectReference) error {
	if len(refs) > maxBindingRefsPerKind {
		return errno.ErrInvalidArgument.WithMessage("%s: must hold at most %d entries", field, maxBindingRefsPerKind)
	}

	for _, ref := range refs {
		if err := validateReference(field, ref); err != nil {
			return err
		}
	}

	return nil
}

// validateReference checks one reference name.
func validateReference(field string, ref *v1.ObjectReference) error {
	if ref.GetName() == "" {
		return errno.ErrInvalidArgument.WithMessage("%s: a reference must name its target", field)
	}

	if msgs := k8svalidation.IsDNS1123Subdomain(ref.GetName()); len(msgs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("%s: %q: %s", field, ref.GetName(), msgs[0])
	}

	return nil
}

// validateBindingCommands checks the named command list.
func validateBindingCommands(commands []string) error {
	if len(commands) > maxBindingCommands {
		return errno.ErrInvalidArgument.WithMessage("commands: must hold at most %d entries", maxBindingCommands)
	}

	if slices.Contains(commands, "") {
		return errno.ErrInvalidArgument.WithMessage("commands: a command name must not be empty")
	}

	return nil
}

// toLabelSelector converts the wire selector to the Kubernetes type so the
// standard validation can decide whether it is usable.
func toLabelSelector(selector *v1.AgentBindingLabelSelector) *metav1.LabelSelector {
	out := &metav1.LabelSelector{MatchLabels: selector.GetMatchLabels(), MatchExpressions: nil}
	for _, requirement := range selector.GetMatchExpressions() {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      requirement.GetKey(),
			Operator: metav1.LabelSelectorOperator(requirement.GetOperator()),
			Values:   requirement.GetValues(),
		})
	}

	return out
}
