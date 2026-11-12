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

import (
	"context"
	"errors"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// errStoreDisabled is returned by write operations when no resource-store
// backend is configured.
var errStoreDisabled = errors.New("resource store is not configured")

// emptyStore is a no-op AIStore used when no resource backend is configured
// (store source is "" or the backend is unavailable). Reads degrade to
// empty/not-found and writes return an explicit error, so the management API
// never dereferences a nil store.
type emptyStore struct{}

// Ensure emptyStore implements the aggregate interface.
var _ AIStore = emptyStore{}

// NewEmptyStore returns a no-op AIStore for the "store disabled" case.
func NewEmptyStore() AIStore { return emptyStore{} }

// NewEmptyRemainingStore returns a no-op RemainingStore. It is what a
// control-plane-only deployment uses for the tables that still live in SQL:
// their readers get an empty/refusing store instead of a nil panic.
func NewEmptyRemainingStore() RemainingStore { return emptyStore{} }

func (emptyStore) Agents() AgentStore                 { return emptyStoreOf[model.AgentM]{} }
func (emptyStore) AgentTemplates() AgentTemplateStore { return emptyStoreOf[model.AgentTemplateM]{} }
func (emptyStore) AgentBindings() AgentBindingStore   { return emptyAgentBindingStore{} }
func (emptyStore) ClusterAgentBindings() ClusterAgentBindingStore {
	return emptyClusterAgentBindingStore{}
}
func (emptyStore) ReferenceGrants() ReferenceGrantStore { return emptyReferenceGrantStore{} }
func (emptyStore) Skills() SkillStore                   { return emptyStoreOf[model.SkillM]{} }
func (emptyStore) CustomCommands() CustomCommandStore   { return emptyStoreOf[model.CustomCommandM]{} }
func (emptyStore) MCPs() MCPStore                       { return emptyStoreOf[model.MCPM]{} }
func (emptyStore) KnowledgeBases() KnowledgeBaseStore   { return emptyStoreOf[model.KnowledgeBaseM]{} }
func (emptyStore) Memories() MemoryStore                { return emptyStoreOf[model.MemoryM]{} }
func (emptyStore) Secrets() SecretStore                 { return emptyStoreOf[model.SecretM]{} }
func (emptyStore) Prompts() PromptStore                 { return emptyStoreOf[model.PromptM]{} }
func (emptyStore) Models() ModelStore                   { return emptyStoreOf[model.ModelM]{} }
func (emptyStore) Namespaces() NamespaceStore           { return emptyStoreOf[model.NamespaceM]{} }
func (emptyStore) AuthzRules() AuthzRuleStore           { return emptyStoreOf[model.AuthzRuleM]{} }
func (emptyStore) Interactions() InteractionStore       { return emptyStoreOf[model.InteractionM]{} }
func (emptyStore) A2AInvocations() A2AInvocationStore {
	return emptyStoreOf[model.A2AInvocationM]{}
}
func (emptyStore) K8SGPTEndpoints() K8SGPTEndpointStore {
	return emptyStoreOf[model.K8SGPTEndpointM]{}
}
func (emptyStore) Webhooks() WebhookStore              { return emptyStoreOf[model.WebhookM]{} }
func (emptyStore) RetrievalAudit() RetrievalAuditStore { return emptyStoreOf[model.RetrievalAuditM]{} }
func (emptyStore) Assets() AssetStore                  { return emptyAssetStore{} }
func (emptyStore) AccessKeys() AccessKeyStore          { return emptyAccessKeyStore{} }
func (emptyStore) UserGroups() UserGroupStore          { return emptyUserGroupStore{} }
func (emptyStore) SandboxTemplates() SandboxTemplateStore {
	return emptyStoreOf[model.SandboxTemplateM]{}
}
func (emptyStore) ClusterEndpoints() ClusterEndpointStore {
	return emptyStoreOf[model.ClusterEndpointM]{}
}

func (emptyStore) RuntimeProfiles() RuntimeProfileStore {
	return emptyStoreOf[model.RuntimeProfileM]{}
}
func (emptyStore) RuntimeHostings() RuntimeHostingStore {
	return emptyStoreOf[model.RuntimeHostingM]{}
}
func (emptyStore) ScheduledRuns() ScheduledRunStore {
	return emptyStoreOf[model.ScheduledRunM]{}
}
func (emptyStore) Refresh(context.Context) error { return nil }
func (emptyStore) Start(context.Context) error   { return nil }

// emptyStoreOf is the no-op standard resource store for one type: reads answer
// empty/not-found, writes refuse rather than pretending to persist. One
// implementation serves every type, so a new type cannot arrive with a subtly
// different idea of what "disabled" means.
type emptyStoreOf[T any] struct{}

var _ AgentStore = emptyStoreOf[model.AgentM]{}

func (emptyStoreOf[T]) Create(context.Context, *T) error { return errStoreDisabled }
func (emptyStoreOf[T]) Update(context.Context, *T) error { return errStoreDisabled }
func (emptyStoreOf[T]) Upsert(context.Context, *T) error { return errStoreDisabled }
func (emptyStoreOf[T]) Delete(context.Context, *where.Options) error {
	return errStoreDisabled
}

func (emptyStoreOf[T]) Get(context.Context, *where.Options) (*T, error) {
	return nil, ErrNotFound
}

func (emptyStoreOf[T]) List(context.Context, *where.Options) (int64, []*T, error) {
	return 0, nil, nil
}

func (emptyStoreOf[T]) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return nil, errStoreDisabled
}

