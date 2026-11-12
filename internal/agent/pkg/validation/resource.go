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
	"reflect"
	"strings"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// maxAgentPromptRefs caps the prompt list a management request may send. It
// mirrors the control plane's maxPromptRefs: a longer list is rejected by the
// Agent spec anyway, and the controller caps the aggregate at reconcile time.
const maxAgentPromptRefs = 16

// maxAgentTemplateRefs mirrors the control plane's maxAgentRefs: a longer list
// is rejected by the Agent spec anyway, but catching it here names the field in
// the REST error instead of surfacing the admission message.
const maxAgentTemplateRefs = 16

// requiredString rejects a blank string field. It is the rule every resource
// identifier uses: the store keys records by name (or secretId), and a blank
// identifier inserts a row nobody can address.
func requiredString(field string) genericvalidation.ValidatorFunc {
	return func(value any) error {
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errno.ErrInvalidArgument.WithMessage("%s is required", field)
		}

		return nil
	}
}

// nonNegativeInt rejects a negative integer field. It accepts every integer
// width because the generated request types mix int32 and int64.
func nonNegativeInt(field string) genericvalidation.ValidatorFunc {
	return func(value any) error {
		if isNegative(value) {
			return errno.ErrInvalidArgument.WithMessage("%s must be a non-negative integer", field)
		}

		return nil
	}
}

// isNegative reports whether value is a negative integer of any width.
func isNegative(value any) bool {
	switch v := value.(type) {
	case int:
		return v < 0
	case int32:
		return v < 0
	case int64:
		return v < 0
	default:
		return false
	}
}

// nameRules is the rule set for a resource addressed by name.
var nameRules = genericvalidation.Rules{"Name": requiredString("name")}

// secretIDRules is the rule set for a secret that is only read or deleted. It
// deliberately does not require DNS-1123: a credential migrated from the SQL
// table may have an ID the old API accepted, and it must remain addressable so
// it can be rotated or removed.
var secretIDRules = genericvalidation.Rules{"SecretId": requiredString("secretId")}

// secretCreateRules is the rule set for a new or updated secret. The core
// Secret that stores it must have a DNS-1123 name; the store can encode an
// historical ID, but a new one is rejected at the edge where the caller can fix
// it.
var secretCreateRules = genericvalidation.Rules{"SecretId": requiredDNS1123Subdomain("secretId")}

// requiredDNS1123Subdomain rejects a blank or non-DNS-1123 identifier.
func requiredDNS1123Subdomain(field string) genericvalidation.ValidatorFunc {
	return func(value any) error {
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errno.ErrInvalidArgument.WithMessage("%s is required", field)
		}
		if errs := k8svalidation.IsDNS1123Subdomain(s); len(errs) > 0 {
			return errno.ErrInvalidArgument.WithMessage("%s %q is not a valid DNS-1123 subdomain: %s", field, s, errs[0])
		}
		return nil
	}
}

// listRules is the rule set every list request shares.
var listRules = genericvalidation.Rules{
	"Offset": nonNegativeInt("offset"),
	"Limit":  nonNegativeInt("limit"),
}

// validatePayload rejects a request whose payload field is absent, then runs
// the rule set over the payload itself.
//
// The presence check is separate because the onexstack helper skips nil pointer
// fields instead of handing them to a rule, so a rule can never see the missing
// case.
func validatePayload(payload any, field string, rules genericvalidation.Rules) error {
	rv := reflect.ValueOf(payload)
	if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return errno.ErrInvalidArgument.WithMessage("%s is required", field)
	}

	normalizeIdentifiers(payload)

	return genericvalidation.ValidateAllFields(payload, rules)
}

// normalizeIdentifiers trims the resource identifier in place. The validator
// receives the request pointer, so the value the store later reads is the one
// that was checked: " helper " and "helper" address the same record, and only
// one spelling should reach the database.
func normalizeIdentifiers(payload any) {
	rv := reflect.ValueOf(payload)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		return
	}

	for _, field := range []string{"Name", "SecretId"} {
		value := rv.FieldByName(field)
		if value.IsValid() && value.Kind() == reflect.String && value.CanSet() {
			value.SetString(strings.TrimSpace(value.String()))
		}
	}
}

