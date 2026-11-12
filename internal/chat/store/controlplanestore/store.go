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

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/pkg/declclient"
)

// Store is the chat store with the migrated resources served from the control
// plane and everything else delegated to SQL.
//
// A resource is served by exactly one side: peer bindings come from the API
// server; on-call groups, schedules, sessions, tickets, messages and the rest
// stay on SQL. That split is what keeps a write and a read of the same resource
// from disagreeing.
type Store struct {
	fallback store.IStore

	peerAgents *PeerBindingStore
}

// NewStore builds the composite over fallback. mirror, when set, redirects the
// List of the chat kinds it selects to the PostgreSQL projection.
func NewStore(
	fallback store.IStore, client *declclient.Client, namespace string, mirror *MirrorConfig,
) (*Store, error) {
	s := &Store{
		fallback:   fallback,
		peerAgents: NewPeerBindingStore(client, namespace),
	}
	if err := mirror.attach(s, client); err != nil {
		return nil, err
	}
	return s, nil
}

var _ store.IStore = (*Store)(nil)

// OncallGroups returns the SQL-backed duty-group accessor. Duty groups stopped
// being declarative resources when the schedule became a chat-owned domain
// object; the control plane is no longer one of their readers or writers.
func (s *Store) OncallGroups() store.OncallGroupStore { return s.fallback.OncallGroups() }

// OncallSchedules returns the SQL-backed schedule accessor.
func (s *Store) OncallSchedules() store.OncallScheduleStore { return s.fallback.OncallSchedules() }

// TicketEscalation returns the SQL-backed escalation state accessor.
func (s *Store) TicketEscalation() store.TicketEscalationStore { return s.fallback.TicketEscalation() }

// PeerAgents returns the control-plane-backed peer-binding accessor.
func (s *Store) PeerAgents() store.PeerAgentStore { return s.peerAgents }

// The remaining accessors are still SQL-backed.

func (s *Store) DB(ctx context.Context, wheres ...where.Where) *gorm.DB {
	return s.fallback.DB(ctx, wheres...)
}

func (s *Store) TX(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.fallback.TX(ctx, fn)
}

func (s *Store) Session() store.SessionStore              { return s.fallback.Session() }
func (s *Store) Ticket() store.TicketStore                { return s.fallback.Ticket() }
func (s *Store) TicketNote() store.TicketNoteStore        { return s.fallback.TicketNote() }
func (s *Store) Job() store.JobStore                      { return s.fallback.Job() }
func (s *Store) CronJob() store.CronJobStore              { return s.fallback.CronJob() }
func (s *Store) Messages() store.MessageStore             { return s.fallback.Messages() }
func (s *Store) ChatMessages() store.ChatMessageStore     { return s.fallback.ChatMessages() }
func (s *Store) PeerBindClaims() store.PeerBindClaimStore { return s.fallback.PeerBindClaims() }
func (s *Store) Users() store.UserStore                   { return s.fallback.Users() }
func (s *Store) Metadata() store.MetadataStore            { return s.fallback.Metadata() }
