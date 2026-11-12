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

package agentbinding

import (
	"context"
	"fmt"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/record"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// Admission enforces who may create a binding.
//
// A binding is a privilege-escalation primitive: one object can hand a
// capability to every agent a selector matches. Creating one therefore requires
// read access to every object it names and list access to the agents it could
// match — the same idea as RBAC's bind/escalate, expressed with the server's own
// authorizer.
//
// Selector access is checked as a `list` on agents in the binding's namespace.
// Agents that start matching the selector later are not re-checked: this is a
// deliberate TOCTOU trade-off. Without it, a binding would have to be
// re-authorized every time any agent's labels change, which is neither cheap nor
// what an operator expects.
type Admission struct {
	Authorizer authorizer.Authorizer
	Recorder   record.EventRecorder
}

// Admit returns an error when the caller may not create or update obj.
func (a Admission) Admit(ctx context.Context, obj *agent.AgentBinding) error {
	if a.Authorizer == nil {
		return nil
	}

	reqUser, _ := request.UserFrom(ctx)
	if reqUser == nil {
		reqUser = &user.DefaultInfo{Name: user.Anonymous}
	}

	missing := make([]string, 0, 8)
	for _, attributes := range requirements(obj, reqUser) {
		decision, reason, err := a.Authorizer.Authorize(ctx, attributes)
		if err != nil {
			a.record(obj, apiv1.EventTypeWarning, "BindingAuthorizationError",
				fmt.Sprintf("authorizer failed for %s: %v", describe(attributes), err))
			return apierrors.NewForbidden(agent.Resource("agentbindings"), obj.Name,
				fmt.Errorf("authorization could not be evaluated: %w", err))
		}
		if decision == authorizer.DecisionAllow {
			continue
		}
		message := describe(attributes)
		if reason != "" {
			message += ": " + reason
		}
		missing = append(missing, message)
	}

	if len(missing) > 0 {
		a.record(obj, apiv1.EventTypeWarning, "BindingDenied",
			fmt.Sprintf("user %q may not bind: %s", reqUser.GetName(), strings.Join(missing, "; ")))
		return apierrors.NewForbidden(agent.Resource("agentbindings"), obj.Name,
			fmt.Errorf("user %q must have read access to every bound object: %s",
				reqUser.GetName(), strings.Join(missing, "; ")))
	}

	a.record(obj, apiv1.EventTypeNormal, "BindingAuthorized",
		fmt.Sprintf("user %q authorized to bind %d object(s) to agents matching the selector",
			reqUser.GetName(), countRefs(obj)))
	return nil
}

func (a Admission) record(obj *agent.AgentBinding, eventType, reason, message string) {
	if a.Recorder == nil {
		return
	}
	a.Recorder.Event(obj, eventType, reason, message)
}

// requirements lists every read the caller must hold.
func requirements(obj *agent.AgentBinding, reqUser user.Info) []authorizer.Attributes {
	ns := obj.Namespace
	out := make([]authorizer.Attributes, 0, 8+len(obj.Spec.Agents))

	for _, ref := range allReferences(obj) {
		out = append(out, attr(reqUser, "get", ref.resource, ns, ref.name))
	}
	for _, name := range obj.Spec.Agents {
		out = append(out, attr(reqUser, "get", "agents", ns, name))
	}
	// The selector is the batch grant. Requiring list on agents is the cheapest
	// check that still means "you may address this population".
	out = append(out, attr(reqUser, "list", "agents", ns, ""))
	// Commands are catalogue objects in this same group. A binding may name
	// them — or select a whole group of them by label — only if the caller can
	// read the catalogue.
	if len(obj.Spec.Commands) > 0 || obj.Spec.CommandSelector != nil {
		out = append(out, attr(reqUser, "get", "commands", ns, ""))
	}

	return out
}

type reference struct {
	resource string
	name     string
}

func allReferences(obj *agent.AgentBinding) []reference {
	collect := func(resource string, refs []agent.Reference) []reference {
		out := make([]reference, 0, len(refs))
		for _, ref := range refs {
			out = append(out, reference{resource: resource, name: ref.Name})
		}
		return out
	}

	out := collect("prompts", obj.Spec.Prompts)
	out = append(out, collect("skills", obj.Spec.Skills)...)
	out = append(out, collect("knowledgebases", obj.Spec.KnowledgeBases)...)
	out = append(out, collect("mcpservers", obj.Spec.MCPServers)...)
	out = append(out, collect("customcommands", obj.Spec.CustomCommands)...)
	return out
}

func countRefs(obj *agent.AgentBinding) int {
	return len(allReferences(obj)) + len(obj.Spec.Agents)
}

func attr(reqUser user.Info, verb, resource, namespace, name string) authorizer.Attributes {
	return authorizer.AttributesRecord{
		User:            reqUser,
		Verb:            verb,
		Namespace:       namespace,
		APIGroup:        agentv1alpha1.GroupName,
		APIVersion:      agentv1alpha1.SchemeGroupVersion.Version,
		Resource:        resource,
		Name:            name,
		ResourceRequest: true,
	}
}

func describe(attributes authorizer.Attributes) string {
	target := attributes.GetResource()
	if attributes.GetName() != "" {
		target += "/" + attributes.GetName()
	}
	return fmt.Sprintf("%s %s in namespace %q", attributes.GetVerb(), target, attributes.GetNamespace())
}