// --- Agents ---

// ValidateAgentRules returns the rules for an agent payload.
func (v *Validator) ValidateAgentRules() genericvalidation.Rules { return nameRules }

// ValidateCreateAgentRequest validates a CreateAgentRequest.
func (v *Validator) ValidateCreateAgentRequest(_ context.Context, rq *v1.CreateAgentRequest) error {
	if err := validatePayload(rq.GetAgent(), "agent", v.ValidateAgentRules()); err != nil {
		return err
	}
	if err := validateAgentPromptInput(rq.GetAgent()); err != nil {
		return err
	}
	if err := validateAgentTemplateInput(rq.GetAgent()); err != nil {
		return err
	}
	return validateAgentLocalRefs(rq.GetAgent())
}

// ValidateUpdateAgentRequest validates an UpdateAgentRequest.
func (v *Validator) ValidateUpdateAgentRequest(_ context.Context, rq *v1.UpdateAgentRequest) error {
	if err := validatePayload(rq.GetAgent(), "agent", v.ValidateAgentRules()); err != nil {
		return err
	}
	if err := validateAgentPromptInput(rq.GetAgent()); err != nil {
		return err
	}
	if err := validateAgentTemplateInput(rq.GetAgent()); err != nil {
		return err
	}
	return validateAgentLocalRefs(rq.GetAgent())
}

// validateAgentLocalRefs enforces the one rule that keeps Agent specs portable:
// every capability reference belongs to the agent's own namespace. Reaching
// across a namespace is an assignment decision, so it lives in an AgentBinding
// or ClusterAgentBinding.
func validateAgentLocalRefs(a *v1.Agent) error {
	if a == nil {
		return nil
	}
	lists := []struct {
		field string
		refs  []*v1.ObjectReference
	}{
		{"skills", a.GetSkills()},
		{"mcps", a.GetMcps()},
		{"knowledgeBases", a.GetKnowledgeBases()},
		{"customCommands", a.GetCustomCommands()},
		{"delegates", a.GetDelegates()},
	}
	for _, list := range lists {
		for i, ref := range list.refs {
			if err := validateAgentLocalRef(list.field, i, ref); err != nil {
				return err
			}
		}
	}
	singles := []struct {
		field string
		ref   *v1.ObjectReference
	}{
		{"model", a.GetModel()},
		{"memoryRef", a.GetMemoryRef()},
	}
	for _, single := range singles {
		if err := validateAgentLocalRef(single.field, 0, single.ref); err != nil {
			return err
		}
	}
	return nil
}

func validateAgentLocalRef(field string, index int, ref *v1.ObjectReference) error {
	if ref == nil {
		return nil
	}
	if ref.GetName() == "" {
		return errno.ErrInvalidArgument.WithMessage("%s: a reference must name its target", field)
	}
	if ref.GetNamespace() != "" {
		return errno.ErrInvalidArgument.WithMessage(
			"%s[%d] must be local to the agent; put cross-namespace references in an AgentBinding", field, index)
	}
	if msgs := k8svalidation.IsDNS1123Subdomain(ref.GetName()); len(msgs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("%s[%d] %q: %s", field, index, ref.GetName(), msgs[0])
	}
	return nil
}

// validateAgentPromptInput checks the prompt selection of a management request.
//
// Every entry must be a Prompt name in the agent's namespace, and the built-in
// sentinel belongs to the empty list, not to an entry. Free-form text is
// refused here rather than silently dropped, because the persona would
// otherwise disappear without a trace.
func validateAgentPromptInput(a *v1.Agent) error {
	if a == nil {
		return nil
	}
	prompts := a.GetPrompts()
	if len(prompts) > maxAgentPromptRefs {
		return errno.ErrInvalidArgument.WithMessage(
			"prompts must contain at most %d entries", maxAgentPromptRefs)
	}
	for i, ref := range prompts {
		name := ref.GetName()
		if ref.GetNamespace() != "" {
			return errno.ErrInvalidArgument.WithMessage(
				"prompts[%d] must be local to the agent; put cross-namespace references in an AgentBinding", i)
		}
		if name == agentv1alpha1.BuiltinPrompt {
			return errno.ErrInvalidArgument.WithMessage(
				"prompts[%d] must name a Prompt; an empty list selects the built-in default", i)
		}
		if msgs := k8svalidation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			return errno.ErrInvalidArgument.WithMessage(
				"prompts[%d] %q is not a Prompt name: %s", i, name, msgs[0])
		}
	}
	return nil
}

