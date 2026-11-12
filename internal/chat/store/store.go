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
	"sync"

	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"
)

// ProviderSet defines the dependency injection providers for the store layer.
// It binds the abstract Interface to the concrete implementation *store.
var ProviderSet = wire.NewSet(NewStore, wire.Bind(new(IStore), new(*store)))

var (
	once sync.Once
	// S is a global variable for convenient access to the initialized store
	// instance from other packages.
	S *store
)

// ErrRecordNotFound is the sentinel returned when a lookup misses. Callers use
// errors.Is(err, ErrRecordNotFound) regardless of which backend is active.
var ErrRecordNotFound = gorm.ErrRecordNotFound

// IStore defines the methods that the persistence layer must implement.
type IStore interface {
	// DB returns the underlying *gorm.DB instance, optionally applying filter conditions.
	DB(ctx context.Context, wheres ...where.Where) *gorm.DB
	// TX executes the given function within a database transaction.
	TX(ctx context.Context, fn func(ctx context.Context) error) error

	// Session returns the session store.
	Session() SessionStore
	// Ticket returns the ticket store.
	Ticket() TicketStore
	// TicketNote returns the store of a ticket's handling log.
	TicketNote() TicketNoteStore
	// Job returns the job store.
	Job() JobStore
	// CronJob returns the cronjob store.
	CronJob() CronJobStore
	// Messages returns the message cache store.
	Messages() MessageStore
	// ChatMessages returns the archive of messages from dissolved groups.
	ChatMessages() ChatMessageStore
	// PeerAgents returns the chat-peer-to-agent binding store.
	PeerAgents() PeerAgentStore
	// PeerBindClaims returns the one-time binding-handshake store.
	PeerBindClaims() PeerBindClaimStore
	// OncallGroups returns the on-call group metadata store.
	OncallGroups() OncallGroupStore
	// OncallSchedules returns the native on-call schedule store.
	OncallSchedules() OncallScheduleStore
	// TicketEscalation returns the ticket escalation runtime state store.
	TicketEscalation() TicketEscalationStore
	// Users returns the platform account, Feishu binding and bind-code store.
	Users() UserStore
	// Metadata returns the store of the resource-metadata table.
	Metadata() MetadataStore
}

// txKey is the context key for storing the transaction *gorm.DB instance.
type txKey struct{}

// store is the concrete implementation of the Interface.
type store struct {
	db *gorm.DB

	// Additional database instances can be added as needed.
}

// Ensure store implements the Interface at compile time.
var _ IStore = (*store)(nil)

// NewStore creates and returns a new store instance.
func NewStore(db *gorm.DB) *store {
	// Initialize the singleton store instance only once.
	once.Do(func() { S = &store{db} })

	return S
}

// DB returns the database instance. If a transaction exists in the context,
// it returns the transactional DB; otherwise, it returns the core DB.
// Optional 'where' clauses can be applied to the returned DB instance.
func (s *store) DB(ctx context.Context, wheres ...where.Where) *gorm.DB {
	db := s.db
	// Retrieve transaction from context if it exists.
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		db = tx
	}

	for _, w := range wheres {
		db = w.Where(db)
	}

	return db
}

// TX executes the provided function fn within a database transaction.
// It injects the transaction handle into the context passed to fn.
func (s *store) TX(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Inject the transaction DB into the context.
		ctx := context.WithValue(ctx, txKey{}, tx)
		return fn(ctx)
	})
}

// Session returns an instance that implements the SessionStore interface.
func (s *store) Session() SessionStore {
	return newSessionStore(s)
}

// Ticket returns an instance that implements the TicketStore interface.
func (s *store) Ticket() TicketStore {
	return newTicketStore(s)
}

// TicketNote returns an instance that implements the TicketNoteStore interface.
func (s *store) TicketNote() TicketNoteStore {
	return newTicketNoteStore(s)
}

// Job returns an instance that implements the JobStore interface.
func (s *store) Job() JobStore {
	return newJobStore(s)
}

// CronJob returns an instance that implements the CronJobStore interface.
func (s *store) CronJob() CronJobStore {
	return newCronJobStore(s)
}

// Messages returns an instance that implements the MessageStore interface.
func (s *store) Messages() MessageStore {
	return newMessageStore(s)
}

// ChatMessages returns the archive of messages from dissolved groups.
func (s *store) ChatMessages() ChatMessageStore { return newChatMessageStore(s) }

// PeerAgents returns an instance that implements the PeerAgentStore interface.
func (s *store) PeerAgents() PeerAgentStore {
	return newPeerAgentStore(s)
}

// PeerBindClaims returns an instance that implements the PeerBindClaimStore
// interface.
func (s *store) PeerBindClaims() PeerBindClaimStore {
	return newPeerBindClaimStore(s)
}

// OncallGroups returns an instance that implements the OncallGroupStore interface.
func (s *store) OncallGroups() OncallGroupStore {
	return newOncallGroupStore(s)
}

// OncallSchedules returns an instance that implements the OncallScheduleStore interface.
func (s *store) OncallSchedules() OncallScheduleStore {
	return newOncallScheduleStore(s)
}

// TicketEscalation returns an instance that implements the TicketEscalationStore interface.
func (s *store) TicketEscalation() TicketEscalationStore {
	return newTicketEscalationStore(s)
}

// Users returns an instance that implements the UserStore interface.
func (s *store) Users() UserStore {
	return newUserStore(s)
}

// Metadata returns an instance that implements the MetadataStore interface.
func (s *store) Metadata() MetadataStore { return newMetadataStore(s) }
