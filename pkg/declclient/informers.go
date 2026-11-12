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

package declclient

import (
	"k8s.io/client-go/tools/cache"

	"github.com/onexstack/onexai/pkg/readmodel"
)

// The kinds this client watches. They are constants rather than string
// literals so the informer table and the lister accessors cannot disagree on a
// spelling: a typo used to produce an accessor backed by an empty indexer and
// no compile error.
const (
	kindAgent               = "Agent"
	kindAgentTemplate       = "AgentTemplate"
	kindAgentBinding        = "AgentBinding"
	kindClusterAgentBinding = "ClusterAgentBinding"
	kindAuthorizationRule   = "AuthorizationRule"
	kindReferenceGrant      = "ReferenceGrant"
	kindModel               = "Model"
	kindPrompt              = "Prompt"
	kindSkill               = "Skill"
	kindCustomCommand       = "CustomCommand"
	kindMCPServer           = "MCPServer"
	kindKnowledgeBase       = "KnowledgeBase"
	kindMemory              = "Memory"
	kindWebhook             = "Webhook"
	kindScheduledRun        = "ScheduledRun"
	kindRuntimeProfile      = "RuntimeProfile"
	kindRuntimeHosting      = "RuntimeHosting"
	kindSandboxTemplate     = "SandboxTemplate"
	kindClusterEndpoint     = "Clusterendpoint"
	kindCommand             = "Command"
	kindPeerBinding         = "PeerBinding"
)

// groupByKind is the kind -> API group projection table, built once. groupOf
// used to scan readmodel.Kinds on every registration; the set is static.
var groupByKind = func() map[string]string {
	groups := make(map[string]string, len(readmodel.Kinds))
	for _, ref := range readmodel.Kinds {
		groups[ref.Kind] = ref.Group
	}
	return groups
}()

// groupOf returns the API group a projected kind belongs to.
func groupOf(kind string) string { return groupByKind[kind] }

// skippedInformer reports whether this process runs no informer for the kind.
func (c *Client) skippedInformer(group, kind string) bool {
	_, ok := c.skipped[group+"/"+kind]
	return ok
}

// buildInformers is the single kind -> informer table. Registration and the
// lister accessors both consume it: a kind missing here is a kind whose lister
// never syncs, so there is one list to update rather than two.
func (c *Client) buildInformers() map[string]func() cache.SharedIndexInformer {
	agent := c.Factory.Agent().V1alpha1()
	chat := c.Factory.Chat().V1alpha1()

	return map[string]func() cache.SharedIndexInformer{
		kindAgent:         func() cache.SharedIndexInformer { return agent.Agents().Informer() },
		kindAgentTemplate: func() cache.SharedIndexInformer { return agent.AgentTemplates().Informer() },
		kindAgentBinding:  func() cache.SharedIndexInformer { return agent.AgentBindings().Informer() },
		kindClusterAgentBinding: func() cache.SharedIndexInformer {
			return agent.ClusterAgentBindings().Informer()
		},
		kindAuthorizationRule: func() cache.SharedIndexInformer { return agent.AuthorizationRules().Informer() },
		kindReferenceGrant:    func() cache.SharedIndexInformer { return agent.ReferenceGrants().Informer() },
		kindModel:             func() cache.SharedIndexInformer { return agent.Models().Informer() },
		kindPrompt:            func() cache.SharedIndexInformer { return agent.Prompts().Informer() },
		kindSkill:             func() cache.SharedIndexInformer { return agent.Skills().Informer() },
		kindCustomCommand:     func() cache.SharedIndexInformer { return agent.CustomCommands().Informer() },
		kindMCPServer:         func() cache.SharedIndexInformer { return agent.MCPServers().Informer() },
		kindKnowledgeBase:     func() cache.SharedIndexInformer { return agent.KnowledgeBases().Informer() },
		kindMemory:            func() cache.SharedIndexInformer { return agent.Memories().Informer() },
		kindWebhook:           func() cache.SharedIndexInformer { return agent.Webhooks().Informer() },
		kindScheduledRun:      func() cache.SharedIndexInformer { return agent.ScheduledRuns().Informer() },
		kindRuntimeProfile:    func() cache.SharedIndexInformer { return agent.RuntimeProfiles().Informer() },
		kindRuntimeHosting:    func() cache.SharedIndexInformer { return agent.RuntimeHostings().Informer() },
		kindSandboxTemplate:   func() cache.SharedIndexInformer { return agent.SandboxTemplates().Informer() },
		kindClusterEndpoint:   func() cache.SharedIndexInformer { return agent.Clusterendpoints().Informer() },
		kindCommand:           func() cache.SharedIndexInformer { return chat.Commands().Informer() },
		kindPeerBinding:       func() cache.SharedIndexInformer { return chat.PeerBindings().Informer() },
	}
}

// registerInformers requests every informer this client exposes.
//
// client-go's factory only starts the informers that existed when Start ran, and
// an informer requested afterwards never syncs — it silently returns an empty
// cache forever. Registering them at construction is what keeps that footgun
// from turning into "the runtime sees no models".
func (c *Client) registerInformers() {
	c.informersMu.Lock()
	if c.informers == nil {
		c.informers = c.buildInformers()
	}
	c.informersMu.Unlock()

	for kind, informer := range c.informers {
		register(c, kind, informer)
	}

	// The full RBAC set, so a Kubernetes RBAC authorizer can answer from this
	// client's cache: a binding without its role resolves to no rules.
	rbac := c.KubeFactory.Rbac().V1()
	_ = rbac.ClusterRoleBindings().Informer()
	_ = rbac.RoleBindings().Informer()
	_ = rbac.ClusterRoles().Informer()
	_ = rbac.Roles().Informer()

	_ = c.KubeFactory.Core().V1().ConfigMaps().Informer()
	_ = c.KubeFactory.Core().V1().Secrets().Informer()
	// Service is the data source a Kubernetes WebhookClientConfig's `service:`
	// reference and an APIService both resolve through.
	_ = c.KubeFactory.Core().V1().Services().Informer()
	// Namespace is cluster-scoped and read through its lister by the console's
	// business-line list. Registering it here — and not from the store that
	// consumes it — is what keeps it ahead of Start: the store is built after
	// Start has already run, and an informer requested then never syncs.
	_ = c.KubeFactory.Core().V1().Namespaces().Informer()
}

// informerFor returns the kind's informer constructor, building the table on
// first use. New populates it eagerly; a Client assembled by hand — a test with
// a fake factory — gets the same accessors instead of an empty cache.
func (c *Client) informerFor(kind string) (func() cache.SharedIndexInformer, bool) {
	c.informersMu.Lock()
	defer c.informersMu.Unlock()
	if c.informers == nil {
		c.informers = c.buildInformers()
	}
	informer, ok := c.informers[kind]
	return informer, ok
}

// register starts one project informer unless the kind is excluded.
func register(c *Client, kind string, informer func() cache.SharedIndexInformer) {
	if c.skippedInformer(groupOf(kind), kind) {
		return
	}
	_ = informer()
}

// indexerFor returns the informer's indexer, or a detached empty one when the
// kind's informer is excluded or unknown. Returning the empty indexer rather
// than an error keeps every lister accessor total, and a filtered kind never
// reads from it: its List comes from the projection and its Get from the API
// server.
func (c *Client) indexerFor(kind string) cache.Indexer {
	informer, ok := c.informerFor(kind)
	if !ok || c.skippedInformer(groupOf(kind), kind) {
		return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	}
	return informer().GetIndexer()
}
