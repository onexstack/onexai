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

package store

// StandardStore is the aggregate accessor set every agent table exposes. The
// per-table interfaces live beside their resources (agent.go, skill.go, mcp.go,
// ...); this file only names the set so AIStore can extend it with Refresh.
//
// The migrated resources are implemented over the control plane in
// internal/agent/store/controlplanestore; the tables that stayed behind are
// implemented over GORM in internal/agent/store/sqlstore.
type StandardStore interface {
	Agents() AgentStore
	AgentTemplates() AgentTemplateStore
	AgentBindings() AgentBindingStore
	ClusterAgentBindings() ClusterAgentBindingStore
	ReferenceGrants() ReferenceGrantStore
	Skills() SkillStore
	CustomCommands() CustomCommandStore
	MCPs() MCPStore
	KnowledgeBases() KnowledgeBaseStore
	Memories() MemoryStore
	Secrets() SecretStore
	Prompts() PromptStore
	Models() ModelStore
	// Namespaces returns the business-line table. Unlike the other accessors it
	// is backed by a row store that the control plane keeps in sync: a business
	// line is a Kubernetes Namespace plus the metadata the console edits.
	Namespaces() NamespaceStore

	AuthzRules() AuthzRuleStore
	Interactions() InteractionStore
	A2AInvocations() A2AInvocationStore
	K8SGPTEndpoints() K8SGPTEndpointStore
	Webhooks() WebhookStore
	RetrievalAudit() RetrievalAuditStore
	Assets() AssetStore
	// AccessKeys returns the personal AccessKey/SecretKey table. It is in the
	// agent schema because this service serves the credential surface.
	AccessKeys() AccessKeyStore
	// UserGroups returns the console-managed group table. It is in the agent
	// schema because this is where authorization expands a member into group
	// subjects.
	UserGroups() UserGroupStore

	RuntimeProfiles() RuntimeProfileStore
	RuntimeHostings() RuntimeHostingStore
	ScheduledRuns() ScheduledRunStore
	SandboxTemplates() SandboxTemplateStore
	ClusterEndpoints() ClusterEndpointStore
}
