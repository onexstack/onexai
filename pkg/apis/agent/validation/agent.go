// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"net/url"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	"github.com/onexstack/onexai/pkg/apis/agent/validation/outputschema"
)

// ValidateAgent checks an agent before it is persisted.
//
// The checks are about shape, not reachability: whether the referenced objects
// exist is the controller's job, because a reference may legitimately be written
// before the thing it names.
// maxPromptRefs caps the number of explicitly named prompts. Selector matches
// are capped separately at reconcile time, where the matched set is known.
const maxPromptRefs = 16

// maxAgentRefs caps the number of explicitly named aggregated agents. Selector
// matches are capped separately at reconcile time, like prompt sources.
const maxAgentRefs = 16

func ValidateAgent(spec *agent.AgentSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateAgentA2A(spec, fldPath)...)

	promptRefs := 0
	if spec.PromptRef != nil {
		promptRefs++
		allErrs = append(allErrs, validatePromptReference(spec.PromptRef, fldPath.Child("promptRef"))...)
	}
	if len(spec.Prompts) > 0 {
		promptRefs += len(spec.Prompts)
		allErrs = append(allErrs, validatePromptReferences(spec.Prompts, fldPath.Child("prompts"))...)
	}
	if promptRefs > maxPromptRefs {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("prompts"), promptRefs, maxPromptRefs))
	}
	if spec.PromptSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.PromptSelector, fldPath.Child("promptSelector"))...)
	}

	if spec.PromptAggregation != nil && len(spec.PromptAggregation.RegenerateToken) > 128 {
		allErrs = append(allErrs, field.TooLong(fldPath.Child("promptAggregation", "regenerateToken"),
			"", 128))
	}

	// Templates are same-namespace by construction: expansion happens in the
	// controller and in the runtime store, both of which address one namespace,
	// so a cross-namespace template could never be resolved.
	if len(spec.TemplateRefs) > maxAgentRefs {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("templateRefs"), len(spec.TemplateRefs), maxAgentRefs))
	}
	for i := range spec.TemplateRefs {
		refPath := fldPath.Child("templateRefs").Index(i)
		allErrs = append(allErrs, validateReference(&spec.TemplateRefs[i], refPath)...)
		if spec.TemplateRefs[i].Namespace != "" {
			allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), spec.TemplateRefs[i].Namespace,
				"a template must be in the same namespace as the agent that names it"))
		}
	}

	// Aggregated agents are same-namespace by construction: the controller's
	// watch, the runtime store and the selector all address one namespace, so a
	// cross-namespace source could never be resolved at run time.
	if len(spec.Agents) > 0 {
		if len(spec.Agents) > maxAgentRefs {
			allErrs = append(allErrs, field.TooMany(fldPath.Child("agents"), len(spec.Agents), maxAgentRefs))
		}
		for i := range spec.Agents {
			refPath := fldPath.Child("agents").Index(i)
			allErrs = append(allErrs, validateReference(&spec.Agents[i], refPath)...)
			if spec.Agents[i].Namespace != "" {
				allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), spec.Agents[i].Namespace,
					"an aggregated agent must be in the same namespace as the agent that names it"))
			}
		}
	}
	if spec.AgentSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.AgentSelector, fldPath.Child("agentSelector"))...)
	}

	// A delegate runs through the same namespace-addressed store, so like an
	// aggregated agent it can only be one in this agent's namespace.
	if len(spec.Delegates) > maxAgentRefs {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("delegates"), len(spec.Delegates), maxAgentRefs))
	}
	for i := range spec.Delegates {
		refPath := fldPath.Child("delegates").Index(i)
		allErrs = append(allErrs, validateReference(&spec.Delegates[i], refPath)...)
		if spec.Delegates[i].Namespace != "" {
			allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), spec.Delegates[i].Namespace,
				"a delegate must be in the same namespace as the agent that names it"))
		}
	}

	// An Agent spec names only objects in its own namespace. Cross-namespace
	// capabilities are injected by an AgentBinding / ClusterAgentBinding, where
	// the target's ReferenceGrant is part of the same object that names it.
	if spec.ModelRef != nil {
		allErrs = append(allErrs, validateLocalReference(spec.ModelRef, fldPath.Child("modelRef"), "model")...)
	}

	allErrs = append(allErrs, validateLocalReferences(spec.Skills, fldPath.Child("skills"))...)
	allErrs = append(allErrs, validateLocalReferences(spec.MCPs, fldPath.Child("mcps"))...)
	allErrs = append(allErrs, validateLocalReferences(spec.KnowledgeBases, fldPath.Child("knowledgeBases"))...)
	allErrs = append(allErrs, validateReferences(spec.CustomCommands, fldPath.Child("customCommands"))...)
	// Custom commands are not shareable: their body is compiled into the chat
	// server and executed with the calling tenant's authority, so a reference
	// from another namespace would be a grant nobody declared.
	for i := range spec.CustomCommands {
		if spec.CustomCommands[i].Namespace != "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("customCommands").Index(i).Child("namespace"),
				spec.CustomCommands[i].Namespace, "a custom command must be in the agent's namespace"))
		}
	}

	if spec.SkillSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.SkillSelector, fldPath.Child("skillSelector"))...)
	}
	if spec.MCPServerSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.MCPServerSelector, fldPath.Child("mcpSelector"))...)
	}
	if spec.KnowledgeBaseSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.KnowledgeBaseSelector, fldPath.Child("knowledgeBaseSelector"))...)
	}
	if spec.CustomCommandSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.CustomCommandSelector,
			fldPath.Child("customCommandSelector"))...)
	}

	if spec.MaxSkills < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxSkills"), spec.MaxSkills, "must not be negative"))
	}

	for i, pattern := range spec.RequireApproval {
		if pattern == "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("requireApproval").Index(i), pattern, "must not be empty"))
		}
	}

	if spec.MemoryRef != nil {
		allErrs = append(allErrs, validateLocalReference(spec.MemoryRef, fldPath.Child("memoryRef"), "memory")...)
	}

	if spec.OutputSchema != nil {
		canonical, _, err := outputschema.Compile(spec.OutputSchema.Raw)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("outputSchema"), string(spec.OutputSchema.Raw), err.Error()))
		} else if err := outputschema.CheckPortable(canonical, spec.OutputSchemaStrict); err != nil {
			// Refused at admission rather than at the end of a paid run: a
			// provider that cannot accept the schema rejects the request, and
			// finding that out after generation has started is too late.
			allErrs = append(allErrs, field.Invalid(fldPath.Child("outputSchema"), string(spec.OutputSchema.Raw), err.Error()))
		}
	}

	if spec.Runtime != nil && spec.Runtime.ProfileRef != nil {
		refPath := fldPath.Child("runtime", "profileRef")
		allErrs = append(allErrs, validateReference(spec.Runtime.ProfileRef, refPath)...)
		if spec.Runtime.ProfileRef.Namespace != "" {
			allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"),
				spec.Runtime.ProfileRef.Namespace,
				"a runtime profile must be in the agent's namespace"))
		}
	}

	return allErrs
}

