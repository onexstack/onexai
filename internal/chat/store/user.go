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
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// UserStore is the persistence surface for accounts, Feishu bindings and the
// one-time codes the binding handshake uses.
//
// The three tables are one accessor rather than three because every operation
// that matters spans at least two of them (register + binding, confirm + claim
// consumption, unbind + binding removal), and a caller that had to fetch three
// sub-stores would be the caller deciding how they are grouped.
type UserStore interface {
	Create(ctx context.Context, obj *model.UserM) error
	Update(ctx context.Context, obj *model.UserM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.UserM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.UserM, error)

	UserExpansion
}

// UserExpansion holds the account-shaped operations that are not the standard
// five.
type UserExpansion interface {
	// GetByUsername looks an account up by its unique login name.
	GetByUsername(ctx context.Context, username string) (*model.UserM, error)
	// GetByID looks an account up by its stable ID.
	GetByID(ctx context.Context, id string) (*model.UserM, error)
	// ResolveByName looks an account up by an on-call name: a login name or an
	// email, compared case-insensitively. It also returns the account's Feishu
	// binding when one exists. An email that matches more than one account
	// resolves to (nil, nil, nil): an ambiguous name must never page the wrong
	// person.
	ResolveByName(ctx context.Context, name string) (*model.UserM, *model.UserLarkBindingM, error)
	// ListPage returns one page of accounts together with the total. An empty
	// keyword or status means "no filter".
	ListPage(ctx context.Context, page, pageSize int, keyword, status, role string) (int64, []*model.UserM, error)
	// Count returns the number of accounts, optionally filtered by role.
	Count(ctx context.Context, role string) (int64, error)
	// CountAll returns the number of accounts.
	CountAll(ctx context.Context) (int64, error)

	// LarkBinding returns the Feishu identity attached to an account, or nil.
	LarkBinding(ctx context.Context, userID string) (*model.UserLarkBindingM, error)
	// LarkBindingByOpenID returns the account binding that names an open_id, or
	// nil. It is the lookup Feishu login uses.
	LarkBindingByOpenID(ctx context.Context, openID string) (*model.UserLarkBindingM, error)
	// LarkBindingsByUserIDs returns the bindings for a page of accounts.
	LarkBindingsByUserIDs(ctx context.Context, userIDs []string) ([]*model.UserLarkBindingM, error)
	// ListLarkBindings returns every account-to-Feishu binding. The console's
	// administrator view needs the rows that have no PeerBinding too, so it
	// cannot be expressed as a lookup by account id.
	ListLarkBindings(ctx context.Context) ([]*model.UserLarkBindingM, error)
	// UsersByIDs batch-loads accounts. It exists so a page of bindings resolves
	// its canonical ids in one query instead of one per row.
	UsersByIDs(ctx context.Context, ids []string) ([]*model.UserM, error)
	// PutLarkBinding upserts by user_id: an account has at most one binding.
	PutLarkBinding(ctx context.Context, m *model.UserLarkBindingM) error
	// UpdateLarkBindingProfile fills the profile columns that are still empty
	// for the binding named by (user_id, open_id). It never overwrites a value
	// another writer stored, and it does nothing when the row is gone — unlike
	// PutLarkBinding, a backfill must not resurrect an unbound identity.
	UpdateLarkBindingProfile(ctx context.Context, userID, openID string, profile model.LarkProfile) error
	// DeleteLarkBinding removes an account's binding. Absence is not an error.
	DeleteLarkBinding(ctx context.Context, userID string) error

	// CreateBindCode records a code minted for an account.
	CreateBindCode(ctx context.Context, m *model.UserBindCodeM) error
	// GetLiveBindCode returns a code that belongs to the account and has not
	// expired. A code that expired reads as not found.
	GetLiveBindCode(ctx context.Context, code, userID string) (*model.UserBindCodeM, error)
	// DeleteBindCode consumes a code. Absence is not an error.
	DeleteBindCode(ctx context.Context, code string) error
	// DeleteBindCodes removes every pending code of an account (start replaces
	// the previous one, unbind abandons it).
	DeleteBindCodes(ctx context.Context, userID string) error
	// CleanupExpiredBindCodes deletes codes past their expiry.
	CleanupExpiredBindCodes(ctx context.Context) (int64, error)
}