// validateAgentTemplateInput checks the template bindings of a management
// request.
//
// Templates are same-namespace by construction and named like any other
// resource; the controller reports a missing or disabled template, but a
// malformed name would be impossible to resolve at all.
func validateAgentTemplateInput(a *v1.Agent) error {
	if a == nil {
		return nil
	}
	refs := a.GetTemplateRefs()
	if len(refs) > maxAgentTemplateRefs {
		return errno.ErrInvalidArgument.WithMessage(
			"templateRefs must contain at most %d entries", maxAgentTemplateRefs)
	}
	for i, ref := range refs {
		name := ref.GetName()
		if ref.GetNamespace() != "" {
			return errno.ErrInvalidArgument.WithMessage(
				"templateRefs[%d] must be local to the agent; a template cannot cross namespaces", i)
		}
		if msgs := k8svalidation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			return errno.ErrInvalidArgument.WithMessage(
				"templateRefs[%d] %q is not an AgentTemplate name: %s", i, name, msgs[0])
		}
	}
	return nil
}

// ValidateGetAgentRequest validates a GetAgentRequest.
func (v *Validator) ValidateGetAgentRequest(_ context.Context, rq *v1.GetAgentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentRules())
}

// ValidateDeleteAgentRequest validates a DeleteAgentRequest.
func (v *Validator) ValidateDeleteAgentRequest(_ context.Context, rq *v1.DeleteAgentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentRules())
}

// ValidateListAgentRequest validates a ListAgentRequest.
func (v *Validator) ValidateListAgentRequest(_ context.Context, rq *v1.ListAgentRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- MCP servers ---

// ValidateMCPRules returns the rules for an MCP payload.
func (v *Validator) ValidateMCPRules() genericvalidation.Rules { return nameRules }

// ValidateCreateMCPRequest validates a CreateMCPRequest.
func (v *Validator) ValidateCreateMCPRequest(_ context.Context, rq *v1.CreateMCPRequest) error {
	return validatePayload(rq.GetMcp(), "mcp", v.ValidateMCPRules())
}

// ValidateUpdateMCPRequest validates an UpdateMCPRequest.
func (v *Validator) ValidateUpdateMCPRequest(_ context.Context, rq *v1.UpdateMCPRequest) error {
	return validatePayload(rq.GetMcp(), "mcp", v.ValidateMCPRules())
}

// ValidateGetMCPRequest validates a GetMCPRequest.
func (v *Validator) ValidateGetMCPRequest(_ context.Context, rq *v1.GetMCPRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateMCPRules())
}

// ValidateDeleteMCPRequest validates a DeleteMCPRequest.
func (v *Validator) ValidateDeleteMCPRequest(_ context.Context, rq *v1.DeleteMCPRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateMCPRules())
}

