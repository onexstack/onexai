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

package controlplanestore

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/batchstore"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// AIStore is the agent resource store with the declarative resources served from
// the control plane and everything else delegated.
//
// A resource is served by exactly one side: the ones here come from the API
// server (reads and writes), the rest stay on SQL. That split is what keeps a
// write and a read of the same resource from disagreeing — the failure a
// half-migrated store would otherwise produce, and the one the authorization
// rules actually produced while their read path had moved and their write path
// had not.
type AIStore struct {
	fallback store.RemainingStore
	// client backs the read-only catalogue view the binding preview needs. It
	// is kept here rather than exposed through AIStore because chat owns that
	// API; see store.CommandCatalog.
	client *declclient.Client

	namespaces           *namespaceStore
	secrets              *secretStore
	agents               *agentStore
	agentTemplates       *agentTemplateStore
	agentBindings        *agentBindingStore
	clusterAgentBindings *clusterAgentBindingStore
	referenceGrants      *referenceGrantStore
	prompts              *promptStore
	skills               *skillStore
	customCommands       *customCommandStore
	knowledgeBases       *knowledgeBaseStore
	memories             *memoryStore
	webhooks             *webhookStore
	models               *modelStore
	mcps                 *mcpStore
	clusterEndpoints     *clusterEndpointStore
	authzRules           *AuthzRuleStore

	runtimeProfiles  *runtimeProfileStore
	sandboxTemplates *sandboxTemplateStore
	runtimeHostings  *runtimeHostingStore
	scheduledRuns    store.ScheduledRunStore
}

// NewAIStore builds the composite over client. namespace scopes the reads and
// names the namespace writes land in; fallback serves every resource that has
// not been migrated; mirror, when set, redirects the List of the kinds it
// selects to the PostgreSQL projection.
func NewAIStore(
	client *declclient.Client, namespace string, fallback store.RemainingStore, mirror *MirrorConfig,
) (*AIStore, error) {
	s := &AIStore{
		fallback:             fallback,
		client:               client,
		namespaces:           NewNamespaceStore(client),
		secrets:              NewSecretStore(client, namespace),
		agents:               NewAgentStore(client, namespace),
		agentTemplates:       NewAgentTemplateStore(client, namespace),
		agentBindings:        NewAgentBindingStore(client, namespace),
		clusterAgentBindings: NewClusterAgentBindingStore(client),
		referenceGrants:      NewReferenceGrantStore(client, namespace),
		prompts:              NewPromptStore(client, namespace),
		skills:               NewSkillStore(client, namespace),
		customCommands:       NewCustomCommandStore(client, namespace),
		knowledgeBases:       NewKnowledgeBaseStore(client, namespace),
		memories:             NewMemoryStore(client, namespace),
		webhooks:             NewWebhookStore(client, namespace),
		models:               NewModelStore(client, namespace),
		mcps:                 NewMCPStore(client, namespace),
		clusterEndpoints:     NewClusterEndpointStore(client, namespace),
		authzRules:           NewAuthzRuleStore(client, namespace),
		runtimeProfiles:      NewRuntimeProfileStore(client, namespace),
		sandboxTemplates:     NewSandboxTemplateStore(client, namespace),
		runtimeHostings:      NewRuntimeHostingStore(client, namespace),
		scheduledRuns:        batchstore.NewScheduledRunStore(client, namespace),
	}
	if err := mirror.attach(s, client); err != nil {
		return nil, err
	}
	return s, nil
}

// Namespaces returns the business-line accessor.
// ListCommands implements store.CommandCatalog.
//
// The catalogue is published into every namespace that holds an agent or a
// binding, so the same command appears once per namespace. The preview wants one
// row per command, so the names are deduplicated here rather than at the call
// site.
func (s *AIStore) ListCommands(_ context.Context) ([]*chatv1alpha1.Command, error) {
	if s.client == nil {
		return nil, nil
	}
	items, err := s.client.Commands().List(labels.Everything())
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]*chatv1alpha1.Command, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.Name]; ok {
			continue
		}
		seen[item.Name] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func (s *AIStore) Namespaces() store.NamespaceStore { return s.namespaces }

// Agents returns the control-plane-backed agent accessor.
func (s *AIStore) Agents() store.AgentStore { return s.agents }

