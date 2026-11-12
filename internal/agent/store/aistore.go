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

import "context"

// AIStore is the aggregate accessor for every agent table. Each accessor
// returns the table's standard store (Create/Update/Delete/Get/List over
// where.Options, plus that table's Expansion for the operations that are not
// one of the five).
//
// It is implemented by the control-plane composite
// (internal/agent/store/controlplanestore) for the migrated resources, with the
// tables that stayed behind delegated to the GORM store
// (internal/agent/store/sqlstore, MySQL or PostgreSQL depending on the coredb
// dialect). It is an implementation on purpose: it is the seam a test replaces
// (see empty.go), not a backend selector.
type AIStore interface {
	StandardStore

	// Refresh reloads the snapshots a backend keeps. It is a no-op for a lister,
	// which is always current.
	Refresh(ctx context.Context) error
	// Start warms the snapshots a backend keeps and keeps them fresh on the
	// configured interval (no-op for live backends). It is called once at wiring
	// time.
	Start(ctx context.Context) error
}

// RemainingStore is the accessor set still served by SQL while the rest of the
// resources moved to the declarative control plane.
//
// It exists so the composite store cannot quietly keep a SQL reader for a
// migrated resource: a resource is either here or on the control plane, never
// both. Authorization rules left this set when their objects became the only
// thing the OPA rule store reads — keeping a SQL reader here would have let a
// write land where nothing evaluates it.
type RemainingStore interface {
	// AccessKeys is the personal credential table. It never moved to the
	// control plane; the SQL side owns it.
	AccessKeys() AccessKeyStore
	// UserGroups is the console-managed group table. It never moved to the
	// control plane; membership is identity hot data and the SQL side owns it.
	UserGroups() UserGroupStore
	Interactions() InteractionStore
	A2AInvocations() A2AInvocationStore
	K8SGPTEndpoints() K8SGPTEndpointStore
	RetrievalAudit() RetrievalAuditStore
	Assets() AssetStore
}