// ValidateListMCPRequest validates a ListMCPRequest.
func (v *Validator) ValidateListMCPRequest(_ context.Context, rq *v1.ListMCPRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// ValidateTestMCPRequest validates a TestMCPRequest.
//
// A test needs no name and persists nothing: the inline server is the whole
// request. An absent payload is installed rather than rejected so the biz layer
// can answer with the ordinary ok=false + "url is required" instead of a bind
// error, which is the same shape a caller sees for an unreachable endpoint.
func (v *Validator) ValidateTestMCPRequest(_ context.Context, rq *v1.TestMCPRequest) error {
	if rq.GetMcp() == nil {
		rq.Mcp = new(v1.MCP)
	}
	return nil
}

// ValidatePreviewA2ARequest checks only the request shape. Card reachability is
// an endpoint question, answered by the biz layer with ok=false so the console
// can show the upstream error without treating the API as broken.
func (v *Validator) ValidatePreviewA2ARequest(_ context.Context, rq *v1.PreviewAgentA2ARequest) error {
	if rq.GetExecution() == nil {
		rq.Execution = new(v1.AgentExecution)
	}
	return nil
}

// ValidateInvokeAgentA2ARequest checks the two things the wire needs: a target
// name and either a new message or a task to continue.
func (v *Validator) ValidateInvokeAgentA2ARequest(_ context.Context, rq *v1.InvokeAgentA2ARequest) error {
	if rq.GetName() == "" {
		return errno.ErrInvalidArgument
	}
	if rq.GetMessage() == "" && rq.GetTaskId() == "" {
		return errno.ErrInvalidArgument
	}
	if rq.GetTimeoutSeconds() < 0 {
		return errno.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListAgentA2AInvocationRequest(_ context.Context, rq *v1.ListAgentA2AInvocationRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

func (v *Validator) ValidateGetAgentA2AInvocationRequest(_ context.Context, rq *v1.GetAgentA2AInvocationRequest) error {
	if rq.GetName() == "" || rq.GetInvocationId() == "" {
		return errno.ErrInvalidArgument
	}
	return nil
}

// --- Custom commands ---

// ValidateCustomCommandRules returns the rules for a custom-command payload.
func (v *Validator) ValidateCustomCommandRules() genericvalidation.Rules { return nameRules }

// ValidateCreateCustomCommandRequest validates a CreateCustomCommandRequest.
//
// Only the request shape is checked here. The command definition itself is
// validated by the control-plane strategy, which sees the typed object and is
// the single authority for templates, URLs and parameter declarations.
func (v *Validator) ValidateCreateCustomCommandRequest(_ context.Context, rq *v1.CreateCustomCommandRequest) error {
	return validatePayload(rq.GetCustomCommand(), "customCommand", v.ValidateCustomCommandRules())
}

// ValidateUpdateCustomCommandRequest validates an UpdateCustomCommandRequest.
func (v *Validator) ValidateUpdateCustomCommandRequest(_ context.Context, rq *v1.UpdateCustomCommandRequest) error {
	return validatePayload(rq.GetCustomCommand(), "customCommand", v.ValidateCustomCommandRules())
}

// ValidateGetCustomCommandRequest validates a GetCustomCommandRequest.
func (v *Validator) ValidateGetCustomCommandRequest(_ context.Context, rq *v1.GetCustomCommandRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateCustomCommandRules())
}

// ValidateDeleteCustomCommandRequest validates a DeleteCustomCommandRequest.
func (v *Validator) ValidateDeleteCustomCommandRequest(_ context.Context, rq *v1.DeleteCustomCommandRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateCustomCommandRules())
}

// ValidateListCustomCommandRequest validates a ListCustomCommandRequest.
func (v *Validator) ValidateListCustomCommandRequest(_ context.Context, rq *v1.ListCustomCommandRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Skills ---

// ValidateSkillRules returns the rules for a skill payload.
func (v *Validator) ValidateSkillRules() genericvalidation.Rules { return nameRules }

// ValidateCreateSkillRequest validates a CreateSkillRequest.
func (v *Validator) ValidateCreateSkillRequest(_ context.Context, rq *v1.CreateSkillRequest) error {
	if err := validatePayload(rq.GetSkill(), "skill", v.ValidateSkillRules()); err != nil {
		return err
	}

	return validateSkillMode(rq.GetSkill())
}

// ValidateUpdateSkillRequest validates an UpdateSkillRequest.
//
// The mode check runs on the request as sent: a request that explicitly says
// mode=tool without an entrypoint is refused even when the stored row has one.
// Filling fields the client omitted is the biz update's job, because it owns
// the stored row the request is merged into.
func (v *Validator) ValidateUpdateSkillRequest(_ context.Context, rq *v1.UpdateSkillRequest) error {
	if err := validatePayload(rq.GetSkill(), "skill", v.ValidateSkillRules()); err != nil {
		return err
	}

	return validateSkillMode(rq.GetSkill())
}

// ValidateGetSkillRequest validates a GetSkillRequest.
func (v *Validator) ValidateGetSkillRequest(_ context.Context, rq *v1.GetSkillRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSkillRules())
}

// ValidateDeleteSkillRequest validates a DeleteSkillRequest.
func (v *Validator) ValidateDeleteSkillRequest(_ context.Context, rq *v1.DeleteSkillRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSkillRules())
}

// ValidateListSkillRequest validates a ListSkillRequest.
func (v *Validator) ValidateListSkillRequest(_ context.Context, rq *v1.ListSkillRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateSkillMode rejects a mode that cannot work. An empty mode is the
// records written before the field existed: they load the full skill into the
// server-side agent, which is what "hosted" names.
func validateSkillMode(s *v1.Skill) error {
	switch s.GetMode() {
	case "", "hosted":
		return nil
	case "tool":
		if strings.TrimSpace(s.GetEntrypoint()) == "" {
			return errno.ErrInvalidArgument.WithMessage("skill %q: mode=tool requires entrypoint", s.GetName())
		}

	case "guide":
		if strings.TrimSpace(s.GetContract()) == "" {
			return errno.ErrInvalidArgument.WithMessage("skill %q: mode=guide requires contract", s.GetName())
		}

	default:
		return errno.ErrInvalidArgument.WithMessage("skill %q: unknown mode %q (want tool, guide, or hosted)", s.GetName(), s.GetMode())
	}

	return nil
}

// --- Prompts ---

// ValidatePromptRules returns the rules for a prompt payload.
func (v *Validator) ValidatePromptRules() genericvalidation.Rules { return nameRules }

// ValidateCreatePromptRequest validates a CreatePromptRequest.
func (v *Validator) ValidateCreatePromptRequest(_ context.Context, rq *v1.CreatePromptRequest) error {
	return validatePayload(rq.GetPrompt(), "prompt", v.ValidatePromptRules())
}

// ValidateUpdatePromptRequest validates an UpdatePromptRequest.
func (v *Validator) ValidateUpdatePromptRequest(_ context.Context, rq *v1.UpdatePromptRequest) error {
	return validatePayload(rq.GetPrompt(), "prompt", v.ValidatePromptRules())
}

// ValidateGetPromptRequest validates a GetPromptRequest.
func (v *Validator) ValidateGetPromptRequest(_ context.Context, rq *v1.GetPromptRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidatePromptRules())
}

// ValidateDeletePromptRequest validates a DeletePromptRequest.
func (v *Validator) ValidateDeletePromptRequest(_ context.Context, rq *v1.DeletePromptRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidatePromptRules())
}