// UpdateStatus is the scheduler's status writer; a disabled store has nothing
// to write.
func (emptyStoreOf[T]) UpdateStatus(context.Context, string, string, int64) error { return nil }

// emptyAgentBindingStore is the no-op binding accessor. It is written out rather
// than derived from emptyStoreOf because the binding store speaks the
// control-plane object instead of a row type.
type emptyAgentBindingStore struct{}

var _ AgentBindingStore = emptyAgentBindingStore{}

func (emptyAgentBindingStore) Create(context.Context, *agentv1alpha1.AgentBinding) (*agentv1alpha1.AgentBinding, error) {
	return nil, errStoreDisabled
}

func (emptyAgentBindingStore) Update(context.Context, *agentv1alpha1.AgentBinding) (*agentv1alpha1.AgentBinding, error) {
	return nil, errStoreDisabled
}

func (emptyAgentBindingStore) Delete(context.Context, string) error { return errStoreDisabled }

func (emptyAgentBindingStore) Get(context.Context, string) (*agentv1alpha1.AgentBinding, error) {
	return nil, ErrNotFound
}

func (emptyAgentBindingStore) List(context.Context, int, int) (int64, []*agentv1alpha1.AgentBinding, error) {
	return 0, nil, nil
}

// emptyReferenceGrantStore is the no-op reference-grant accessor.
type emptyReferenceGrantStore struct{}

var _ ReferenceGrantStore = emptyReferenceGrantStore{}

func (emptyReferenceGrantStore) Create(context.Context, *agentv1alpha1.ReferenceGrant) (*agentv1alpha1.ReferenceGrant, error) {
	return nil, errStoreDisabled
}

func (emptyReferenceGrantStore) Update(context.Context, *agentv1alpha1.ReferenceGrant) (*agentv1alpha1.ReferenceGrant, error) {
	return nil, errStoreDisabled
}

func (emptyReferenceGrantStore) Delete(context.Context, string) error { return errStoreDisabled }

func (emptyReferenceGrantStore) Get(context.Context, string) (*agentv1alpha1.ReferenceGrant, error) {
	return nil, ErrNotFound
}

func (emptyReferenceGrantStore) List(context.Context, int, int) (int64, []*agentv1alpha1.ReferenceGrant, error) {
	return 0, nil, nil
}