// userStore implements UserStore over the chat database.
type userStore struct {
	st *store
}

var _ UserStore = (*userStore)(nil)

func newUserStore(s *store) *userStore { return &userStore{st: s} }

// session returns the database handle bound to ctx, so a caller inside TX sees
// the transaction. The parent store's DB(ctx) is the only place that knows
// about the transaction context key.
func (s *userStore) session(ctx context.Context) *gorm.DB {
	if s == nil || s.st == nil || s.st.db == nil {
		return nil
	}
	return s.st.DB(ctx).WithContext(ctx)
}

func (s *userStore) Create(ctx context.Context, obj *model.UserM) error {
	db := s.session(ctx)
	if db == nil {
		return ErrRecordNotFound
	}
	return db.Create(obj).Error
}

func (s *userStore) Update(ctx context.Context, obj *model.UserM) error {
	db := s.session(ctx)
	if db == nil {
		return ErrRecordNotFound
	}
	return db.Save(obj).Error
}

func (s *userStore) Delete(ctx context.Context, opts *where.Options) error {
	db := s.session(ctx)
	if db == nil {
		return nil
	}
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.UserM{}).Error
}

func (s *userStore) Get(ctx context.Context, opts *where.Options) (*model.UserM, error) {
	db := s.session(ctx)
	if db == nil {
		return nil, ErrRecordNotFound
	}
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.UserM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *userStore) List(ctx context.Context, opts *where.Options) (int64, []*model.UserM, error) {
	// The standard List is the generic store's: all matching rows, ordered by
	// id. ListPage is the management page's shape (newest first, keyword
	// filter), and folding the two together would make a caller that asked for
	// "the list" silently receive only the first 20 rows.
	return newNilSafeStore[model.UserM](s.st).List(ctx, opts)
}

// GetByUsername implements UserStore.
func (s *userStore) GetByUsername(ctx context.Context, username string) (*model.UserM, error) {
	if username == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return s.Get(ctx, where.F("username", username))
}

// GetByID implements UserStore.
func (s *userStore) GetByID(ctx context.Context, id string) (*model.UserM, error) {
	if id == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return s.Get(ctx, where.F("id", id))
}

// ResolveByName implements UserStore.
//
// The lookup order follows the uniqueness of the columns: username is unique
// and indexed, so it is tried first (exact, then case-insensitive), and email
// has no unique index, so it only resolves when exactly one account matches.
// Disabled accounts are deliberately included: they may still be on an existing
// rotation, and dropping them silently would make that on-call disappear.
func (s *userStore) ResolveByName(
	ctx context.Context, name string,
) (*model.UserM, *model.UserLarkBindingM, error) {
	db := s.session(ctx)
	name = strings.TrimSpace(name)
	if db == nil || name == "" {
		return nil, nil, nil
	}

	var user model.UserM
	err := db.Where("username = ?", name).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Where("LOWER(username) = ?", strings.ToLower(name)).First(&user).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Email has no unique constraint: more than one match is ambiguous and
		// must not be resolved by picking an arbitrary row.
		var matches []model.UserM
		if err := db.Where("LOWER(email) = ?", strings.ToLower(name)).
			Limit(2).Find(&matches).Error; err != nil {
			return nil, nil, err
		}
		if len(matches) != 1 {
			return nil, nil, nil
		}
		user = matches[0]
	} else if err != nil {
		return nil, nil, err
	}

	var binding model.UserLarkBindingM
	err = db.Where("user_id = ?", user.ID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &user, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &user, &binding, nil
}

