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

// Package usergroup implements the console's user-group management.
//
// A group is a named set of people that a policy can name as
// `subject_type=group`. The group's name is the authorization identifier; the
// membership list only answers "which groups does this identity belong to".
// Keeping the two apart is what lets an operator add or remove a person
// without touching a single policy.
package usergroup

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

const (
	// MaxMembersPerGroup bounds one group. It is far above any real team and
	// low enough that a single group cannot turn into an unbounded table.
	MaxMembersPerGroup = 10000

	// MaxAliasesPerMember is the four identifiers a person may carry.
	MaxAliasesPerMember = 4
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Member is one person as the console edits them.
//
// Every field is an alias the authorization layer matches exactly; DisplayName
// and Email are presentation only.
type Member struct {
	UserID      string `json:"userID,omitempty"`
	Username    string `json:"username,omitempty"`
	LarkUserID  string `json:"larkUserID,omitempty"`
	OpenID      string `json:"openID,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
}

// aliases returns the non-empty matchable identifiers after trimming.
func (m Member) aliases() []string {
	out := make([]string, 0, MaxAliasesPerMember)
	for _, v := range []string{m.UserID, m.Username, m.LarkUserID, m.OpenID} {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// UserGroup is one group as the console reads it.
type UserGroup struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Owner       string    `json:"owner"`
	Members     []Member  `json:"members"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ListRequest narrows and pages the list.
type ListRequest struct {
	Offset  int
	Limit   int
	Member  string
	Keyword string
}

// ListResponse is the list envelope the console's resource API reads.
type ListResponse struct {
	UserGroups []*UserGroup `json:"userGroups"`
	Count      int64        `json:"count"`
}

// CreateRequest is the write shape shared by create and update.
//
// Enabled is a pointer so "omitted" can mean true on create while an explicit
// false still disables the group. Owner is stamped by the handler from the
// caller's claims, never by the browser.
type CreateRequest struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Enabled     *bool    `json:"enabled"`
	Owner       string   `json:"owner,omitempty"`
	Members     []Member `json:"members"`
}

// UpdateRequest is CreateRequest addressed by its immutable name.
type UpdateRequest = CreateRequest

// UserGroupBiz is the user-group management surface.
type UserGroupBiz interface {
	List(ctx context.Context, req *ListRequest) (*ListResponse, error)
	Get(ctx context.Context, name string) (*UserGroup, error)
	Create(ctx context.Context, req *CreateRequest) (*UserGroup, error)
	Update(ctx context.Context, req *UpdateRequest) (*UserGroup, error)
	Delete(ctx context.Context, name string) error
	// CodesFor returns the names of the enabled groups identity belongs to.
	// It is the read chat uses to turn a person into the group subjects its
	// role rules are written against.
	CodesFor(ctx context.Context, identity string) ([]string, error)
}

type userGroupBiz struct {
	// store is the aggregate so the accessor is resolved lazily. Eagerly
	// resolving UserGroups() at construction would call a method a test fake
	// that embeds a nil AIStore does not implement, panicking every handler test
	// that never touches user groups.
	store agentstore.AIStore
}

var _ UserGroupBiz = (*userGroupBiz)(nil)

// New creates the business logic over the aggregate store.
func New(store agentstore.AIStore) UserGroupBiz { return &userGroupBiz{store: store} }

// groups returns the group accessor, or nil when this deployment has none.
func (b *userGroupBiz) groups() agentstore.UserGroupStore {
	if b == nil || b.store == nil {
		return nil
	}
	return b.store.UserGroups()
}

// List implements UserGroupBiz.
func (b *userGroupBiz) List(ctx context.Context, req *ListRequest) (*ListResponse, error) {
	store := b.groups()
	if store == nil {
		return nil, errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	total, groups, err := store.List(ctx, agentstore.ListUserGroupsOptions{
		Offset:  req.Offset,
		Limit:   req.Limit,
		Member:  req.Member,
		Keyword: req.Keyword,
	})
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取用户组失败: %s", err.Error())
	}
	out := make([]*UserGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, viewOf(group))
	}
	return &ListResponse{UserGroups: out, Count: total}, nil
}