// ValidateListPromptRequest validates a ListPromptRequest.
func (v *Validator) ValidateListPromptRequest(_ context.Context, rq *v1.ListPromptRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Models ---

// ValidateModelRules returns the rules for a model payload.
func (v *Validator) ValidateModelRules() genericvalidation.Rules { return nameRules }

// ValidateCreateModelRequest validates a CreateModelRequest.
func (v *Validator) ValidateCreateModelRequest(_ context.Context, rq *v1.CreateModelRequest) error {
	if err := validatePayload(rq.GetModel(), "model", v.ValidateModelRules()); err != nil {
		return err
	}

	m := rq.GetModel()
	if m.GetOwner() != "" && m.GetApiKey() == "" {
		return errno.ErrInvalidArgument.WithMessage("a model endpoint requires an api key")
	}
	if agentmodel.IsSealedAPIKey(m.GetApiKey()) {
		return errno.ErrInvalidArgument.WithMessage(
			"api key must be plaintext; the enc:v1: form is reserved for storage")
	}

	return nil
}

// ValidateUpdateModelRequest validates an UpdateModelRequest.
//
// Filling the values a full-row update would otherwise erase is the biz
// update's job: it owns the stored row the request is merged into.
func (v *Validator) ValidateUpdateModelRequest(_ context.Context, rq *v1.UpdateModelRequest) error {
	if err := validatePayload(rq.GetModel(), "model", v.ValidateModelRules()); err != nil {
		return err
	}
	if agentmodel.IsSealedAPIKey(rq.GetModel().GetApiKey()) {
		return errno.ErrInvalidArgument.WithMessage(
			"api key must be plaintext; the enc:v1: form is reserved for storage")
	}
	return nil
}

// ValidateGetModelRequest validates a GetModelRequest.
func (v *Validator) ValidateGetModelRequest(_ context.Context, rq *v1.GetModelRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateModelRules())
}

