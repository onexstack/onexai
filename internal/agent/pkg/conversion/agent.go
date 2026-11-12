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
	"encoding/json"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// AgentV1ToAgentModel converts a proto Agent to the domain model.AgentM.
func AgentV1ToAgentModel(p *v1.Agent) *model.AgentM {
	if p == nil {
		return &model.AgentM{}
	}
	return &model.AgentM{
		Name:        p.GetName(),
		DisplayName: p.GetDisplayName(),
		Status:      p.GetStatus(),
		Description: p.GetDescription(),
		Owner:       p.GetOwner(),
		Model:       objectRefToEntry(p.GetNamespace(), p.GetModel()),
		PromptRefs:  objectRefsToEntries(p.GetNamespace(), p.GetPrompts()),
		// Keep nil and an explicit empty slice distinct: nil means "no opinion"
		// (leave the spec alone), while [] means "clear the binding".
		TemplateRefs:    objectRefsToEntries(p.GetNamespace(), p.GetTemplateRefs()),
		Skills:          objectRefsToEntries(p.GetNamespace(), p.GetSkills()),
		MCPs:            objectRefsToEntries(p.GetNamespace(), p.GetMcps()),
		KnowledgeBases:  objectRefsToEntries(p.GetNamespace(), p.GetKnowledgeBases()),
		CustomCommands:  objectRefsToEntries(p.GetNamespace(), p.GetCustomCommands()),
		RequireApproval: p.GetRequireApprovals(),
		AuthzBackends:   p.GetAuthzBackends(),
		MemoryRef:       objectRefToEntry(p.GetNamespace(), p.GetMemoryRef()),
		Namespace:       p.GetNamespace(),
		Labels:          p.GetLabels(),
		CreatedAt:       p.GetCreatedAt(),
		UpdatedAt:       p.GetUpdatedAt(),
		Kind:            p.GetKind(),
		Calls:           model.StringList(p.GetCalls()),
		Delegates:       objectRefsToEntries(p.GetNamespace(), p.GetDelegates()),
		// Webhooks are bare names: a hook row has no namespace of its own here,
		// and the dispatcher resolves them by name. Same nil/empty contract as
		// the reference lists above.
		Webhooks:      model.StringList(p.GetWebhooks()),
		AuthnWebhooks: model.StringList(p.GetAuthnWebhooks()),
		AuthzWebhooks: model.StringList(p.GetAuthzWebhooks()),
		ExecutionJSON: marshalJSON(executionToModel(p.GetExecution())),
		ContractJSON:  marshalJSON(contractToModel(p.GetContract())),
		SharingJSON:   marshalJSON(sharingToModel(p.GetSharing())),
	}
}

// AgentModelToAgentV1 converts a domain model.AgentM to a proto Agent.
func AgentModelToAgentV1(a *model.AgentM) *v1.Agent {
	if a == nil {
		return nil
	}
	out := &v1.Agent{
		Name:          a.Name,
		DisplayName:   a.DisplayName,
		Status:        StatusToV1(a.Status),
		Description:   a.Description,
		Owner:         a.Owner,
		Model:         entryToObjectRef(a.Namespace, a.Model),
		ResolvedModel: a.EffectiveModel(),
		// The runtime stores references as canonical strings; the API edge is
		// structured. A reference in the agent's own namespace is written with
		// an empty namespace, which is the only form an Agent spec may use.
		Prompts:          entriesToObjectRefs(a.Namespace, a.PromptRefs),
		TemplateRefs:     entriesToObjectRefs(a.Namespace, a.TemplateRefs),
		Skills:           entriesToObjectRefs(a.Namespace, a.Skills),
		Mcps:             entriesToObjectRefs(a.Namespace, a.MCPs),
		KnowledgeBases:   entriesToObjectRefs(a.Namespace, a.KnowledgeBases),
		CustomCommands:   entriesToObjectRefs(a.Namespace, a.CustomCommands),
		RequireApprovals: []string(a.RequireApproval),
		AuthzBackends:    a.AuthzBackends,
		MemoryRef:        entryToObjectRef(a.Namespace, a.MemoryRef),
		Namespace:        a.Namespace,
		Labels:           a.Labels,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
		Kind:             a.Kind,
		Calls:            append([]string(nil), a.Calls...),
		Delegates:        entriesToObjectRefs(a.Namespace, a.Delegates),
		// Preserve nil vs explicit empty so a GET-then-PUT round trip does not
		// turn "no opinion" into "clear the hooks".
		Webhooks:      []string(a.Webhooks),
		AuthnWebhooks: []string(a.AuthnWebhooks),
		AuthzWebhooks: []string(a.AuthzWebhooks),
	}
	var execution model.AgentExecutionM
	if unmarshalJSON(a.ExecutionJSON, &execution) {
		out.Execution = executionFromModel(&execution)
	}
	var contract model.AgentContractM
	if unmarshalJSON(a.ContractJSON, &contract) {
		out.Contract = contractFromModel(&contract)
	}
	var sharing model.AgentSharingM
	if unmarshalJSON(a.SharingJSON, &sharing) {
		out.Sharing = sharingFromModel(&sharing)
	}
	var status model.AgentA2AStatusM
	if unmarshalJSON(a.A2AStatusJSON, &status) {
		out.A2A = a2aStatusFromModel(&status)
	}
	flattenA2AStatus(out)
	return out
}