func (emptyReferenceGrantStore) ListLive(context.Context) ([]*agentv1alpha1.ReferenceGrant, error) {
	return nil, nil
}

// emptyClusterAgentBindingStore is the no-op cluster-wide binding accessor.
type emptyClusterAgentBindingStore struct{}

var _ ClusterAgentBindingStore = emptyClusterAgentBindingStore{}

func (emptyClusterAgentBindingStore) Create(context.Context, *agentv1alpha1.ClusterAgentBinding) (*agentv1alpha1.ClusterAgentBinding, error) {
	return nil, errStoreDisabled
}

func (emptyClusterAgentBindingStore) Update(context.Context, *agentv1alpha1.ClusterAgentBinding) (*agentv1alpha1.ClusterAgentBinding, error) {
	return nil, errStoreDisabled
}

func (emptyClusterAgentBindingStore) Delete(context.Context, string) error { return errStoreDisabled }

func (emptyClusterAgentBindingStore) Get(context.Context, string) (*agentv1alpha1.ClusterAgentBinding, error) {
	return nil, ErrNotFound
}

func (emptyClusterAgentBindingStore) List(context.Context, int, int) (int64, []*agentv1alpha1.ClusterAgentBinding, error) {
	return 0, nil, nil
}

// emptyAccessKeyStore is the no-op personal credential store. Reads answer
// "nothing there"; writes refuse rather than pretending to persist a credential
// that could never be verified.
type emptyAccessKeyStore struct{}

var _ AccessKeyStore = emptyAccessKeyStore{}

func (emptyAccessKeyStore) Create(context.Context, *model.AccessKeyM) error {
	return errStoreDisabled
}
func (emptyAccessKeyStore) Get(context.Context, string, string) (*model.AccessKeyM, error) {
	return nil, ErrNotFound
}
func (emptyAccessKeyStore) List(context.Context, string) ([]*model.AccessKeyM, error) {
	return nil, nil
}
func (emptyAccessKeyStore) Count(context.Context, string) (int64, error) { return 0, nil }
func (emptyAccessKeyStore) SetEnabled(context.Context, string, string, bool) error {
	return errStoreDisabled
}
func (emptyAccessKeyStore) Delete(context.Context, string, string) error {
	return errStoreDisabled
}

// emptyUserGroupStore is the no-op group store. Reads answer "no groups",
// writes refuse. Returning an empty group list here is safe for the admin
// surface, but the authorization resolver distinguishes this backend from a
// genuinely empty table by failing the process's store wiring instead of
// treating it as "the user is in no groups".
type emptyUserGroupStore struct{}

var _ UserGroupStore = emptyUserGroupStore{}

func (emptyUserGroupStore) Create(context.Context, *model.UserGroupM) error {
	return errStoreDisabled
}
func (emptyUserGroupStore) Update(context.Context, *model.UserGroupM) error {
	return errStoreDisabled
}
func (emptyUserGroupStore) Delete(context.Context, string) error { return errStoreDisabled }
func (emptyUserGroupStore) Get(context.Context, string) (*model.UserGroupM, error) {
	return nil, ErrNotFound
}
func (emptyUserGroupStore) List(context.Context, ListUserGroupsOptions) (int64, []*model.UserGroupM, error) {
	return 0, nil, nil
}
func (emptyUserGroupStore) GroupCodesFor(context.Context, string) ([]string, error) {
	return nil, nil
}

// emptyAssetStore adds the asset-specific lookups to the no-op store.
type emptyAssetStore struct{ emptyStoreOf[model.AssetM] }

func (emptyAssetStore) GetByObjectKey(context.Context, string) (*model.AssetM, error) {
	return nil, ErrNotFound
}

func (emptyAssetStore) GetByDigest(context.Context, string, string) (*model.AssetM, error) {
	return nil, ErrNotFound
}

func (emptyAssetStore) ListExpired(context.Context, time.Time, int) ([]*model.AssetM, error) {
	return nil, nil
}
