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

// Package agentreference refuses to delete a resource that an enabled Agent
// still names in its spec.
//
// Strategies validate a single object against itself; they cannot see that
// something else points at it. Without this plugin, deleting a Prompt that an
// Agent's spec.promptRef names succeeds, and the Agent quietly degrades to
// ReferenceNotFound on its next reconcile. The API already models that
// reference as an error condition, so the delete should fail at the point of
// the mistake instead.
//
// Label selectors are deliberately not checked: they are dynamic by design, and
// refusing every delete a selector might match would make cleanup impossible.
// This plugin only protects the references a user wrote by hand.
package agentreference

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/client-go/kubernetes"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	versioned "github.com/onexstack/onexai/pkg/generated/clientset/versioned"
)

// PluginName is the name the plugin is registered and enabled under.
const PluginName = "AgentReferenceProtection"

var (
	// errMissingOnexClient is returned when the initializer did not run.
	errMissingOnexClient = errors.New(PluginName + ": missing onex clientset")
	// errMissingKubeClient is returned when the initializer did not run.
	errMissingKubeClient = errors.New(PluginName + ": missing kubernetes clientset")
	// errStillReferenced prefixes every Forbidden message this plugin returns,
	// so the reason is greppable and the wording is defined in one place.
	errStillReferenced = errors.New("still named in the spec of an enabled agent")
)

// Register registers the plugin with the admission plugin registry.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(io.Reader) (admission.Interface, error) {
		return New(), nil
	})
}

// New builds the plugin. It handles deletes only, so the handler short-circuits
// every other operation before the plugin body runs.
func New() *Plugin {
	// The clients are injected by the initializer, not by this factory.
	return &Plugin{Handler: admission.NewHandler(admission.Delete)} //nolint:exhaustruct_v5 // clients arrive via the initializer
}

// Plugin refuses deletes that would break an enabled Agent.
type Plugin struct {
	*admission.Handler

	onex versioned.Interface
	kube kubernetes.Interface
}

var (
	_ admission.ValidationInterface     = (*Plugin)(nil)
	_ admission.InitializationValidator = (*Plugin)(nil)
)

// SetOnexClientSet implements the initializer's Wants interface.
func (p *Plugin) SetOnexClientSet(client versioned.Interface) { p.onex = client }

// SetKubeClientSet implements the initializer's Wants interface.
func (p *Plugin) SetKubeClientSet(client kubernetes.Interface) { p.kube = client }

// ValidateInitialization implements admission.InitializationValidator.
func (p *Plugin) ValidateInitialization() error {
	if p.onex == nil {
		return errMissingOnexClient
	}

	if p.kube == nil {
		return errMissingKubeClient
	}

	return nil
}

// Validate implements admission.ValidationInterface.
func (p *Plugin) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetOperation() != admission.Delete || a.GetSubresource() != "" {
		return nil
	}

	resource := a.GetResource().Resource
	if a.GetResource().Group != agentv1alpha1.GroupName || !protectedResources[resource] {
		return nil
	}

	namespace, name, ok := deletedObject(a)
	if !ok || p.namespaceTerminating(ctx, namespace) {
		return nil
	}

	blocking := p.blockingAgents(ctx, resource, namespace, name)
	if len(blocking) == 0 {
		return nil
	}

	slices.Sort(blocking)

	return admission.NewForbidden(a, fmt.Errorf(
		"%w: %s %q is referenced by %v; update those agents first",
		errStillReferenced, resource, name, blocking,
	))
}

// deletedObject returns the namespace and name of the object being removed.
//
// On a delete the removed object is what admission carries; the old object is
// the update-time field. Both are checked because the REST layer populates them
// at different points.
func deletedObject(a admission.Attributes) (string, string, bool) {
	obj := a.GetObject()
	if obj == nil {
		obj = a.GetOldObject()
	}

	accessor, err := meta.Accessor(obj)
	if err != nil {
		// An unreadable object cannot be matched against anything.
		return "", "", false //nolint:nilerr // deliberate fail-open
	}

	if accessor.GetNamespace() == "" {
		return "", "", false
	}

	return accessor.GetNamespace(), accessor.GetName(), true
}

// namespaceTerminating reports whether the namespace is being torn down, in
// which case every object in it must be deletable or the namespace stays stuck
// in Terminating. A failed lookup is treated as "not terminating": an
// unavailable namespace must not silently disable the protection.
func (p *Plugin) namespaceTerminating(ctx context.Context, namespace string) bool {
	ns, err := p.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}) //nolint:exhaustruct,exhaustruct_v5 // every field is optional
	return err == nil && ns.DeletionTimestamp != nil
}