// objectRefsToEntries converts the structured wire shape to the canonical
// (namespace/name) entries the runtime store indexes by.
func objectRefsToEntries(defaultNamespace string, refs []*v1.ObjectReference) model.StringList {
	if refs == nil {
		return nil
	}
	out := make(model.StringList, 0, len(refs))
	for _, ref := range refs {
		if ref.GetName() == "" {
			continue
		}
		out = append(out, model.RefOf(defaultNamespace, ref.GetNamespace(), ref.GetName()).Canonical())
	}
	return out
}

func objectRefToEntry(defaultNamespace string, ref *v1.ObjectReference) string {
	if ref == nil || ref.GetName() == "" {
		return ""
	}
	return model.RefOf(defaultNamespace, ref.GetNamespace(), ref.GetName()).Canonical()
}

// entriesToObjectRefs converts canonical entries back to the wire shape. A
// reference in the default namespace is written with an empty namespace, which
// is the only form an Agent spec may carry.
func entriesToObjectRefs(defaultNamespace string, entries []string) []*v1.ObjectReference {
	if entries == nil {
		return nil
	}
	out := make([]*v1.ObjectReference, 0, len(entries))
	for _, entry := range entries {
		if ref := entryToObjectRef(defaultNamespace, entry); ref != nil {
			out = append(out, ref)
		}
	}
	return out
}

func entryToObjectRef(defaultNamespace, entry string) *v1.ObjectReference {
	if entry == "" {
		return nil
	}
	ref := model.ResolveRef(defaultNamespace, entry)
	if ref.Name == "" {
		return nil
	}
	out := &v1.ObjectReference{Name: ref.Name}
	if ref.Namespace != model.NormalizeNamespace(defaultNamespace) {
		namespace := ref.Namespace
		out.Namespace = &namespace
	}
	return out
}

func marshalJSON(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return ""
	}
	return string(data)
}

func clientConfigToRaw(cc *v1.ClientConfig) json.RawMessage {
	if cc == nil {
		return nil
	}
	data, err := json.Marshal(ClientConfigV1ToCR(cc))
	if err != nil || string(data) == "null" {
		return nil
	}
	return data
}

func clientConfigFromRaw(raw json.RawMessage) *v1.ClientConfig {
	if len(raw) == 0 {
		return nil
	}
	cfg := &agentv1alpha1.ClientConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil
	}
	return ClientConfigFromCR(cfg)
}

func unmarshalJSON(raw string, v any) bool {
	if raw == "" || v == nil {
		return false
	}
	return json.Unmarshal([]byte(raw), v) == nil
}

// --- proto -> model ---

func executionToModel(e *v1.AgentExecution) *model.AgentExecutionM {
	if e == nil || (e.GetMode() == "" && e.GetA2A() == nil) {
		return nil
	}
	out := &model.AgentExecutionM{Mode: e.GetMode()}
	if a := e.GetA2A(); a != nil && !isEmptyA2A(a) {
		out.A2A = &model.A2AExecutionM{
			RuntimeRef:               a.GetRuntimeRef(),
			Endpoint:                 a.GetEndpoint(),
			ClientConfig:             clientConfigToRaw(a.GetClientConfig()),
			Source:                   a.GetSource(),
			CardURL:                  a.GetCardUrl(),
			Card:                     a.GetCard(),
			ProtocolVersion:          a.GetProtocolVersion(),
			Auth:                     authToModel(a.GetAuth()),
			TimeoutSeconds:           a.GetTimeoutSeconds(),
			MaxConcurrentInvocations: a.GetMaxConcurrentInvocations(),
			MetadataJSON:             a.GetMetadataJson(),
		}
	}
	return out
}