// ListPage implements UserStore.
//
// It is hand-written rather than delegated to the generic store for two
// reasons: the keyword filter needs an OR across two columns (a plain filter
// map ANDs everything), and the generic list orders by `id`, which for a UUID
// primary key is a random order rather than "newest first".
func (s *userStore) ListPage(
	ctx context.Context, page, pageSize int, keyword, status, role string,
) (int64, []*model.UserM, error) {
	db := s.session(ctx)
	if db == nil {
		return 0, nil, nil
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	query := db.Model(&model.UserM{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if role != "" {
		query = query.Where("role = ?", role)
	}
	if keyword != "" {
		like := "%" + strings.ToLower(keyword) + "%"
		query = query.Where("LOWER(username) LIKE ? OR LOWER(display_name) LIKE ?", like, like)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}
	if total == 0 {
		return 0, nil, nil
	}

	var users []*model.UserM
	err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&users).Error
	return total, users, err
}

// CountAll implements UserStore.
func (s *userStore) CountAll(ctx context.Context) (int64, error) {
	return s.Count(ctx, "")
}

// Count implements UserStore.
func (s *userStore) Count(ctx context.Context, role string) (int64, error) {
	db := s.session(ctx)
	if db == nil {
		return 0, nil
	}
	query := db.Model(&model.UserM{})
	if role != "" {
		query = query.Where("role = ?", role)
	}
	var total int64
	return total, query.Count(&total).Error
}

// LarkBinding implements UserStore.
func (s *userStore) LarkBinding(ctx context.Context, userID string) (*model.UserLarkBindingM, error) {
	db := s.session(ctx)
	if db == nil || userID == "" {
		return nil, nil
	}
	var out model.UserLarkBindingM
	err := db.Where("user_id = ?", userID).First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LarkBindingByOpenID implements UserStore.
func (s *userStore) LarkBindingByOpenID(ctx context.Context, openID string) (*model.UserLarkBindingM, error) {
	db := s.session(ctx)
	if db == nil || openID == "" {
		return nil, nil
	}
	var out model.UserLarkBindingM
	err := db.Where("open_id = ?", openID).First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LarkBindingsByUserIDs implements UserStore.
func (s *userStore) LarkBindingsByUserIDs(
	ctx context.Context, userIDs []string,
) ([]*model.UserLarkBindingM, error) {
	db := s.session(ctx)
	if db == nil || len(userIDs) == 0 {
		return nil, nil
	}
	var out []*model.UserLarkBindingM
	if err := db.Where("user_id IN ?", userIDs).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListLarkBindings implements UserStore.
func (s *userStore) ListLarkBindings(ctx context.Context) ([]*model.UserLarkBindingM, error) {
	db := s.session(ctx)
	if db == nil {
		return nil, nil
	}
	var out []*model.UserLarkBindingM
	if err := db.Order("updated_at DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UsersByIDs implements UserStore.
func (s *userStore) UsersByIDs(ctx context.Context, ids []string) ([]*model.UserM, error) {
	db := s.session(ctx)
	if db == nil || len(ids) == 0 {
		return nil, nil
	}
	var out []*model.UserM
	if err := db.Where("id IN ?", ids).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// PutLarkBinding implements UserStore.
func (s *userStore) PutLarkBinding(ctx context.Context, m *model.UserLarkBindingM) error {
	db := s.session(ctx)
	if db == nil || m == nil || m.UserID == "" || m.OpenID == "" {
		return nil
	}
	if m.BoundAt.IsZero() {
		m.BoundAt = time.Now()
	}
	m.UpdatedAt = time.Now()

	var existing model.UserLarkBindingM
	err := db.Where("user_id = ?", m.UserID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return db.Create(m).Error
	case err != nil:
		return err
	default:
		m.ID = existing.ID
		return db.Save(m).Error
	}
}

// UpdateLarkBindingProfile implements UserStore.
//
// It is a targeted UPDATE rather than the read-modify-Save of PutLarkBinding:
// the backfill must not resurrect a binding that was deleted after it read the
// row, and it must not rewrite columns another writer just changed. The
// COALESCE guard keeps every non-empty value, so the update only fills holes.
// UpdateColumns is used on purpose: repairing a profile is not a binding event
// and must not move the row in the console's updated_at ordering.
func (s *userStore) UpdateLarkBindingProfile(
	ctx context.Context, userID, openID string, profile model.LarkProfile,
) error {
	db := s.session(ctx)
	if db == nil || userID == "" || openID == "" {
		return nil
	}
	if profile.LarkUserID == "" && profile.UnionID == "" &&
		profile.Name == "" && profile.AvatarURL == "" {
		return nil
	}

	fill := func(column string, value string) any {
		return gorm.Expr(
			"CASE WHEN COALESCE("+column+", '') = '' THEN ? ELSE "+column+" END",
			value,
		)
	}
	return db.Model(&model.UserLarkBindingM{}).
		Where("user_id = ? AND open_id = ?", userID, openID).
		UpdateColumns(map[string]any{
			"lark_user_id": fill("lark_user_id", profile.LarkUserID),
			"union_id":     fill("union_id", profile.UnionID),
			"name":         fill("name", profile.Name),
			"avatar_url":   fill("avatar_url", profile.AvatarURL),
		}).Error
}

// DeleteLarkBinding implements UserStore.
func (s *userStore) DeleteLarkBinding(ctx context.Context, userID string) error {
	db := s.session(ctx)
	if db == nil || userID == "" {
		return nil
	}
	return db.Where("user_id = ?", userID).Delete(&model.UserLarkBindingM{}).Error
}

// CreateBindCode implements UserStore.
func (s *userStore) CreateBindCode(ctx context.Context, m *model.UserBindCodeM) error {
	db := s.session(ctx)
	if db == nil || m == nil || m.Code == "" {
		return ErrRecordNotFound
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	return db.Create(m).Error
}

// GetLiveBindCode implements UserStore.
func (s *userStore) GetLiveBindCode(
	ctx context.Context, code, userID string,
) (*model.UserBindCodeM, error) {
	db := s.session(ctx)
	if db == nil || code == "" || userID == "" {
		return nil, nil
	}
	var out model.UserBindCodeM
	err := db.Where("code = ? AND user_id = ? AND expires_at > ?", code, userID, time.Now()).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBindCode implements UserStore.
func (s *userStore) DeleteBindCode(ctx context.Context, code string) error {
	db := s.session(ctx)
	if db == nil || code == "" {
		return nil
	}
	return db.Where("code = ?", code).Delete(&model.UserBindCodeM{}).Error
}

// DeleteBindCodes implements UserStore.
func (s *userStore) DeleteBindCodes(ctx context.Context, userID string) error {
	db := s.session(ctx)
	if db == nil || userID == "" {
		return nil
	}
	return db.Where("user_id = ?", userID).Delete(&model.UserBindCodeM{}).Error
}

// CleanupExpiredBindCodes implements UserStore.
func (s *userStore) CleanupExpiredBindCodes(ctx context.Context) (int64, error) {
	db := s.session(ctx)
	if db == nil {
		return 0, nil
	}
	res := db.Where("expires_at <= ?", time.Now()).Delete(&model.UserBindCodeM{})
	return res.RowsAffected, res.Error
}

// IsDuplicateError reports whether err is a unique-constraint violation from
// any of the supported dialects.
//
// # Why it is a string check
//
// GORM's TranslateError is off in this deployment, so the driver returns the
// dialect's own error: Postgres says "duplicate key value violates unique
// constraint", MySQL says "Duplicate entry", SQLite says "UNIQUE constraint
// failed". The alternative — pre-checking and then creating — leaves a race
// between two simultaneous registrations, and that race is exactly what the
// unique index exists for.
func IsDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique violation")
}
