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

	corelisters "k8s.io/client-go/listers/core/v1"
	rbacinformers "k8s.io/client-go/listers/rbac/v1"

	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"
	chatlisters "github.com/onexstack/onexai/pkg/generated/listers/chat/v1alpha1"
)

// listerFor builds one project lister over its kind's indexer.
//
// The generated listers share no method set, so the constructor is passed in
// rather than invented here; the typed kind constant is what keeps an accessor
// and the informer table from drifting apart.
func listerFor[T any](c *Client, kind string, ctor func(cache.Indexer) T) T {
	return ctor(c.indexerFor(kind))
}

// AuthorizationRules returns the rule lister. Callers narrow to a namespace
// through AuthorizationRules(ns) on the returned lister when the client is
// namespace-scoped; an empty namespace means "all namespaces".
func (c *Client) AuthorizationRules() agentlisters.AuthorizationRuleLister {
	return listerFor(c, kindAuthorizationRule, agentlisters.NewAuthorizationRuleLister)
}

// AgentTemplates returns the agent template lister.
func (c *Client) AgentTemplates() agentlisters.AgentTemplateLister {
	return listerFor(c, kindAgentTemplate, agentlisters.NewAgentTemplateLister)
}

// RuntimeProfiles returns the runtime profile lister.
func (c *Client) RuntimeProfiles() agentlisters.RuntimeProfileLister {
	return listerFor(c, kindRuntimeProfile, agentlisters.NewRuntimeProfileLister)
}

// ScheduledRuns returns the scheduled run lister.
func (c *Client) ScheduledRuns() agentlisters.ScheduledRunLister {
	return listerFor(c, kindScheduledRun, agentlisters.NewScheduledRunLister)
}

// SandboxTemplates returns the sandbox template lister.
func (c *Client) SandboxTemplates() agentlisters.SandboxTemplateLister {
	return listerFor(c, kindSandboxTemplate, agentlisters.NewSandboxTemplateLister)
}

// RuntimeHostings returns the runtime hosting lister.
func (c *Client) RuntimeHostings() agentlisters.RuntimeHostingLister {
	return listerFor(c, kindRuntimeHosting, agentlisters.NewRuntimeHostingLister)
}

// ClusterEndpoints returns the cluster endpoint lister.
func (c *Client) ClusterEndpoints() agentlisters.ClusterendpointLister {
	return listerFor(c, kindClusterEndpoint, agentlisters.NewClusterendpointLister)
}

// Memories returns the long-term memory lister.
func (c *Client) Memories() agentlisters.MemoryLister {
	return listerFor(c, kindMemory, agentlisters.NewMemoryLister)
}

// Models returns the model lister.
func (c *Client) Models() agentlisters.ModelLister {
	return listerFor(c, kindModel, agentlisters.NewModelLister)
}

// CustomCommands returns the custom command lister.
func (c *Client) CustomCommands() agentlisters.CustomCommandLister {
	return listerFor(c, kindCustomCommand, agentlisters.NewCustomCommandLister)
}

// Skills returns the skill lister.
func (c *Client) Skills() agentlisters.SkillLister {
	return listerFor(c, kindSkill, agentlisters.NewSkillLister)
}

// MCPServers returns the MCP server lister.
func (c *Client) MCPServers() agentlisters.MCPServerLister {
	return listerFor(c, kindMCPServer, agentlisters.NewMCPServerLister)
}

// KnowledgeBases returns the knowledge base lister.
func (c *Client) KnowledgeBases() agentlisters.KnowledgeBaseLister {
	return listerFor(c, kindKnowledgeBase, agentlisters.NewKnowledgeBaseLister)
}

// Prompts returns the prompt lister.
func (c *Client) Prompts() agentlisters.PromptLister {
	return listerFor(c, kindPrompt, agentlisters.NewPromptLister)
}

// Webhooks returns the webhook lister.
func (c *Client) Webhooks() agentlisters.WebhookLister {
	return listerFor(c, kindWebhook, agentlisters.NewWebhookLister)
}

// Commands returns the command-catalogue lister.
func (c *Client) Commands() chatlisters.CommandLister {
	return listerFor(c, kindCommand, chatlisters.NewCommandLister)
}

// PeerBindings returns the peer-binding lister.
func (c *Client) PeerBindings() chatlisters.PeerBindingLister {
	return listerFor(c, kindPeerBinding, chatlisters.NewPeerBindingLister)
}

// ClusterRoleBindings returns the cluster role binding lister.
func (c *Client) ClusterRoleBindings() rbacinformers.ClusterRoleBindingLister {
	return rbacinformers.NewClusterRoleBindingLister(
		c.KubeFactory.Rbac().V1().ClusterRoleBindings().Informer().GetIndexer(),
	)
}

// RoleBindings returns the role binding lister for one namespace.
func (c *Client) RoleBindings(namespace string) rbacinformers.RoleBindingNamespaceLister {
	return rbacinformers.NewRoleBindingLister(
		c.KubeFactory.Rbac().V1().RoleBindings().Informer().GetIndexer(),
	).RoleBindings(namespace)
}

// ConfigMaps returns the ConfigMap lister for one namespace. An empty namespace
// lists every ConfigMap the cache holds.
func (c *Client) ConfigMaps(namespace string) corelisters.ConfigMapNamespaceLister {
	return corelisters.NewConfigMapLister(
		c.KubeFactory.Core().V1().ConfigMaps().Informer().GetIndexer(),
	).ConfigMaps(namespace)
}

// Secrets returns the Secret lister. It is not namespace-scoped: a model or MCP
// server may live in a namespace other than the store's own and still
// name a credential in its own.
func (c *Client) Secrets() corelisters.SecretLister {
	return corelisters.NewSecretLister(c.KubeFactory.Core().V1().Secrets().Informer().GetIndexer())
}

// ClusterAgentBindings returns the cluster-wide agent-binding lister.
func (c *Client) ClusterAgentBindings() agentlisters.ClusterAgentBindingLister {
	return listerFor(c, kindClusterAgentBinding, agentlisters.NewClusterAgentBindingLister)
}

// AgentBindings returns the agent-binding lister.
func (c *Client) AgentBindings() agentlisters.AgentBindingLister {
	return listerFor(c, kindAgentBinding, agentlisters.NewAgentBindingLister)
}

// Agents returns the agent lister.
func (c *Client) Agents() agentlisters.AgentLister {
	return listerFor(c, kindAgent, agentlisters.NewAgentLister)
}

// ReferenceGrants returns the reference-grant lister.
func (c *Client) ReferenceGrants() agentlisters.ReferenceGrantLister {
	return listerFor(c, kindReferenceGrant, agentlisters.NewReferenceGrantLister)
}