// AgentTemplates returns the control-plane-backed agent-template accessor.
func (s *AIStore) AgentTemplates() store.AgentTemplateStore { return s.agentTemplates }

// AgentBindings returns the control-plane-backed agent-binding accessor.
func (s *AIStore) AgentBindings() store.AgentBindingStore { return s.agentBindings }

// ClusterAgentBindings returns the control-plane-backed cluster-binding accessor.
func (s *AIStore) ClusterAgentBindings() store.ClusterAgentBindingStore {
	return s.clusterAgentBindings
}

// ReferenceGrants returns the control-plane-backed reference-grant accessor.
func (s *AIStore) ReferenceGrants() store.ReferenceGrantStore { return s.referenceGrants }

// Prompts returns the control-plane-backed prompt accessor.
func (s *AIStore) Prompts() store.PromptStore { return s.prompts }

// Skills returns the control-plane-backed skill accessor.
func (s *AIStore) Skills() store.SkillStore { return s.skills }

// CustomCommands returns the control-plane-backed custom command accessor.
func (s *AIStore) CustomCommands() store.CustomCommandStore { return s.customCommands }

// KnowledgeBases returns the control-plane-backed knowledge base accessor.
func (s *AIStore) KnowledgeBases() store.KnowledgeBaseStore { return s.knowledgeBases }

// Memories returns the control-plane-backed long-term memory accessor.
func (s *AIStore) Memories() store.MemoryStore { return s.memories }

// Webhooks returns the control-plane-backed webhook accessor.
func (s *AIStore) Webhooks() store.WebhookStore { return s.webhooks }

// ClusterEndpoints returns the control-plane-backed cluster endpoint accessor.
func (s *AIStore) ClusterEndpoints() store.ClusterEndpointStore { return s.clusterEndpoints }

// AuthzRules returns the control-plane-backed authorization-rule accessor.
//
// The rule set was the one resource whose read path moved to the objects while
// its write path stayed on SQL, and the mismatch is what disabled the admin API
// (see AuthzRuleStore). Serving both directions from the objects is what makes
// a rule written here the rule the policy evaluates.
func (s *AIStore) AuthzRules() store.AuthzRuleStore { return s.authzRules }

// RuntimeProfiles returns the control-plane-backed runtime contract accessor.
func (s *AIStore) RuntimeProfiles() store.RuntimeProfileStore { return s.runtimeProfiles }

// SandboxTemplates returns the control-plane-backed sandbox template accessor.
func (s *AIStore) SandboxTemplates() store.SandboxTemplateStore { return s.sandboxTemplates }

// RuntimeHostings returns the control-plane-backed hosting pool accessor.
func (s *AIStore) RuntimeHostings() store.RuntimeHostingStore { return s.runtimeHostings }

// ScheduledRuns returns the control-plane-backed cron schedule accessor.
func (s *AIStore) ScheduledRuns() store.ScheduledRunStore { return s.scheduledRuns }

// The remaining accessors are still SQL-backed.
func (s *AIStore) MCPs() store.MCPStore                 { return s.mcps }
func (s *AIStore) Secrets() store.SecretStore           { return s.secrets }
func (s *AIStore) AccessKeys() store.AccessKeyStore     { return s.fallback.AccessKeys() }
func (s *AIStore) UserGroups() store.UserGroupStore     { return s.fallback.UserGroups() }
func (s *AIStore) Models() store.ModelStore             { return s.models }
func (s *AIStore) Interactions() store.InteractionStore { return s.fallback.Interactions() }
func (s *AIStore) A2AInvocations() store.A2AInvocationStore {
	return s.fallback.A2AInvocations()
}
func (s *AIStore) K8SGPTEndpoints() store.K8SGPTEndpointStore {
	return s.fallback.K8SGPTEndpoints()
}
func (s *AIStore) RetrievalAudit() store.RetrievalAuditStore {
	return s.fallback.RetrievalAudit()
}
func (s *AIStore) Assets() store.AssetStore { return s.fallback.Assets() }

// Refresh is a no-op: a lister is always current, so there is no control-plane
// snapshot to rebuild, and the SQL side this store is built over is the uncached
// one (its readers that need a snapshot own it themselves).
func (s *AIStore) Refresh(context.Context) error { return nil }

// Start is a no-op for the same reason.
func (s *AIStore) Start(context.Context) error { return nil }