// isEmptyA2A reports an a2a block that carries no information. A form that
// renders the fields but leaves them blank must not turn "no A2A" into "A2A
// configured with empty values", which validation would reject for inproc.
func isEmptyA2A(a *v1.A2AExecution) bool {
	if a == nil {
		return true
	}
	return a.GetRuntimeRef() == "" && a.GetEndpoint() == "" && a.GetSource() == "" &&
		a.GetCardUrl() == "" && a.GetCard() == "" && a.GetProtocolVersion() == "" &&
		a.GetAuth() == nil && a.GetTimeoutSeconds() == 0 && a.GetClientConfig() == nil &&
		a.GetMaxConcurrentInvocations() == 0 && a.GetMetadataJson() == ""
}

func authToModel(a *v1.A2AAuth) *model.A2AAuthM {
	if a == nil {
		return nil
	}
	return &model.A2AAuthM{
		Type:               a.GetType(),
		APIKeyHeader:       a.GetApiKeyHeader(),
		HeaderNames:        append([]string(nil), a.GetHeaderNames()...),
		TokenURL:           a.GetTokenUrl(),
		ClientID:           a.GetClientId(),
		Scopes:             append([]string(nil), a.GetScopes()...),
		Audience:           a.GetAudience(),
		ClientAuth:         a.GetClientAuth(),
		InsecureSkipVerify: a.GetInsecureSkipVerify(),
		HasCredentials:     a.GetHasCredentials(),
		Credential:         a.GetCredential(),
		ClientSecret:       a.GetClientSecret(),
		ClearCredentials:   a.GetClearCredentials(),
	}
}

func contractToModel(c *v1.AgentContract) *model.AgentContractM {
	if c == nil {
		return nil
	}
	return &model.AgentContractM{
		AllowedSkills: append([]string(nil), c.GetAllowedSkills()...),
		InputModes:    append([]string(nil), c.GetInputModes()...),
		OutputModes:   append([]string(nil), c.GetOutputModes()...),
	}
}

func sharingToModel(s *v1.AgentSharing) *model.AgentSharingM {
	if s == nil {
		return nil
	}
	return &model.AgentSharingM{AllowedNamespaces: append([]string(nil), s.GetAllowedNamespaces()...)}
}

// --- model -> proto ---

func executionFromModel(e *model.AgentExecutionM) *v1.AgentExecution {
	if e == nil {
		return nil
	}
	out := &v1.AgentExecution{Mode: e.Mode}
	if a := e.A2A; a != nil {
		out.A2A = &v1.A2AExecution{
			RuntimeRef:               a.RuntimeRef,
			Endpoint:                 a.Endpoint,
			ClientConfig:             clientConfigFromRaw(a.ClientConfig),
			Source:                   a.Source,
			CardUrl:                  a.CardURL,
			Card:                     a.Card,
			ProtocolVersion:          a.ProtocolVersion,
			Auth:                     authFromModel(a.Auth),
			TimeoutSeconds:           a.TimeoutSeconds,
			MaxConcurrentInvocations: a.MaxConcurrentInvocations,
			MetadataJson:             a.MetadataJSON,
		}
	}
	return out
}

func authFromModel(a *model.A2AAuthM) *v1.A2AAuth {
	if a == nil {
		return nil
	}
	return &v1.A2AAuth{
		Type:               a.Type,
		ApiKeyHeader:       a.APIKeyHeader,
		HeaderNames:        append([]string(nil), a.HeaderNames...),
		TokenUrl:           a.TokenURL,
		ClientId:           a.ClientID,
		Scopes:             append([]string(nil), a.Scopes...),
		Audience:           a.Audience,
		ClientAuth:         a.ClientAuth,
		InsecureSkipVerify: a.InsecureSkipVerify,
		HasCredentials:     a.HasCredentials,
	}
}

