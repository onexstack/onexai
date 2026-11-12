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

package sqlstore

import (
	"context"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// Store is the GORM-backed implementation of the standard store shape.
type Store struct {
	db *gorm.DB

	// userGroups is created once so every caller shares one membership cache.
	// A cache per accessor call would be no cache at all, and the authorization
	// path asks this question on every run.
	userGroups *userGroupStore
}

var (
	// The migrated resources (agents, skills, MCP servers, knowledge bases,
	// prompts, models, webhooks) are served by the control plane; this store now
	// covers only the tables that stayed behind.
	_ agentstore.RemainingStore = (*Store)(nil)
	_ agentstore.TxStore        = (*Store)(nil)
)

// NewStore returns a standard store over db.
func NewStore(db *gorm.DB) *Store {
	s := &Store{db: db}
	s.userGroups = newUserGroupStore(s)
	return s
}

// DB returns the database handle, using the transaction in ctx when there is
// one, with every provided where applied.
func (s *Store) DB(ctx context.Context, wheres ...where.Where) *gorm.DB {
	db := s.db
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		db = tx
	}
	for _, w := range wheres {
		if w != nil {
			db = w.Where(db)
		}
	}
	return db
}

// TX runs fn inside one transaction. A returned error rolls the whole unit back.
func (s *Store) TX(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// The SQL tables that remain: each accessor names the one thing the standard
// store cannot infer — the key column and how to read it off a row.
//
// The resources that moved to the control plane (agents, skills, MCP servers,
// knowledge bases, prompts, models, webhooks) no longer have accessors here:
// their tables are read-only history now, kept for one release of rollback.

// Secrets returns the standard CRUD surface for secret definitions, keyed by
// secret ID.
func (s *Store) Secrets() agentstore.SecretStore { return newSecretStore(s) }

// AccessKeys returns the personal credential surface.
//
// Unlike the other accessors it is hand-written: every operation carries the
// owner in its predicate, which the generic store's where.Options cannot
// guarantee.
func (s *Store) AccessKeys() agentstore.AccessKeyStore { return newAccessKeyStore(s) }

// UserGroups returns the console-managed group surface.
func (s *Store) UserGroups() agentstore.UserGroupStore { return s.userGroups }

// Namespaces returns the CRUD surface for business-line rows.
//
// The reverse sync writes through this accessor; the read/write path the console
// uses goes through the control-plane composite, which writes the Namespace
// object first and then lands the row here.
func (s *Store) Namespaces() agentstore.NamespaceStore { return newNamespaceStore(s) }

// Interactions returns the standard CRUD surface for recorded interactions.
func (s *Store) Interactions() agentstore.InteractionStore { return newInteractionStore(s) }

// A2AInvocations returns the outbound A2A call log.
func (s *Store) A2AInvocations() agentstore.A2AInvocationStore {
	return newA2AInvocationStore(s)
}

// K8SGPTEndpoints returns the standard CRUD surface for cluster k8sgpt
// endpoints.
func (s *Store) K8SGPTEndpoints() agentstore.K8SGPTEndpointStore {
	return newK8SGPTEndpointStore(s)
}

// RetrievalAudit returns the standard CRUD surface for retrieval-audit rows.
func (s *Store) RetrievalAudit() agentstore.RetrievalAuditStore { return newRetrievalAuditStore(s) }

// Assets returns the standard CRUD surface for uploaded asset rows.
func (s *Store) Assets() agentstore.AssetStore { return newAssetStore(s) }