// blockingAgents names the enabled agents that reference the object. A read
// failure yields no blockers: the plugin is a safety net, not a source of
// truth, and must not wedge deletes when the API is degraded.
//
// The list spans every namespace because a Reference may name another
// namespace (spec.skills allows it), so an agent outside the target namespace
// can hold the reference. Deletes are infrequent and an install holds tens of
// agents, so one cluster-wide list per delete is cheaper than a cache that
// could serve a stale answer and block a legitimate delete.
func (p *Plugin) blockingAgents(ctx context.Context, resource, namespace, name string) []string {
	agents, err := p.onex.AgentV1alpha1().Agents(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}) //nolint:exhaustruct,exhaustruct_v5,kubelistcheck // fields are optional; a stale cache must not decide whether a delete is allowed
	if err != nil {
		return nil //nolint:nilerr // deliberate fail-open
	}

	var blocking []string

	for i := range agents.Items {
		agent := &agents.Items[i]
		if !agent.Spec.Enabled {
			continue
		}

		if references(agent, resource, namespace, name) {
			blocking = append(blocking, agent.Namespace+"/"+agent.Name)
		}
	}

	// A template can reference the same resources an agent can (and an agent
	// can reference a template), so the protection has to look at templates
	// too. A failure is skipped for the same fail-open reason as the agent
	// list above.
	templates, terr := p.onex.AgentV1alpha1().AgentTemplates(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}) //nolint:exhaustruct,exhaustruct_v5,kubelistcheck // fields are optional; a stale cache must not decide whether a delete is allowed
	if terr == nil {
		for i := range templates.Items {
			tmpl := &templates.Items[i]
			if !tmpl.Spec.Enabled {
				continue
			}
			if templateReferences(tmpl, resource, namespace, name) {
				blocking = append(blocking, "AgentTemplate/"+tmpl.Namespace+"/"+tmpl.Name)
			}
		}
	}

	// Bindings carry the same kinds of reference as an agent. Without this scan a
	// platform preset could keep pointing at a deleted Skill and degrade silently.
	bindings, berr := p.onex.AgentV1alpha1().AgentBindings(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}) //nolint:exhaustruct,exhaustruct_v5,kubelistcheck // fields are optional; a stale cache must not decide whether a delete is allowed
	if berr == nil {
		for i := range bindings.Items {
			binding := &bindings.Items[i]
			if binding.Spec.Enabled != nil && !*binding.Spec.Enabled {
				continue
			}
			if bindingReferences(binding.Namespace,
				binding.Spec.Prompts, binding.Spec.Skills, binding.Spec.KnowledgeBases,
				binding.Spec.MCPServers, binding.Spec.CustomCommands, nil,
				resource, namespace, name) {
				blocking = append(blocking, "AgentBinding/"+binding.Namespace+"/"+binding.Name)
			}
		}
	}

	clusterBindings, cerr := p.onex.AgentV1alpha1().ClusterAgentBindings().List(ctx, metav1.ListOptions{}) //nolint:exhaustruct,exhaustruct_v5,kubelistcheck // fields are optional; a stale cache must not decide whether a delete is allowed
	if cerr == nil {
		for i := range clusterBindings.Items {
			binding := &clusterBindings.Items[i]
			if binding.Spec.Enabled != nil && !*binding.Spec.Enabled {
				continue
			}
			// A cluster binding has no namespace of its own, but every reference
			// carries one, so the default is never consulted.
			if bindingReferences("",
				binding.Spec.Prompts, binding.Spec.Skills, binding.Spec.KnowledgeBases,
				binding.Spec.MCPServers, binding.Spec.CustomCommands, binding.Spec.MemoryRef,
				resource, namespace, name) {
				blocking = append(blocking, "ClusterAgentBinding/"+binding.Name)
			}
		}
	}

	return blocking
}

// bindingReferences is the binding-side analogue of references: a binding names
// the same kinds of object, with a namespace on each reference (or, for a
// namespaced binding, a default of its own namespace).
func bindingReferences(
	defaultNamespace string,
	prompts, skills, knowledgeBases, mcps, customCommands []agentv1alpha1.Reference,
	memory *agentv1alpha1.Reference,
	resource, namespace, name string,
) bool {
	sameTarget := func(ref agentv1alpha1.Reference) bool {
		refNamespace := ref.Namespace
		if refNamespace == "" {
			refNamespace = defaultNamespace
		}
		return refNamespace == namespace && ref.Name == name
	}
	matchesAny := func(refs []agentv1alpha1.Reference) bool {
		return slices.ContainsFunc(refs, sameTarget)
	}

	switch resource {
	case resourcePrompt:
		return matchesAny(prompts)
	case resourceSkill:
		return matchesAny(skills)
	case resourceKnowledgeBase:
		return matchesAny(knowledgeBases)
	case resourceMCPServer:
		return matchesAny(mcps)
	case resourceCustomCommand:
		return matchesAny(customCommands)
	case resourceMemory:
		return memory != nil && sameTarget(*memory)
	}
	return false
}