// Get implements UserGroupBiz.
func (b *userGroupBiz) Get(ctx context.Context, name string) (*UserGroup, error) {
	store := b.groups()
	if store == nil {
		return nil, errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	group, err := store.Get(ctx, name)
	if err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return nil, errno.ErrNotFound.WithMessage("用户组 %q 不存在", name)
		}
		return nil, errno.ErrDBRead.WithMessage("读取用户组失败: %s", err.Error())
	}
	return viewOf(group), nil
}

// Create implements UserGroupBiz.
func (b *userGroupBiz) Create(ctx context.Context, req *CreateRequest) (*UserGroup, error) {
	store := b.groups()
	if store == nil {
		return nil, errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	group, err := modelOf(req, enabled)
	if err != nil {
		return nil, err
	}
	if _, err := store.Get(ctx, group.Name); err == nil {
		return nil, errno.ErrAlreadyExists.WithMessage("用户组 %q 已存在", group.Name)
	} else if !errors.Is(err, agentstore.ErrNotFound) {
		return nil, errno.ErrDBRead.WithMessage("检查用户组失败: %s", err.Error())
	}
	if err := store.Create(ctx, group); err != nil {
		if isDuplicate(err) {
			return nil, errno.ErrAlreadyExists.WithMessage("用户组 %q 已存在", group.Name)
		}
		return nil, errno.ErrDBWrite.WithMessage("创建用户组失败: %s", err.Error())
	}
	return viewOf(group), nil
}

// Update implements UserGroupBiz.
func (b *userGroupBiz) Update(ctx context.Context, req *UpdateRequest) (*UserGroup, error) {
	store := b.groups()
	if store == nil {
		return nil, errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	// The existing row is read for two fields the request does not carry:
	// `enabled` when omitted, and `owner`. Owner is audit metadata; the
	// handler deliberately clears it on update so a save cannot transfer
	// ownership, and this is where the stored value is put back.
	existing, err := store.Get(ctx, strings.TrimSpace(req.Name))
	if err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return nil, errno.ErrNotFound.WithMessage("用户组 %q 不存在", req.Name)
		}
		return nil, errno.ErrDBRead.WithMessage("读取用户组失败: %s", err.Error())
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	group, err := modelOf(req, enabled)
	if err != nil {
		return nil, err
	}
	group.Owner = existing.Owner
	group.CreatedAt = existing.CreatedAt
	if err := store.Update(ctx, group); err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return nil, errno.ErrNotFound.WithMessage("用户组 %q 不存在", group.Name)
		}
		return nil, errno.ErrDBWrite.WithMessage("更新用户组失败: %s", err.Error())
	}
	// The store filled in the immutable identity fields from the existing row.
	return viewOf(group), nil
}

// Delete implements UserGroupBiz.
func (b *userGroupBiz) Delete(ctx context.Context, name string) error {
	store := b.groups()
	if store == nil {
		return errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	if err := store.Delete(ctx, name); err != nil {
		return errno.ErrDBWrite.WithMessage("删除用户组失败: %s", err.Error())
	}
	return nil
}

// CodesFor implements UserGroupBiz.
func (b *userGroupBiz) CodesFor(ctx context.Context, identity string) ([]string, error) {
	store := b.groups()
	if store == nil {
		return nil, errno.ErrNotConfigured.WithMessage("用户组存储未配置")
	}
	codes, err := store.GroupCodesFor(ctx, identity)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取用户组归属失败: %s", err.Error())
	}
	return codes, nil
}