// ValidateDeleteModelRequest validates a DeleteModelRequest.
func (v *Validator) ValidateDeleteModelRequest(_ context.Context, rq *v1.DeleteModelRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateModelRules())
}

// ValidateListModelRequest validates a ListModelRequest.
func (v *Validator) ValidateListModelRequest(_ context.Context, rq *v1.ListModelRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Knowledge bases ---

// ValidateKnowledgeBaseRules returns the rules for a knowledge-base payload.
func (v *Validator) ValidateKnowledgeBaseRules() genericvalidation.Rules { return nameRules }

// ValidateCreateKnowledgeBaseRequest validates a CreateKnowledgeBaseRequest.
func (v *Validator) ValidateCreateKnowledgeBaseRequest(_ context.Context, rq *v1.CreateKnowledgeBaseRequest) error {
	return validatePayload(rq.GetKnowledgeBase(), "knowledgeBase", v.ValidateKnowledgeBaseRules())
}

// ValidateUpdateKnowledgeBaseRequest validates an UpdateKnowledgeBaseRequest.
func (v *Validator) ValidateUpdateKnowledgeBaseRequest(_ context.Context, rq *v1.UpdateKnowledgeBaseRequest) error {
	return validatePayload(rq.GetKnowledgeBase(), "knowledgeBase", v.ValidateKnowledgeBaseRules())
}

// ValidateGetKnowledgeBaseRequest validates a GetKnowledgeBaseRequest.
func (v *Validator) ValidateGetKnowledgeBaseRequest(_ context.Context, rq *v1.GetKnowledgeBaseRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateKnowledgeBaseRules())
}

// ValidateDeleteKnowledgeBaseRequest validates a DeleteKnowledgeBaseRequest.
func (v *Validator) ValidateDeleteKnowledgeBaseRequest(_ context.Context, rq *v1.DeleteKnowledgeBaseRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateKnowledgeBaseRules())
}

// ValidateListKnowledgeBaseRequest validates a ListKnowledgeBaseRequest.
func (v *Validator) ValidateListKnowledgeBaseRequest(_ context.Context, rq *v1.ListKnowledgeBaseRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// maxRetrievalTestQuery caps one retrieval-test question. It is a request-shape
// guard: a question long enough to exceed it is a client bug, not a search.
const maxRetrievalTestQuery = 2000

// maxRetrievalTestTopK mirrors kbtool's cap. The two are kept in step because
// the handler is the only place a caller can see the limit before the request
// reaches the platform.
const maxRetrievalTestTopK = 50

// ValidateTestKnowledgeBaseRetrievalRequest validates a
// TestKnowledgeBaseRetrievalRequest.
//
// The optional numbers are checked only when present: nil means "use the
// record's or the deployment's value", which is valid, while an explicit 0 is
// a real request for the platform's own default.
func (v *Validator) ValidateTestKnowledgeBaseRetrievalRequest(
	_ context.Context, rq *v1.TestKnowledgeBaseRetrievalRequest,
) error {
	if strings.TrimSpace(rq.GetName()) == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}
	query := strings.TrimSpace(rq.GetQuery())
	if query == "" {
		return errno.ErrInvalidArgument.WithMessage("query is required")
	}
	if len([]rune(query)) > maxRetrievalTestQuery {
		return errno.ErrInvalidArgument.WithMessage("query must hold at most %d characters", maxRetrievalTestQuery)
	}
	if topK := rq.GetTopK(); topK < 0 || topK > maxRetrievalTestTopK {
		return errno.ErrInvalidArgument.WithMessage("topK must be between 0 and %d", maxRetrievalTestTopK)
	}
	if rq.ScoreThreshold != nil {
		if value := rq.GetScoreThreshold(); value < 0 || value > 1 {
			return errno.ErrInvalidArgument.WithMessage("scoreThreshold must be between 0 and 1")
		}
	}
	if rq.VectorSimilarityWeight != nil {
		if value := rq.GetVectorSimilarityWeight(); value < 0 || value > 1 {
			return errno.ErrInvalidArgument.WithMessage("vectorSimilarityWeight must be between 0 and 1")
		}
	}
	return nil
}

