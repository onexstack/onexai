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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// groupCodesTTL bounds how long a membership answer may be served from memory.
//
// The write path invalidates the cache in the same process, so the TTL is a
// safety net for another replica that wrote the group, not the normal path. It
// is short because a revoked membership is an authorization change.
const groupCodesTTL = 30 * time.Second

// groupCodesCacheMax bounds the alias map. 3000 accounts with four aliases is
// about 12k entries; the cap only stops a pathological caller from growing it
// without bound.
const groupCodesCacheMax = 100_000

type cachedGroupCodes struct {
	codes   []string
	expires time.Time
}

// errUserGroupStoreUnavailable is returned when the SQL backend is missing.
// It is an explicit failure rather than an empty list: a group list that
// silently reads as empty would revoke every group grant.
var errUserGroupStoreUnavailable = errors.New("user group store is not configured")

// userGroupStore implements the user-group tables.
//
// The group and its members are written in one transaction in every direction.
// A caller that could write one half would be able to leave a group whose
// members disagree with what the console just showed.
type userGroupStore struct {
	s *Store

	// codes is the alias -> group-code answer the authorization path reads on
	// every request. Empty answers are cached too: a user in no groups is the
	// common case and must not become a database read per tool call.
	mu    sync.RWMutex
	codes map[string]cachedGroupCodes
}

var _ agentstore.UserGroupStore = (*userGroupStore)(nil)

func newUserGroupStore(s *Store) *userGroupStore {
	return &userGroupStore{s: s, codes: make(map[string]cachedGroupCodes)}
}

// invalidate drops every cached membership answer. It runs after a write in the
// same process; other replicas converge on the TTL.
func (u *userGroupStore) invalidate() {
	u.mu.Lock()
	u.codes = make(map[string]cachedGroupCodes)
	u.mu.Unlock()
}

func (u *userGroupStore) db(ctx context.Context) *gorm.DB {
	if u == nil || u.s == nil {
		return nil
	}
	db := u.s.DB(ctx)
	if db == nil {
		return nil
	}
	return db.WithContext(ctx)
}

// Create implements UserGroupStore.
func (u *userGroupStore) Create(ctx context.Context, group *model.UserGroupM) error {
	db := u.db(ctx)
	if db == nil {
		return errUserGroupStoreUnavailable
	}
	if group.ID == "" {
		group.ID = uuid.NewString()
	}
	now := time.Now()
	if group.CreatedAt.IsZero() {
		group.CreatedAt = now
	}
	group.UpdatedAt = now

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(group).Error; err != nil {
			return err
		}
		return u.insertMembers(tx, group)
	})
	if err == nil {
		u.invalidate()
	}
	return err
}

// Update implements UserGroupStore.
//
// The group row is updated by name (the immutable identity), then the member
// set is replaced. RowsAffected == 0 means the name does not exist; that is
// reported as ErrNotFound instead of letting an update silently create a
// group nobody asked for.
func (u *userGroupStore) Update(ctx context.Context, group *model.UserGroupM) error {
	db := u.db(ctx)
	if db == nil {
		return errUserGroupStoreUnavailable
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var existing model.UserGroupM
		if err := tx.Where("name = ?", group.Name).First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return agentstore.ErrNotFound
			}
			return err
		}

		if err := tx.Model(&model.UserGroupM{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"display_name": group.DisplayName,
				"description":  group.Description,
				"enabled":      group.Enabled,
				"owner":        group.Owner,
				"updated_at":   time.Now(),
			}).Error; err != nil {
			return err
		}

		group.ID = existing.ID
		group.CreatedAt = existing.CreatedAt
		if err := tx.Where("group_id = ?", existing.ID).
			Delete(&model.UserGroupMemberM{}).Error; err != nil {
			return err
		}
		return u.insertMembers(tx, group)
	})
	if err == nil {
		u.invalidate()
	}
	return err
}

// insertMembers writes group.Members and stamps their identity columns.
func (u *userGroupStore) insertMembers(tx *gorm.DB, group *model.UserGroupM) error {
	now := time.Now()
	for i := range group.Members {
		member := &group.Members[i]
		if member.ID == "" {
			member.ID = uuid.NewString()
		}
		member.GroupID = group.ID
		if member.CreatedAt.IsZero() {
			member.CreatedAt = now
		}
		if err := tx.Create(member).Error; err != nil {
			return err
		}
	}
	return nil
}

// Delete implements UserGroupStore. Absence is not an error: a delete that is
// retried after a successful first attempt must converge, not fail.
func (u *userGroupStore) Delete(ctx context.Context, name string) error {
	db := u.db(ctx)
	if db == nil {
		return errUserGroupStoreUnavailable
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var group model.UserGroupM
		if err := tx.Where("name = ?", name).First(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Where("group_id = ?", group.ID).
			Delete(&model.UserGroupMemberM{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", group.ID).Delete(&model.UserGroupM{}).Error
	})
	if err == nil {
		u.invalidate()
	}
	return err
}

// Get implements UserGroupStore.
func (u *userGroupStore) Get(ctx context.Context, name string) (*model.UserGroupM, error) {
	db := u.db(ctx)
	if db == nil {
		return nil, agentstore.ErrNotFound
	}

	var group model.UserGroupM
	if err := db.Where("name = ?", name).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agentstore.ErrNotFound
		}
		return nil, err
	}
	members, err := u.members(ctx, []string{group.ID})
	if err != nil {
		return nil, err
	}
	group.Members = members[group.ID]
	if group.Members == nil {
		group.Members = []model.UserGroupMemberM{}
	}
	return &group, nil
}