func validateReferences(refs []agent.Reference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i := range refs {
		allErrs = append(allErrs, validateReference(&refs[i], fldPath.Index(i))...)
	}
	return allErrs
}

// validatePromptReferences validates a list of Prompt references.
func validatePromptReferences(refs []agent.Reference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i := range refs {
		allErrs = append(allErrs, validatePromptReference(&refs[i], fldPath.Index(i))...)
	}
	return allErrs
}

// validatePromptReference additionally requires a name shaped like a DNS
// subdomain. A cross-namespace reference is refused for the same reason every
// other Agent-spec capability reference is: an Agent names objects in its own
// namespace, and cross-namespace capability is injected by a Binding, where the
// grant and the reference live in the same object.
func validatePromptReference(ref *agent.Reference, fldPath *field.Path) field.ErrorList {
	allErrs := validateReference(ref, fldPath)
	if ref.Name != "" {
		for _, msg := range validation.IsDNS1123Subdomain(ref.Name) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), ref.Name, msg))
		}
	}
	if ref.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("namespace"), ref.Namespace,
			"a prompt must be in the agent's namespace; put cross-namespace references in an AgentBinding"))
	}
	return allErrs
}

// validateLocalReferences is validateReferences for a capability kind an Agent
// may only name in its own namespace.
func validateLocalReferences(refs []agent.Reference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i := range refs {
		allErrs = append(allErrs, validateLocalReference(&refs[i], fldPath.Index(i), "capability")...)
	}
	return allErrs
}