// --- Secrets ---

// ValidateSecretRules returns the rules for a secret payload.
func (v *Validator) ValidateSecretRules() genericvalidation.Rules { return secretIDRules }

// ValidateCreateSecretRequest validates a CreateSecretRequest.
func (v *Validator) ValidateCreateSecretRequest(_ context.Context, rq *v1.CreateSecretRequest) error {
	return validatePayload(rq.GetSecret(), "secret", secretCreateRules)
}

// ValidateUpdateSecretRequest validates an UpdateSecretRequest.
//
// The key is not returned by any read API, so a client writing back a secret it
// read does not have it. The biz update keeps the stored key when the request
// omits it; validation checks the request as sent.
func (v *Validator) ValidateUpdateSecretRequest(_ context.Context, rq *v1.UpdateSecretRequest) error {
	return validatePayload(rq.GetSecret(), "secret", secretCreateRules)
}

// ValidateGetSecretRequest validates a GetSecretRequest.
func (v *Validator) ValidateGetSecretRequest(_ context.Context, rq *v1.GetSecretRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSecretRules())
}

// ValidateDeleteSecretRequest validates a DeleteSecretRequest.
func (v *Validator) ValidateDeleteSecretRequest(_ context.Context, rq *v1.DeleteSecretRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSecretRules())
}

// ValidateListSecretRequest validates a ListSecretRequest.
func (v *Validator) ValidateListSecretRequest(_ context.Context, rq *v1.ListSecretRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Authorization rules ---

// ValidateAuthzRuleRules returns the rules for an authorization-rule payload.
func (v *Validator) ValidateAuthzRuleRules() genericvalidation.Rules { return nameRules }

// ValidateCreateAuthzRuleRequest validates a CreateAuthzRuleRequest.
func (v *Validator) ValidateCreateAuthzRuleRequest(_ context.Context, rq *v1.CreateAuthzRuleRequest) error {
	return validatePayload(rq.GetAuthzRule(), "authzRule", v.ValidateAuthzRuleRules())
}

// ValidateUpdateAuthzRuleRequest validates an UpdateAuthzRuleRequest.
func (v *Validator) ValidateUpdateAuthzRuleRequest(_ context.Context, rq *v1.UpdateAuthzRuleRequest) error {
	return validatePayload(rq.GetAuthzRule(), "authzRule", v.ValidateAuthzRuleRules())
}

// ValidateGetAuthzRuleRequest validates a GetAuthzRuleRequest.
func (v *Validator) ValidateGetAuthzRuleRequest(_ context.Context, rq *v1.GetAuthzRuleRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAuthzRuleRules())
}

// ValidateDeleteAuthzRuleRequest validates a DeleteAuthzRuleRequest.
func (v *Validator) ValidateDeleteAuthzRuleRequest(_ context.Context, rq *v1.DeleteAuthzRuleRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAuthzRuleRules())
}

// ValidateListAuthzRuleRequest validates a ListAuthzRuleRequest.
func (v *Validator) ValidateListAuthzRuleRequest(_ context.Context, rq *v1.ListAuthzRuleRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Run-lifecycle webhooks ---

// ValidateWebhookRules returns the rules for a webhook payload.
func (v *Validator) ValidateWebhookRules() genericvalidation.Rules { return nameRules }

// ValidateCreateWebhookRequest validates a CreateWebhookRequest.
func (v *Validator) ValidateCreateWebhookRequest(_ context.Context, rq *v1.CreateWebhookRequest) error {
	return validatePayload(rq.GetWebhook(), "webhook", v.ValidateWebhookRules())
}

// ValidateUpdateWebhookRequest validates an UpdateWebhookRequest.
func (v *Validator) ValidateUpdateWebhookRequest(_ context.Context, rq *v1.UpdateWebhookRequest) error {
	return validatePayload(rq.GetWebhook(), "webhook", v.ValidateWebhookRules())
}

// ValidateGetWebhookRequest validates a GetWebhookRequest.
func (v *Validator) ValidateGetWebhookRequest(_ context.Context, rq *v1.GetWebhookRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateWebhookRules())
}