// List implements UserGroupStore.
func (u *userGroupStore) List(
	ctx context.Context, opts agentstore.ListUserGroupsOptions,
) (int64, []*model.UserGroupM, error) {
	db := u.db(ctx)
	if db == nil {
		return 0, nil, errUserGroupStoreUnavailable
	}

	query := u.filtered(db, opts)
	var total int64
	if err := query.Model(&model.UserGroupM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	pageQuery := u.filtered(db, opts).
		Order("created_at DESC, id DESC")
	if opts.Limit > 0 {
		offset := opts.Offset
		if offset < 0 {
			offset = 0
		}
		pageQuery = pageQuery.Offset(offset).Limit(opts.Limit)
	}

	var groups []model.UserGroupM
	if err := pageQuery.Find(&groups).Error; err != nil {
		return 0, nil, err
	}
	if len(groups) == 0 {
		return total, []*model.UserGroupM{}, nil
	}

	ids := make([]string, 0, len(groups))
	for i := range groups {
		ids = append(ids, groups[i].ID)
	}
	members, err := u.members(ctx, ids)
	if err != nil {
		return 0, nil, err
	}
	out := make([]*model.UserGroupM, 0, len(groups))
	for i := range groups {
		group := groups[i]
		group.Members = members[group.ID]
		if group.Members == nil {
			group.Members = []model.UserGroupMemberM{}
		}
		out = append(out, &group)
	}
	return total, out, nil
}

// filtered applies the list filters. It is built twice (count and page) rather
// than reusing one *gorm.DB, because GORM mutates the statement in place.
func (u *userGroupStore) filtered(db *gorm.DB, opts agentstore.ListUserGroupsOptions) *gorm.DB {
	query := db.Model(&model.UserGroupM{})

	if keyword := strings.TrimSpace(opts.Keyword); keyword != "" {
		like := "%" + strings.ToLower(keyword) + "%"
		query = query.Where(
			"LOWER(name) LIKE ? OR LOWER(display_name) LIKE ? OR LOWER(description) LIKE ?",
			like, like, like,
		)
	}

	if member := strings.TrimSpace(opts.Member); member != "" {
		sub := db.Model(&model.UserGroupMemberM{}).
			Select("group_id").
			Where("user_id = ? OR username = ? OR lark_user_id = ? OR open_id = ?",
				member, member, member, member)
		query = query.Where("id IN (?)", sub)
	}

	return query
}

// members loads members for the given group IDs and groups them by group.
func (u *userGroupStore) members(
	ctx context.Context, groupIDs []string,
) (map[string][]model.UserGroupMemberM, error) {
	out := make(map[string][]model.UserGroupMemberM, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	db := u.db(ctx)
	if db == nil {
		return nil, errUserGroupStoreUnavailable
	}

	var rows []model.UserGroupMemberM
	if err := db.Where("group_id IN ?", groupIDs).
		Order("created_at ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].GroupID] = append(out[rows[i].GroupID], rows[i])
	}
	return out, nil
}

// GroupCodesFor implements UserGroupStore.
//
// The join is deliberate: filtering by a member alias in SQL keeps the cost of
// a single authorization check independent of how many groups exist. Only
// enabled groups contribute; a disabled group is the operator's way to pause a
// grant without deleting the membership list.
func (u *userGroupStore) GroupCodesFor(ctx context.Context, identity string) ([]string, error) {
	if identity == "" {
		return nil, nil
	}
	if codes, ok := u.cached(identity); ok {
		return codes, nil
	}

	db := u.db(ctx)
	if db == nil {
		return nil, errUserGroupStoreUnavailable
	}

	groupTable := model.TableNameUserGroupM()
	memberTable := model.TableNameUserGroupMemberM()

	var codes []string
	err := db.Table(fmt.Sprintf("%s AS g", groupTable)).
		Joins(fmt.Sprintf("JOIN %s AS m ON m.group_id = g.id", memberTable)).
		Where("g.enabled = ?", true).
		Where("m.user_id = ? OR m.username = ? OR m.lark_user_id = ? OR m.open_id = ?",
			identity, identity, identity, identity).
		Distinct().
		Order("g.name ASC").
		Pluck("g.name", &codes).Error
	if err != nil {
		return nil, err
	}
	u.remember(identity, codes)
	return codes, nil
}

// cached returns a copy of the cached answer when it is still fresh.
func (u *userGroupStore) cached(identity string) ([]string, bool) {
	u.mu.RLock()
	entry, ok := u.codes[identity]
	u.mu.RUnlock()
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return append([]string(nil), entry.codes...), true
}

// remember stores an answer, including an empty one. The map is cleared rather
// than evicted one entry at a time when it grows past the cap: at that size the
// working set is not a working set.
func (u *userGroupStore) remember(identity string, codes []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.codes) >= groupCodesCacheMax {
		u.codes = make(map[string]cachedGroupCodes)
	}
	u.codes[identity] = cachedGroupCodes{
		codes:   append([]string(nil), codes...),
		expires: time.Now().Add(groupCodesTTL),
	}
}