// protectedResources lists the resources worth protecting. Anything absent here
// is never blocked.
var protectedResources = map[string]bool{
	resourcePrompt:        true,
	resourceModel:         true,
	resourceSkill:         true,
	resourceMCPServer:     true,
	resourceKnowledgeBase: true,
	resourceMemory:        true,
	resourceCustomCommand: true,
	resourceWebhook:       true,
	resourceAgent:         true,
	resourceAgentTemplate: true,
}

const (
	resourcePrompt        = "prompts"
	resourceAgentTemplate = "agenttemplates"
	resourceModel         = "models"
	resourceSkill         = "skills"
	resourceMCPServer     = "mcpservers"
	resourceKnowledgeBase = "knowledgebases"
	resourceMemory        = "memories"
	resourceCustomCommand = "customcommands"
	resourceWebhook       = "webhooks"
	resourceAgent         = "agents"
)

// references reports whether agent names the object identified by resource,
// namespace and name anywhere in its spec.
//
// Only spec is consulted: status is derived and rewritten by the controller, so
// a stale status must not decide whether a delete is allowed.
//
// The switch is a flat dispatch over the API's reference fields; splitting it
// would hide which fields are covered.
//
//nolint:gocyclo,cyclop // one case per reference field is the clearest form
func references(agent *agentv1alpha1.Agent, resource string, namespace, name string) bool {
	sameTarget := func(ref agentv1alpha1.Reference) bool {
		refNamespace := ref.Namespace
		if refNamespace == "" {
			refNamespace = agent.Namespace
		}

		return refNamespace == namespace && ref.Name == name
	}
	matchesAny := func(refs []agentv1alpha1.Reference) bool {
		return slices.ContainsFunc(refs, sameTarget)
	}

	switch resource {
	case resourcePrompt:
		return (agent.Spec.PromptRef != nil && sameTarget(*agent.Spec.PromptRef)) || matchesAny(agent.Spec.Prompts)
	case resourceModel:
		return agent.Spec.ModelRef != nil && sameTarget(*agent.Spec.ModelRef)
	case resourceSkill:
		return matchesAny(agent.Spec.Skills)
	case resourceMCPServer:
		return matchesAny(agent.Spec.MCPs)
	case resourceKnowledgeBase:
		return matchesAny(agent.Spec.KnowledgeBases)
	case resourceMemory:
		return agent.Spec.MemoryRef != nil && sameTarget(*agent.Spec.MemoryRef)
	case resourceCustomCommand:
		return matchesAny(agent.Spec.CustomCommands)
	case resourceAgent:
		return matchesAny(agent.Spec.Agents)
	case resourceAgentTemplate:
		return matchesAny(agent.Spec.TemplateRefs)
	case resourceWebhook:
		// spec.webhooks holds bare names, always in the agent's own namespace.
		return agent.Namespace == namespace && slices.Contains(agent.Spec.Webhooks, name)
	}

	return false
}

// templateReferences is references for an AgentTemplate. It covers the same
// reference fields minus the agent-only ones: a template cannot aggregate
// another agent or another template, so there is no case for either.
func templateReferences(tmpl *agentv1alpha1.AgentTemplate, resource, namespace, name string) bool {
	sameTarget := func(ref agentv1alpha1.Reference) bool {
		refNamespace := ref.Namespace
		if refNamespace == "" {
			refNamespace = tmpl.Namespace
		}
		return refNamespace == namespace && ref.Name == name
	}
	matchesAny := func(refs []agentv1alpha1.Reference) bool {
		return slices.ContainsFunc(refs, sameTarget)
	}

	switch resource {
	case resourcePrompt:
		return (tmpl.Spec.PromptRef != nil && sameTarget(*tmpl.Spec.PromptRef)) || matchesAny(tmpl.Spec.Prompts)
	case resourceModel:
		return tmpl.Spec.ModelRef != nil && sameTarget(*tmpl.Spec.ModelRef)
	case resourceSkill:
		return matchesAny(tmpl.Spec.Skills)
	case resourceMCPServer:
		return matchesAny(tmpl.Spec.MCPs)
	case resourceKnowledgeBase:
		return matchesAny(tmpl.Spec.KnowledgeBases)
	case resourceMemory:
		return tmpl.Spec.MemoryRef != nil && sameTarget(*tmpl.Spec.MemoryRef)
	case resourceCustomCommand:
		return matchesAny(tmpl.Spec.CustomCommands)
	case resourceAgent:
		return matchesAny(tmpl.Spec.Delegates)
	case resourceWebhook:
		return tmpl.Namespace == namespace && slices.Contains(tmpl.Spec.Webhooks, name)
	}

	return false
}