// ValidateDeleteWebhookRequest validates a DeleteWebhookRequest.
func (v *Validator) ValidateDeleteWebhookRequest(_ context.Context, rq *v1.DeleteWebhookRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateWebhookRules())
}

// ValidateListWebhookRequest validates a ListWebhookRequest.
func (v *Validator) ValidateListWebhookRequest(_ context.Context, rq *v1.ListWebhookRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// --- Business lines ---

// namespaceNameRule rejects a name that a Kubernetes Namespace would reject.
// The name is the namespace, so this is also the API server's own rule.
func namespaceNameRule() genericvalidation.ValidatorFunc {
	return func(value any) error {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return errno.ErrInvalidArgument.WithMessage("name is required")
		}
		if errs := k8svalidation.IsDNS1123Label(name); len(errs) > 0 {
			return errno.ErrInvalidArgument.WithMessage("name %q is not a valid namespace: %s", name, strings.Join(errs, "; "))
		}
		return nil
	}
}

// namespaceLabelsRule validates label keys and values against the Kubernetes
// rules the Namespace object will apply anyway, so the caller gets a message
// instead of the API server's generic rejection.
func namespaceLabelsRule() genericvalidation.ValidatorFunc {
	return func(value any) error {
		labels, ok := value.(map[string]string)
		if !ok {
			return nil
		}
		for key, val := range labels {
			if errs := k8svalidation.IsQualifiedName(key); len(errs) > 0 {
				return errno.ErrInvalidArgument.WithMessage("label key %q is invalid: %s", key, strings.Join(errs, "; "))
			}
			if errs := k8svalidation.IsValidLabelValue(val); len(errs) > 0 {
				return errno.ErrInvalidArgument.WithMessage("label %q value is invalid: %s", key, strings.Join(errs, "; "))
			}
		}
		return nil
	}
}

// namespaceDisplayNameRule bounds the display text to the column width.
func namespaceDisplayNameRule() genericvalidation.ValidatorFunc {
	return func(value any) error {
		name, _ := value.(string)
		if len(name) > 128 {
			return errno.ErrInvalidArgument.WithMessage("displayName must be at most 128 bytes")
		}
		return nil
	}
}

// ValidateNamespaceRules returns the rules for a business-line payload.
func (v *Validator) ValidateNamespaceRules() genericvalidation.Rules {
	return genericvalidation.Rules{
		"Name":        namespaceNameRule(),
		"DisplayName": namespaceDisplayNameRule(),
		"Labels":      namespaceLabelsRule(),
	}
}

// ValidateCreateNamespaceRequest validates a CreateNamespaceRequest.
func (v *Validator) ValidateCreateNamespaceRequest(_ context.Context, rq *v1.CreateNamespaceRequest) error {
	return validatePayload(rq.GetNamespace(), "namespace", v.ValidateNamespaceRules())
}

// ValidateUpdateNamespaceRequest validates an UpdateNamespaceRequest.
func (v *Validator) ValidateUpdateNamespaceRequest(_ context.Context, rq *v1.UpdateNamespaceRequest) error {
	return validatePayload(rq.GetNamespace(), "namespace", v.ValidateNamespaceRules())
}

// ValidateGetNamespaceRequest validates a GetNamespaceRequest.
func (v *Validator) ValidateGetNamespaceRequest(_ context.Context, rq *v1.GetNamespaceRequest) error {
	return genericvalidation.ValidateAllFields(rq, genericvalidation.Rules{"Name": namespaceNameRule()})
}

// ValidateDeleteNamespaceRequest validates a DeleteNamespaceRequest.
func (v *Validator) ValidateDeleteNamespaceRequest(_ context.Context, rq *v1.DeleteNamespaceRequest) error {
	return genericvalidation.ValidateAllFields(rq, genericvalidation.Rules{"Name": namespaceNameRule()})
}

// ValidateListNamespaceRequest validates a ListNamespaceRequest.
func (v *Validator) ValidateListNamespaceRequest(_ context.Context, rq *v1.ListNamespaceRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}