func contractFromModel(c *model.AgentContractM) *v1.AgentContract {
	if c == nil {
		return nil
	}
	return &v1.AgentContract{
		AllowedSkills: append([]string(nil), c.AllowedSkills...),
		InputModes:    append([]string(nil), c.InputModes...),
		OutputModes:   append([]string(nil), c.OutputModes...),
	}
}

func sharingFromModel(s *model.AgentSharingM) *v1.AgentSharing {
	if s == nil {
		return nil
	}
	return &v1.AgentSharing{AllowedNamespaces: append([]string(nil), s.AllowedNamespaces...)}
}

func a2aStatusFromModel(s *model.AgentA2AStatusM) *v1.AgentA2AStatus {
	if s == nil {
		return nil
	}
	out := &v1.AgentA2AStatus{
		Phase:                s.Phase,
		ProtocolVersion:      s.ProtocolVersion,
		Transport:            s.Transport,
		ResolvedEndpoint:     s.ResolvedEndpoint,
		CardVersion:          s.CardVersion,
		CardDigest:           s.CardDigest,
		CardFetchedAt:        s.CardFetchedAt,
		LastSyncError:        s.LastSyncError,
		AuthReady:            s.AuthReady,
		Provider:             s.Provider,
		InputModes:           append([]string(nil), s.InputModes...),
		OutputModes:          append([]string(nil), s.OutputModes...),
		SecuritySchemes:      s.SecuritySchemes,
		Card:                 s.Card,
		ObservedRefreshToken: s.ObservedRefreshToken,
		Capabilities: &v1.A2ACapabilities{
			Streaming:              s.Capabilities.Streaming,
			PushNotifications:      s.Capabilities.PushNotifications,
			ExtendedAgentCard:      s.Capabilities.ExtendedAgentCard,
			StateTransitionHistory: s.Capabilities.StateTransitionHistory,
		},
	}
	for _, skill := range s.Skills {
		out.Skills = append(out.Skills, &v1.A2ASkill{
			Id:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Tags:        append([]string(nil), skill.Tags...),
			Examples:    append([]string(nil), skill.Examples...),
			InputModes:  append([]string(nil), skill.InputModes...),
			OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	for _, ext := range s.Extensions {
		out.Extensions = append(out.Extensions, &v1.A2AExtension{
			Uri: ext.URI, Description: ext.Description, Required: ext.Required,
		})
	}
	for _, snapshot := range s.CardHistory {
		out.CardHistory = append(out.CardHistory, &v1.A2ACardSnapshot{
			Version:        snapshot.Version,
			Digest:         snapshot.Digest,
			FetchedAt:      snapshot.FetchedAt,
			SkillCount:     snapshot.SkillCount,
			InterfaceCount: snapshot.InterfaceCount,
		})
	}
	return out
}

// flattenA2AStatus fills the list/table summary fields from status.a2a and
// spec.execution. The detail view still reads the structured sub-objects.
func flattenA2AStatus(a *v1.Agent) {
	if a == nil || a.A2A == nil {
		return
	}
	s := a.A2A
	a.Phase = s.GetPhase()
	a.ProtocolVersion = s.GetProtocolVersion()
	a.Transport = s.GetTransport()
	a.Endpoint = s.GetResolvedEndpoint()
	if a.Endpoint == "" && a.Execution != nil && a.Execution.A2A != nil {
		a.Endpoint = a.Execution.A2A.GetEndpoint()
	}
	a.Source = ""
	if a.Execution != nil && a.Execution.A2A != nil {
		a.Source = a.Execution.A2A.GetSource()
	}
	a.CardVersion = s.GetCardVersion()
	a.CardDigest = s.GetCardDigest()
	a.CardFetchedAt = s.GetCardFetchedAt()
	if caps := s.GetCapabilities(); caps != nil {
		if caps.GetStreaming() {
			a.Capabilities = append(a.Capabilities, "streaming")
		}
		if caps.GetPushNotifications() {
			a.Capabilities = append(a.Capabilities, "pushNotifications")
		}
		if caps.GetExtendedAgentCard() {
			a.Capabilities = append(a.Capabilities, "extendedAgentCard")
		}
	}
	a.SkillCount = int32(len(s.GetSkills()))
	a.SkillNames = make([]string, 0, len(s.GetSkills()))
	for _, skill := range s.GetSkills() {
		if skill.GetName() != "" {
			a.SkillNames = append(a.SkillNames, skill.GetName())
		}
	}
}
