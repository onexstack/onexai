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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/onexstack/onexai/internal/chat/model"
)

// PeerBindClaimStore defines the standard persistence API for
// chat.peer_bind_claim plus the claim state machine.
type PeerBindClaimStore interface {
	Create(ctx context.Context, obj *model.PeerBindClaimM) error
	Update(ctx context.Context, obj *model.PeerBindClaimM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.PeerBindClaimM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.PeerBindClaimM, error)

	PeerBindClaimExpansion
}

// PeerBindClaimExpansion holds the claim operations that are not the standard
// five.
type PeerBindClaimExpansion interface {
	// Claim records that a peer spoke the given code, and reports whether this
	// call is the one that recorded it along with the confirmation code to speak
	// back.
	//
	// # First writer wins, and that is still what decides *who is asking*
	//
	// The code is typed into a chat, and a chat has an audience: anyone in the
	// room can send it first. Whoever sends it first is the identity this row
	// will name, so the first write has to be the only one — a later claim for
	// the same code returns false and changes nothing.
	//
	// It cannot be checked any other way. The chat side has no idea which
	// account the code was minted for; that is exactly the fact it is waiting to
	// be told.
	//
	// # What first-writer-wins no longer decides
	//
	// It used to decide the binding. It now decides only who gets handed a
	// confirmation code — the returned string, which the caller must speak back
	// into the same conversation. The business side still owns the binding code
	// and will not commit on this row alone, so a first writer who read the code
	// over somebody's shoulder collects a confirmation code for *their own*
	// attempt and no binding (see ConfirmHash).
	//
	// The confirmation code is returned for the winner only. Every other outcome
	// returns the empty string: a loser must never be handed one, because there
	// is no case where somebody else's claim needs confirming by the person who
	// did not make it.
	Claim(ctx context.Context, code, backend, scope, peerID string, ttl time.Duration) (won bool, confirm string, err error)

	// Reissue rotates the confirmation code on a live claim the caller already
	// spoke, and returns the new one.
	//
	// # Why a repeat has to hand out a fresh code rather than the old one
	//
	// Only the digest is stored, so the words of the first code cannot be
	// recovered to say them again. Repeating is not an error — the user may have
	// scrolled past the message, or opened Feishu on a second device — and the
	// alternative (refusing, or replying "use the code you were already sent")
	// strands exactly the person whose binding is fine.
	//
	// So the second send supersedes the first: the console's next fill-in must
	// use the newest code. That is also why the caller's reply must say the older
	// one is dead — a user holding two codes and guessing wrong would read the
	// mismatch as a broken feature.
	//
	// ok is false when there is no live row for this (code, peerID) pair, which
	// covers the row having expired, having been swept, or belonging to somebody
	// else. Nothing is written in that case, and in particular a row belonging to
	// another peer is never rotated — that would let a stranger invalidate the
	// confirmation code of the person the claim actually names.
	Reissue(ctx context.Context, code, peerID string) (confirm string, ok bool, err error)

	// GetLive returns a live claim, or nil when there is none or it has expired.
	//
	// Expiry is applied here rather than left to the caller: an expired claim
	// must read as "nothing to find", and a caller that forgot to compare
	// ExpiresAt would bind a peer to a code that stopped meaning anything.
	GetLive(ctx context.Context, code string) (*model.PeerBindClaimM, error)

	// CleanupExpired deletes rows past ExpiresAt and reports how many went.
	CleanupExpired(ctx context.Context) (int64, error)
}

// peerBindClaimStore is the concrete PeerBindClaimStore implementation.
type peerBindClaimStore struct {
	*nilSafeStore[model.PeerBindClaimM]
	db *gorm.DB
}

var _ PeerBindClaimStore = (*peerBindClaimStore)(nil)

// newPeerBindClaimStore creates a peerBindClaimStore backed by the given store.
func newPeerBindClaimStore(s *store) *peerBindClaimStore {
	return &peerBindClaimStore{nilSafeStore: newNilSafeStore[model.PeerBindClaimM](s), db: s.db}
}

// List overrides the generic List because this table's primary key is `code`,
// not `id`: the inherited ORDER BY id desc would be a SQL error.
func (s *peerBindClaimStore) List(
	ctx context.Context, opts *where.Options,
) (int64, []*model.PeerBindClaimM, error) {
	if s.db == nil {
		return 0, nil, nil
	}
	var total int64
	var out []*model.PeerBindClaimM

	countOpts := copyClaimOptions(opts, 0, -1)
	if err := countOpts.Where(s.db.WithContext(ctx).Model(&model.PeerBindClaimM{})).Count(&total).Error; err != nil {
		return 0, nil, err
	}
	if err := copyClaimOptions(opts, 0, 0).Where(
		s.db.WithContext(ctx).Model(&model.PeerBindClaimM{}),
	).Order("code").Find(&out).Error; err != nil {
		return 0, nil, err
	}
	return total, out, nil
}

