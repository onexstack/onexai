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

// Package mirror projects the declarative control plane into PostgreSQL and
// keeps the projection honest.
//
// It exists because reads are O(N) per request in every runtime service: a List
// walks the whole informer cache, converts every row and sorts it before
// slicing a page. That is fine for a thousand objects and unusable at a hundred
// thousand. A SQL read model makes the same List O(page).
//
// The control plane stays the single source of truth. Nothing in this package
// ever writes back to the API server, and every row here is derived: dropping
// the schema costs a rebuild, never data.
package mirror

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// Kind is one projected resource type.
//
// New returns a zero-valued object with a fresh ObjectMeta each call — the
// controller-runtime builder and the typed client both need one per use, and
// sharing a single instance would race on its TypeMeta.
type Kind struct {
	// Group is the API group, e.g. "agent.onexai.io".
	Group string
	// Kind is the kind name, e.g. "Agent". It is both the partitioned table's
	// partition key value and the name a read backend is configured by.
	Kind string
	// New returns a typed object.
	New func() client.Object
	// NewList returns a typed list.
	NewList func() client.ObjectList
	// ClusterScoped marks a kind whose objects have no namespace. The reader
	// scope in Config must not be applied to such a kind: its rows are keyed by
	// the empty namespace, and filtering by the manager's namespaces would make
	// the first sweep treat every row as absent.
	ClusterScoped bool
	// Exclude reports an object the projection must not hold. The mirror is a
	// faithful projection by default; a kind sets this only when the read model
	// deliberately narrows the set (Namespace excludes the control plane's own
	// namespaces, matching the business-line table).
	Exclude func(client.Object) bool
}

// registry holds every projected kind. Adding a resource is one entry here —
// the projection itself is generic (see project.go), so no per-kind code is
// needed anywhere else.
var registry = []Kind{
	{Group: readmodel.GroupAgent, Kind: "Agent",
		New:     func() client.Object { return &agentv1alpha1.Agent{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.AgentList{} }},
	{Group: readmodel.GroupAgent, Kind: "AgentBinding",
		New:     func() client.Object { return &agentv1alpha1.AgentBinding{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.AgentBindingList{} }},
	// ClusterScoped is not optional here: without it the reader scope would be
	// applied to a kind whose rows carry an empty namespace, and the first sweep
	// would treat every row as absent and delete it.
	{Group: readmodel.GroupAgent, Kind: "ClusterAgentBinding", ClusterScoped: true,
		New:     func() client.Object { return &agentv1alpha1.ClusterAgentBinding{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.ClusterAgentBindingList{} }},
	{Group: readmodel.GroupAgent, Kind: "AuthorizationRule",
		New:     func() client.Object { return &agentv1alpha1.AuthorizationRule{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.AuthorizationRuleList{} }},
	{Group: readmodel.GroupAgent, Kind: "CustomCommand",
		New:     func() client.Object { return &agentv1alpha1.CustomCommand{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.CustomCommandList{} }},
	{Group: readmodel.GroupAgent, Kind: "KnowledgeBase",
		New:     func() client.Object { return &agentv1alpha1.KnowledgeBase{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.KnowledgeBaseList{} }},
	{Group: readmodel.GroupAgent, Kind: "Memory",
		New:     func() client.Object { return &agentv1alpha1.Memory{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.MemoryList{} }},
	{Group: readmodel.GroupAgent, Kind: "MCPServer",
		New:     func() client.Object { return &agentv1alpha1.MCPServer{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.MCPServerList{} }},
	{Group: readmodel.GroupAgent, Kind: "Model",
		New:     func() client.Object { return &agentv1alpha1.Model{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.ModelList{} }},
	{Group: readmodel.GroupAgent, Kind: "Prompt",
		New:     func() client.Object { return &agentv1alpha1.Prompt{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.PromptList{} }},
	{Group: readmodel.GroupAgent, Kind: "Skill",
		New:     func() client.Object { return &agentv1alpha1.Skill{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.SkillList{} }},
	{Group: readmodel.GroupAgent, Kind: "Webhook",
		New:     func() client.Object { return &agentv1alpha1.Webhook{} },
		NewList: func() client.ObjectList { return &agentv1alpha1.WebhookList{} }},

	{Group: readmodel.GroupChat, Kind: "BotApp",
		New:     func() client.Object { return &chatv1alpha1.BotApp{} },
		NewList: func() client.ObjectList { return &chatv1alpha1.BotAppList{} }},
	{Group: readmodel.GroupChat, Kind: "Command",
		New:     func() client.Object { return &chatv1alpha1.Command{} },
		NewList: func() client.ObjectList { return &chatv1alpha1.CommandList{} }},
	{Group: readmodel.GroupChat, Kind: "PeerBinding",
		New:     func() client.Object { return &chatv1alpha1.PeerBinding{} },
		NewList: func() client.ObjectList { return &chatv1alpha1.PeerBindingList{} }},
}

// Registry returns every projected kind.
func Registry() []Kind { return registry }

// KnownKinds returns the kind names, which is what a read backend is configured
// by.
func KnownKinds() []string { return readmodel.KindNames() }

// IsKnownKind reports whether name is a projected kind.
func IsKnownKind(name string) bool { return readmodel.IsKnownKind(name) }

// Select narrows the registry to the requested kinds. An empty list means every
// kind; an unknown name is an error rather than a silent no-op, because a
// misspelt kind in a configuration would otherwise leave the operator believing
// a resource is projected when it is not.
func Select(names []string) ([]Kind, error) {
	if len(names) == 0 {
		return registry, nil
	}
	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		if !IsKnownKind(n) {
			return nil, errUnknownKind(n)
		}
		wanted[n] = struct{}{}
	}
	selected := make([]Kind, 0, len(wanted))
	for _, k := range registry {
		if _, ok := wanted[k.Kind]; ok {
			selected = append(selected, k)
		}
	}
	return selected, nil
}

// AddToScheme registers every projected type so the manager's client can
// decode them.
func AddToScheme(scheme *runtime.Scheme) error {
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := agentv1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	return chatv1alpha1.AddToScheme(scheme)
}
