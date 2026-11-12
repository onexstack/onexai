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

package clusteragentbinding

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

// Admission enforces who may create a cluster-wide binding.
//
// It is deliberately the same check the namespaced binding makes, applied to
// every namespace the object reaches: read access to each referenced object and
// list access to the agents it could match. The admin-only gate lives on the
// REST face; this is the second lock, for callers that arrive through the
// service-to-service path.
type Admission struct {
	Authorizer authorizer.Authorizer
	Recorder   record.EventRecorder
}

// Admit returns an error when the caller may not create or update obj.
func (a Admission) Admit(ctx context.Context, obj *agent.ClusterAgentBinding) error {
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
			return apierrors.NewForbidden(agent.Resource("clusteragentbindings"), obj.Name,
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
		return apierrors.NewForbidden(agent.Resource("clusteragentbindings"), obj.Name,
			fmt.Errorf("user %q must have read access to every bound object and every target namespace: %s",
				reqUser.GetName(), strings.Join(missing, "; ")))
	}

	a.record(obj, apiv1.EventTypeNormal, "BindingAuthorized",
		fmt.Sprintf("user %q authorized to bind %d object(s) across %d namespace(s)",
			reqUser.GetName(), countRefs(obj), len(obj.Spec.Namespaces)))
	return nil
}

func (a Admission) record(obj *agent.ClusterAgentBinding, eventType, reason, message string) {
	if a.Recorder == nil {
		return
	}
	a.Recorder.Event(obj, eventType, reason, message)
}

// requirements lists every read the caller must hold.
func requirements(obj *agent.ClusterAgentBinding, reqUser user.Info) []authorizer.Attributes {
	out := make([]authorizer.Attributes, 0, 8+len(obj.Spec.Agents)+len(obj.Spec.Namespaces))

	for _, ref := range allReferences(obj) {
		out = append(out, attr(reqUser, "get", ref.resource, ref.namespace, ref.name))
	}
	for _, ref := range obj.Spec.Agents {
		out = append(out, attr(reqUser, "get", "agents", ref.Namespace, ref.Name))
	}
	// The audience is the batch grant: list on agents in each target namespace.
	for _, namespace := range obj.Spec.Namespaces {
		if namespace == agent.AllNamespaces {
			// A wildcard audience is a cluster-wide read. Asking the authorizer
			// about a namespace literally named "*" would ask a question no
			// policy answers, so the grant is checked at cluster scope instead —
			// and creating a cluster-wide grant therefore needs cluster-wide
			// rights.
			out = append(out, attr(reqUser, "list", "agents", "", ""))
			if len(obj.Spec.Commands) > 0 || obj.Spec.CommandSelector != nil {
				out = append(out, attr(reqUser, "get", "commands", "", ""))
			}
			continue
		}
		out = append(out, attr(reqUser, "list", "agents", namespace, ""))
		if len(obj.Spec.Commands) > 0 || obj.Spec.CommandSelector != nil {
			out = append(out, attr(reqUser, "get", "commands", namespace, ""))
		}
	}

	return out
}

type reference struct {
	resource  string
	namespace string
	name      string
}

func allReferences(obj *agent.ClusterAgentBinding) []reference {
	collect := func(resource string, refs []agent.Reference) []reference {
		out := make([]reference, 0, len(refs))
		for _, ref := range refs {
			out = append(out, reference{resource: resource, namespace: ref.Namespace, name: ref.Name})
		}
		return out
	}

	out := collect("prompts", obj.Spec.Prompts)
	out = append(out, collect("skills", obj.Spec.Skills)...)
	out = append(out, collect("knowledgebases", obj.Spec.KnowledgeBases)...)
	out = append(out, collect("mcpservers", obj.Spec.MCPServers)...)
	out = append(out, collect("customcommands", obj.Spec.CustomCommands)...)
	if obj.Spec.MemoryRef != nil {
		out = append(out, reference{
			resource:  "memories",
			namespace: obj.Spec.MemoryRef.Namespace,
			name:      obj.Spec.MemoryRef.Name,
		})
	}
	return out
}

func countRefs(obj *agent.ClusterAgentBinding) int {
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