// validateLocalReference refuses a namespace on a reference whose kind is
// addressed in one namespace by the controller and the runtime.
func validateLocalReference(ref *agent.Reference, fldPath *field.Path, kind string) field.ErrorList {
	allErrs := validateReference(ref, fldPath)
	if ref.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("namespace"), ref.Namespace,
			"a "+kind+" must be in the agent's namespace; put cross-namespace references in an AgentBinding"))
	}
	return allErrs
}

func validateReference(ref *agent.Reference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "a reference must name an object"))
	}
	if ref.Namespace != "" {
		for _, msg := range validation.IsDNS1123Label(ref.Namespace) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("namespace"), ref.Namespace, msg))
		}
	}
	return allErrs
}

func validateSelector(selector *metav1.LabelSelector, fldPath *field.Path) field.ErrorList {
	if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
		return field.ErrorList{field.Invalid(fldPath, selector, err.Error())}
	}
	return nil
}

// maxCalls caps the number of agents one agent may call as tools. The limit is
// deliberately the same order as the other reference lists: a tool list large
// enough to hide the agent's own tools is a modelling error, not a preference.
const maxCalls = 32

// validateAgentA2A checks the kind/execution combination. It is the single
// place that decides which fields belong to which kind; the controller and the
// console render from the same rule.
func validateAgentA2A(spec *agent.AgentSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	kind := spec.Kind
	if kind == "" {
		kind = agent.AgentKindNative
	}
	if kind != agent.AgentKindNative && kind != agent.AgentKindExternal {
		return append(allErrs, field.NotSupported(fldPath.Child("kind"), kind,
			[]string{string(agent.AgentKindNative), string(agent.AgentKindExternal)}))
	}

	mode := agent.ExecutionModeInproc
	if spec.Execution != nil && spec.Execution.Mode != "" {
		mode = spec.Execution.Mode
	}
	if mode != agent.ExecutionModeInproc && mode != agent.ExecutionModeA2A {
		return append(allErrs, field.NotSupported(fldPath.Child("execution", "mode"), mode,
			[]string{string(agent.ExecutionModeInproc), string(agent.ExecutionModeA2A)}))
	}

	if kind == agent.AgentKindExternal {
		if mode != agent.ExecutionModeA2A {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("execution", "mode"), mode,
				"an external agent must execute over A2A"))
		}
		if hasNativeBehavior(spec) {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("kind"),
				"an external agent's behavior is defined by its AgentCard; platform behavior fields must be empty"))
		}
		if len(spec.Calls) > 0 {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("calls"),
				"an external agent's behavior is defined remotely; calls must be empty"))
		}
	} else {
		if mode == agent.ExecutionModeInproc && spec.Execution != nil && spec.Execution.A2A != nil {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("execution", "a2a"),
				"a2a settings require execution.mode=a2a"))
		}
		if len(spec.Calls) > maxCalls {
			allErrs = append(allErrs, field.TooMany(fldPath.Child("calls"), len(spec.Calls), maxCalls))
		}
		for i := range spec.Calls {
			refPath := fldPath.Child("calls").Index(i)
			allErrs = append(allErrs, validateReference(&spec.Calls[i], refPath)...)
			if spec.Calls[i].Namespace != "" {
				allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), spec.Calls[i].Namespace,
					"a call target must be in the agent's namespace"))
			}
		}
	}

	if mode == agent.ExecutionModeA2A {
		if spec.Execution == nil || spec.Execution.A2A == nil {
			allErrs = append(allErrs, field.Required(fldPath.Child("execution", "a2a"),
				"a2a settings are required when execution.mode=a2a"))
		} else {
			allErrs = append(allErrs, validateA2AExecution(spec.Execution.A2A, kind, fldPath.Child("execution", "a2a"))...)
		}
	}

	if spec.Sharing != nil {
		if kind != agent.AgentKindExternal {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("sharing"),
				"sharing applies to external agents only"))
		}
		for i, ns := range spec.Sharing.AllowedNamespaces {
			for _, msg := range validation.IsDNS1123Label(ns) {
				allErrs = append(allErrs, field.Invalid(fldPath.Child("sharing", "allowedNamespaces").Index(i), ns, msg))
			}
		}
	}

	return allErrs
}