// copyClaimOptions clones opts so the two statements do not share the Clauses
// that Options.Where appends to when Queries is non-empty.
func copyClaimOptions(opts *where.Options, offset, limit int) *where.Options {
	if opts == nil {
		return where.NewWhere().O(offset).L(limit)
	}
	cp := &where.Options{Offset: offset, Limit: limit}
	if opts.Filters != nil {
		cp.Filters = make(map[any]any, len(opts.Filters))
		for k, v := range opts.Filters {
			cp.Filters[k] = v
		}
	}
	cp.Queries = append([]where.Query(nil), opts.Queries...)
	cp.Clauses = append([]clause.Expression(nil), opts.Clauses...)
	return cp
}

// Claim implements PeerBindClaimStore.
//
// The insert is an ON CONFLICT DO NOTHING rather than a read-then-write: two
// messages carrying the same code can arrive in the same instant, and a
// check-then-insert would let both of them believe they were first.
//
// The confirmation code is drawn before the insert because it is part of the
// row, and it is returned only on the write that won. A loser gets the empty
// string: the code it drew in a call it lost names nobody, and handing it over
// would invite the caller to speak a confirmation for a claim that is not
// theirs.
func (s *peerBindClaimStore) Claim(
	ctx context.Context, code, backend, scope, peerID string, ttl time.Duration,
) (bool, string, error) {
	if s.db == nil || code == "" || peerID == "" {
		return false, "", nil
	}
	confirm, err := newConfirmCode()
	if err != nil {
		return false, "", err
	}
	now := time.Now()
	rec := model.PeerBindClaimM{
		Code:        code,
		Backend:     backend,
		Scope:       scope,
		PeerID:      peerID,
		ConfirmHash: hashConfirm(confirm),
		ClaimedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}
	res := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoNothing: true}).
		Create(&rec)
	if res.Error != nil {
		return false, "", res.Error
	}
	if res.RowsAffected != 1 {
		return false, "", nil
	}
	return true, confirm, nil
}

// Reissue implements PeerBindClaimStore.
//
// One conditional UPDATE rather than a read-then-write, for the reason Claim is
// one insert: the predicate is what decides, and a read that answered "yes, this
// row is yours" followed by a write would let the row change underneath the two
// statements. Here the whole decision is the WHERE clause, and RowsAffected says
// whether it matched.
//
// expires_at > now() is in the predicate because the sweeper runs on a timer and
// a lapsed row can still be on disk: rotating one would hand out a confirmation
// code for a claim Get already reports as absent, and the user would fill it in
// to be told the code expired.
func (s *peerBindClaimStore) Reissue(ctx context.Context, code, peerID string) (string, bool, error) {
	if s.db == nil || code == "" || peerID == "" {
		return "", false, nil
	}
	confirm, err := newConfirmCode()
	if err != nil {
		return "", false, err
	}
	res := s.db.WithContext(ctx).
		Model(&model.PeerBindClaimM{}).
		Where("code = ? AND peer_id = ? AND expires_at > ?", code, peerID, time.Now()).
		Update("confirm_hash", hashConfirm(confirm))
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected != 1 {
		return "", false, nil
	}
	return confirm, true, nil
}

// Get implements PeerBindClaimStore.
func (s *peerBindClaimStore) GetLive(ctx context.Context, code string) (*model.PeerBindClaimM, error) {
	if s.db == nil || code == "" {
		return nil, nil
	}
	var rec model.PeerBindClaimM
	err := s.db.WithContext(ctx).
		Where("code = ? AND expires_at > ?", code, time.Now()).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// CleanupExpired implements PeerBindClaimStore.
//
// Nothing consumes a row on read, so expired rows are the only way this table
// grows. A failed binding attempt leaves one behind every time, which makes the
// sweep routine rather than exceptional.
func (s *peerBindClaimStore) CleanupExpired(ctx context.Context) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	res := s.db.WithContext(ctx).
		Where("expires_at <= ?", time.Now()).
		Delete(&model.PeerBindClaimM{})
	return res.RowsAffected, res.Error
}