// modelOf validates and converts one request.
func modelOf(req *CreateRequest, enabled bool) (*model.UserGroupM, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("缺少用户组名称")
	}
	if !nameRE.MatchString(name) {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"用户组名称只允许字母、数字、下划线、点和连字符，且以字母或数字开头，最长 64 位")
	}
	if utf8.RuneCountInString(req.DisplayName) > 128 {
		return nil, errno.ErrInvalidArgument.WithMessage("显示名最长 128 个字符")
	}
	if utf8.RuneCountInString(req.Description) > 512 {
		return nil, errno.ErrInvalidArgument.WithMessage("描述最长 512 个字符")
	}
	if len(req.Members) > MaxMembersPerGroup {
		return nil, errno.ErrInvalidArgument.WithMessage("单个用户组最多 %d 个成员", MaxMembersPerGroup)
	}

	members, err := membersOf(req.Members)
	if err != nil {
		return nil, err
	}

	return &model.UserGroupM{
		Name:        name,
		DisplayName: strings.TrimSpace(req.DisplayName),
		Description: strings.TrimSpace(req.Description),
		Enabled:     enabled,
		Owner:       req.Owner,
		Members:     members,
	}, nil
}

// membersOf validates the member list and converts it to rows.
//
// A person's aliases are a set. Repeating an alias across two members would
// make membership ambiguous (which row does a removal target?), so it is
// rejected rather than silently deduplicated.
func membersOf(in []Member) ([]model.UserGroupMemberM, error) {
	out := make([]model.UserGroupMemberM, 0, len(in))
	seen := make(map[string]int, len(in))

	for i, member := range in {
		aliases := member.aliases()
		if len(aliases) == 0 {
			return nil, errno.ErrInvalidArgument.WithMessage(
				"第 %d 个成员没有任何身份标识（账号 ID、登录名、飞书 user_id、open_id 至少填一个）", i+1)
		}
		if utf8.RuneCountInString(member.Email) > 255 {
			return nil, errno.ErrInvalidArgument.WithMessage("第 %d 个成员的邮箱过长", i+1)
		}
		for _, alias := range aliases {
			if utf8.RuneCountInString(alias) > 64 {
				return nil, errno.ErrInvalidArgument.WithMessage(
					"第 %d 个成员的身份标识过长（最长 64 个字符）", i+1)
			}
			// The same person may legitimately have the same value in two
			// alias fields (a local account's username often equals its ID).
			// Only two *different* members claiming one identifier is
			// ambiguous, because a removal would not know which row to target.
			if prev, ok := seen[alias]; ok && prev != i {
				return nil, errno.ErrInvalidArgument.WithMessage(
					"第 %d 个成员与第 %d 个成员使用了相同的身份标识 %q", i+1, prev+1, alias)
			}
			seen[alias] = i
		}
		out = append(out, model.UserGroupMemberM{
			UserID:      strings.TrimSpace(member.UserID),
			Username:    strings.TrimSpace(member.Username),
			LarkUserID:  strings.TrimSpace(member.LarkUserID),
			OpenID:      strings.TrimSpace(member.OpenID),
			DisplayName: strings.TrimSpace(member.DisplayName),
			Email:       strings.TrimSpace(member.Email),
		})
	}
	return out, nil
}

// viewOf converts a row to the console view.
func viewOf(group *model.UserGroupM) *UserGroup {
	if group == nil {
		return nil
	}
	members := make([]Member, 0, len(group.Members))
	for i := range group.Members {
		member := &group.Members[i]
		members = append(members, Member{
			UserID:      member.UserID,
			Username:    member.Username,
			LarkUserID:  member.LarkUserID,
			OpenID:      member.OpenID,
			DisplayName: member.DisplayName,
			Email:       member.Email,
		})
	}
	return &UserGroup{
		Name:        group.Name,
		DisplayName: group.DisplayName,
		Description: group.Description,
		Enabled:     group.Enabled,
		Owner:       group.Owner,
		Members:     members,
		CreatedAt:   group.CreatedAt,
		UpdatedAt:   group.UpdatedAt,
	}
}

// isDuplicate recognizes the unique-index violation both supported dialects
// report. The alternative — a pre-check only — leaves a race that surfaces as
// a 500 for a name that is in fact taken.
func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique violation")
}