// hasNativeBehavior reports whether any field that only the platform can define
// is set. External agents must leave all of them empty.
//
// Webhooks are deliberately not part of this set. They are platform-side
// governance rather than agent behaviour: the runtime asks the run-lifecycle
// hooks before it branches to the peer, so an external agent's runs are gated
// by onStart exactly like a native one's.
func hasNativeBehavior(spec *agent.AgentSpec) bool {
	return spec.PromptRef != nil || len(spec.Prompts) > 0 || spec.PromptSelector != nil ||
		len(spec.TemplateRefs) > 0 || len(spec.Agents) > 0 || spec.AgentSelector != nil ||
		spec.ModelRef != nil || len(spec.Skills) > 0 || spec.SkillSelector != nil ||
		len(spec.MCPs) > 0 || spec.MCPServerSelector != nil ||
		len(spec.KnowledgeBases) > 0 || spec.KnowledgeBaseSelector != nil ||
		len(spec.CustomCommands) > 0 || spec.CustomCommandSelector != nil ||
		len(spec.RequireApproval) > 0 ||
		len(spec.AuthzBackends) > 0 || spec.MaxSkills != 0 || spec.SessionTools ||
		spec.MemoryRef != nil || spec.OutputSchema != nil || spec.Runtime != nil ||
		spec.PromptAggregation != nil
}

func validateA2AExecution(a *agent.A2AExecution, kind agent.AgentKind, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if a.TimeoutSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("timeoutSeconds"), a.TimeoutSeconds, "must not be negative"))
	}
	if a.MaxConcurrentInvocations < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxConcurrentInvocations"),
			a.MaxConcurrentInvocations, "must not be negative"))
	}
	switch a.ProtocolVersion {
	case "", agent.A2AProtocolVersion03, agent.A2AProtocolVersion10:
	default:
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("protocolVersion"), a.ProtocolVersion,
			[]string{agent.A2AProtocolVersion03, agent.A2AProtocolVersion10}))
	}
	if a.Endpoint != "" {
		allErrs = append(allErrs, validateHTTPURL(a.Endpoint, fldPath.Child("endpoint"))...)
	}
	if a.ClientConfig != nil {
		allErrs = append(allErrs, validateClientConfig(a.ClientConfig, fldPath.Child("clientConfig"))...)
	}
	if a.CardURL != "" {
		allErrs = append(allErrs, validateHTTPURL(a.CardURL, fldPath.Child("cardUrl"))...)
	}

	if kind == agent.AgentKindExternal {
		switch a.Source {
		case agent.A2ASourceCardURL:
			if a.CardURL == "" && a.Endpoint == "" && a.ClientConfig == nil {
				allErrs = append(allErrs, field.Required(fldPath.Child("cardUrl"),
					"a card-url source requires cardUrl, endpoint or clientConfig"))
			}
		case agent.A2ASourceJSON, agent.A2ASourceManual:
			if a.Card == nil || len(a.Card.Raw) == 0 {
				allErrs = append(allErrs, field.Required(fldPath.Child("card"),
					"a json/manual source requires an AgentCard"))
			}
		case agent.A2ASourceRuntime:
			if a.RuntimeRef == nil || a.RuntimeRef.Name == "" {
				allErrs = append(allErrs, field.Required(fldPath.Child("runtimeRef"),
					"a runtime source requires a RuntimeProfile reference"))
			}
		default:
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("source"), a.Source,
				[]string{string(agent.A2ASourceCardURL), string(agent.A2ASourceJSON),
					string(agent.A2ASourceManual), string(agent.A2ASourceRuntime)}))
		}
	} else {
		if a.RuntimeRef == nil && a.Endpoint == "" && a.ClientConfig == nil {
			allErrs = append(allErrs, field.Required(fldPath.Child("runtimeRef"),
				"a2a execution requires runtimeRef, endpoint or clientConfig"))
		}
		if a.Source != "" || a.Card != nil {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("source"),
				"source/card describe an external card; a native agent's behavior is defined by the platform"))
		}
	}

	if a.Auth != nil {
		switch a.Auth.Type {
		case "", "none", "apiKey", "bearer", "headers", "oauth2", "runtime", "aksk":
		default:
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("auth", "type"), a.Auth.Type,
				[]string{"none", "apiKey", "bearer", "headers", "oauth2", "runtime", "aksk"}))
		}
		if a.Auth.Type == "oauth2" && a.Auth.TokenURL == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("auth", "tokenUrl"),
				"oauth2 requires a token URL"))
		}
	}
	return allErrs
}

func validateHTTPURL(raw string, fldPath *field.Path) field.ErrorList {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return field.ErrorList{field.Invalid(fldPath, raw, "must be an absolute http(s) URL")}
	}
	return nil
}
