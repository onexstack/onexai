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

// Package user implements account administration and the console half of the
// Feishu binding handshake.
package user

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/password"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// DefaultBindTTL is how long a bind code minted by the console stays usable.
// It is shorter than the bot's claim TTL on purpose: a code that is already
// dead in the console must not still be writable into a claim.
const DefaultBindTTL = 10 * time.Minute

// UserBiz is the account-administration surface.
type UserBiz interface {
	List(ctx context.Context, req *ListRequest) (*ListResponse, error)
	Get(ctx context.Context, req *GetRequest) (*UserView, error)
	Create(ctx context.Context, req *CreateRequest) (*UserView, error)
	Update(ctx context.Context, req *UpdateRequest) (*UserView, error)
	Delete(ctx context.Context, req *DeleteRequest) error
	ResetPassword(ctx context.Context, req *ResetPasswordRequest) error

	FeishuBindStart(ctx context.Context, req *BindStartRequest) (*BindStartResponse, error)
	FeishuBindStatus(ctx context.Context, req *BindStatusRequest) (*BindStatusResponse, error)
	FeishuBindConfirm(ctx context.Context, req *BindConfirmRequest) (*BindConfirmResponse, error)
	FeishuUnbind(ctx context.Context, req *UnbindRequest) error
	FeishuBindings(ctx context.Context, req *BindingsRequest) (*BindingsResponse, error)

	// The peer-binding management surface. It is separate from the handshake
	// above: a claim proves who spoke in Feishu, while these read and write the
	// long-lived (lark, app, open_id) -> (account, agents) projection.
	ListFeishuBindings(ctx context.Context, req *ListFeishuBindingsRequest) (*ListFeishuBindingsResponse, error)
	GetFeishuBinding(ctx context.Context, req *GetFeishuBindingRequest) (*FeishuBindingItem, error)
	UpdateFeishuAgents(ctx context.Context, req *UpdateFeishuAgentsRequest) (*FeishuBindingItem, error)
	UnbindFeishuBinding(ctx context.Context, req *UnbindFeishuBindingRequest) error

	// AccountStatus is what the auth middleware asks on every request. known is
	// false for a token that names no account (legacy providers, dev bypass),
	// which the middleware treats as "nothing to revoke or recompute". role is
	// the account's current role, so a demotion takes effect on an already
	// issued token.
	AccountStatus(ctx context.Context, userID string) (known, active bool, role string, err error)
}

// ListRequest is one page of accounts.
type ListRequest struct {
	Page     int
	PageSize int
	Keyword  string
	Status   string
	Role     string
}

// GetRequest names one account by its stable ID.
type GetRequest struct {
	UserID string
}

// UserView is the account as the management page shows it.
type UserView struct {
	ID          string         `json:"id"`
	Username    string         `json:"username"`
	DisplayName string         `json:"display_name"`
	Email       string         `json:"email,omitempty"`
	Role        string         `json:"role,omitempty"`
	Status      string         `json:"status"`
	Provider    string         `json:"provider"`
	CreatedAt   time.Time      `json:"created_at"`
	LastLoginAt *time.Time     `json:"last_login_at,omitempty"`
	Feishu      *FeishuBinding `json:"feishu,omitempty"`
}

// FeishuBinding is the Feishu identity attached to an account.
type FeishuBinding struct {
	OpenID     string    `json:"open_id"`
	UnionID    string    `json:"union_id,omitempty"`
	LarkUserID string    `json:"lark_user_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	AvatarURL  string    `json:"avatar_url,omitempty"`
	BoundVia   string    `json:"bound_via,omitempty"`
	BoundAt    time.Time `json:"bound_at"`
}

// ListResponse is one page plus the total behind it.
type ListResponse struct {
	Total int64      `json:"total"`
	Users []UserView `json:"users"`
}

// CreateRequest is an administrator creating a local account.
type CreateRequest struct {
	ActorID     string
	Username    string
	Password    string
	DisplayName string
	Role        string
	Status      string
}

// DeleteRequest removes an account.
type DeleteRequest struct {
	ActorID string
	UserID  string
}

// UpdateRequest changes the fields an administrator may change.
type UpdateRequest struct {
	ActorID     string
	UserID      string
	DisplayName *string
	Role        *string
	Status      *string
}

// ResetPasswordRequest sets a new password without knowing the old one.
type ResetPasswordRequest struct {
	ActorID     string
	UserID      string
	NewPassword string
}

// BindStartRequest starts the /bind-onex handshake for the signed-in account.
type BindStartRequest struct {
	UserID string
}

// BindStartResponse carries the code and the exact command to send to the bot.
type BindStartResponse struct {
	Code      string    `json:"code"`
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
}

// BindStatusRequest polls a code.
type BindStatusRequest struct {
	UserID string
	Code   string
}

// BindStatusResponse mirrors the four states the console draws.
type BindStatusResponse struct {
	Bound           bool `json:"bound"`
	Expired         bool `json:"expired"`
	AwaitingConfirm bool `json:"awaiting_confirm"`
}

// BindConfirmRequest completes the handshake with the bot's confirmation code.
type BindConfirmRequest struct {
	UserID  string
	Code    string
	Confirm string
}

// BindConfirmResponse reports completion.
type BindConfirmResponse struct {
	Bound bool `json:"bound"`
}

// UnbindRequest removes the signed-in account's Feishu binding.
type UnbindRequest struct {
	UserID string
}

// BindingsRequest lists the signed-in account's bindings.
type BindingsRequest struct {
	UserID string
}

// BindingsResponse is the 0-or-1 binding list.
type BindingsResponse struct {
	Bindings []FeishuBinding `json:"bindings"`
}

// Peer-binding states. They are wire values: the console renders each one
// differently, so renaming one is a breaking change.
const (
	// PeerStateBound: the account binding and the bot's projection agree.
	PeerStateBound = "bound"
	// PeerStateAccountOnly: the account is bound but no peer row exists. The
	// bot cannot resolve @peer until the projection catches up.
	PeerStateAccountOnly = "account_only"
	// PeerStatePeerOnly: the bot has a projection with no account binding
	// (typically pushed by an external identity system).
	PeerStatePeerOnly = "peer_only"
	// PeerStateMismatch: both exist but name different accounts.
	PeerStateMismatch = "mismatch"
)

// Why a row cannot be edited. The console shows these as a reason rather than
// guessing from a disabled button.
const (
	PeerReadonlyExternal      = "external"
	PeerReadonlyNoPeer        = "no_peer"
	PeerReadonlyMismatch      = "canonical_mismatch"
	PeerReadonlyUnknownSource = "unknown_source"
	PeerReadonlyNoCanonical   = "canonical_missing"
	PeerReadonlyNotOwner      = "not_owner"
)

// maxPeerAgents is the product limit of one Feishu identity's agent list: one
// identity resolves to one agent. Zero is still valid and means "bound but
// @peer disabled". The limit applies to the console's UpdateFeishuAgents only;
// the CRD and PutPeerAgent stay list-valued for onex-iam.
const maxPeerAgents = 1

// AgentCatalog is the directory the bot resolves @peer against. The chat biz
// already implements it; depending on the narrow interface keeps this package
// from reaching into the agent client.
type AgentCatalog interface {
	ListAgent(ctx context.Context) (*api.ListAgentResponse, error)
}

// ListFeishuBindingsRequest lists the peer bindings visible to the caller.
type ListFeishuBindingsRequest struct {
	ActorID  string
	IsAdmin  bool
	Page     int
	PageSize int
	Keyword  string
	Source   string
	// UserID narrows the list to one platform account. It is administrator-only;
	// a non-administrator asking for someone else is refused, not silently
	// narrowed, so a mistaken filter cannot look like an empty result.
	UserID string
}

// GetFeishuBindingRequest names one binding by the bot's peer id.
type GetFeishuBindingRequest struct {
	ActorID string
	IsAdmin bool
	PeerID  string
}

// UpdateFeishuAgentsRequest overwrites the single agent (0 or 1 names) of one
// binding.
type UpdateFeishuAgentsRequest struct {
	ActorID string
	IsAdmin bool
	PeerID  string
	Agents  []string
}

// UnbindFeishuBindingRequest removes one binding.
type UnbindFeishuBindingRequest struct {
	ActorID string
	IsAdmin bool
	PeerID  string
}

// FeishuBindingAccount is the platform account a binding resolves to.
type FeishuBindingAccount struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// FeishuBindingItem is one row of the console's binding view.
//
// Agents is always an array: an empty list means "bound but nothing usable",
// which is a different state from a missing peer row, and the console draws
// them differently.
type FeishuBindingItem struct {
	Backend   string     `json:"backend"`
	Scope     string     `json:"scope"`
	PeerID    string     `json:"peer_id"`
	Canonical string     `json:"canonical,omitempty"`
	Agents    []string   `json:"agents"`
	Source    string     `json:"source,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`

	Account *FeishuBindingAccount `json:"account,omitempty"`
	Feishu  *FeishuBinding        `json:"feishu,omitempty"`

	State          string `json:"state"`
	Writable       bool   `json:"writable"`
	CanUnbind      bool   `json:"can_unbind"`
	ReadonlyReason string `json:"readonly_reason,omitempty"`
}

// ListFeishuBindingsResponse is one page of the binding view.
type ListFeishuBindingsResponse struct {
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	Bindings []FeishuBindingItem `json:"bindings"`
}

// userBiz implements UserBiz over the chat store.
type userBiz struct {
	st      store.IStore
	appID   string
	bindTTL time.Duration
	catalog AgentCatalog
	// profile is optional: without it a binding is still written, just without
	// the contact fields.
	profile LarkProfileResolver
}

var _ UserBiz = (*userBiz)(nil)

// New builds the user biz. appID is the Lark application scope of a peer
// binding; it must be the same value the bot writes into chat.peer_bind_claim.
func New(st store.IStore, appID string, bindTTL time.Duration) *userBiz {
	return NewWithCatalog(st, appID, bindTTL, nil)
}

// NewWithCatalog builds the user biz with an agent directory. The console
// validates the names it writes against the same directory the bot resolves
// @peer against, so a typo is refused where it is made rather than silently
// producing a binding the bot cannot use.
func NewWithCatalog(
	st store.IStore, appID string, bindTTL time.Duration, catalog AgentCatalog,
) *userBiz {
	if bindTTL <= 0 {
		bindTTL = DefaultBindTTL
	}
	return &userBiz{st: st, appID: appID, bindTTL: bindTTL, catalog: catalog}
}

func (b *userBiz) users() store.UserStore {
	if b == nil || b.st == nil {
		return nil
	}
	return b.st.Users()
}

// List implements UserBiz.
func (b *userBiz) List(ctx context.Context, req *ListRequest) (*ListResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	page, size := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	total, rows, err := users.ListPage(ctx, page, size, strings.TrimSpace(req.Keyword), req.Status, req.Role)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取账号列表失败: %s", err.Error())
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	bindings, err := users.LarkBindingsByUserIDs(ctx, ids)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	byUser := make(map[string]*model.UserLarkBindingM, len(bindings))
	for _, binding := range bindings {
		byUser[binding.UserID] = binding
	}

	out := make([]UserView, 0, len(rows))
	for _, row := range rows {
		view := viewOf(row)
		if binding := byUser[row.ID]; binding != nil {
			view.Feishu = bindingOf(binding)
		}
		out = append(out, view)
	}
	return &ListResponse{Total: total, Users: out}, nil
}

// Get implements UserBiz.
//
// The console's user detail page is addressable by ID so a refresh or a copied
// link works without carrying the list row in navigation state. Feishu is
// attached the same way List does it, so the detail page shows the same
// binding as the list row.
func (b *userBiz) Get(ctx context.Context, req *GetRequest) (*UserView, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	id := strings.TrimSpace(req.UserID)
	if id == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("账号 ID 不能为空")
	}
	user, err := users.GetByID(ctx, id)
	if err != nil || user == nil {
		return nil, errno.ErrNotFound.WithMessage("账号不存在")
	}
	view := viewOf(user)
	if binding, _ := users.LarkBinding(ctx, user.ID); binding != nil {
		view.Feishu = bindingOf(binding)
	}
	return &view, nil
}

// Create implements UserBiz.
//
// The account is a local one: it gets a bcrypt hash immediately, so it can log
// in through /v1/auth/login with the username and password given here. The
// shape rules are the same ones registration uses — one rule, whether the
// account is created by its owner or by an administrator.
func (b *userBiz) Create(ctx context.Context, req *CreateRequest) (*UserView, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	username := strings.TrimSpace(req.Username)
	if err := password.ValidateUsername(username); err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	role := strings.TrimSpace(req.Role)
	switch role {
	case "", model.UserRoleAdmin, model.UserRoleSRE:
	default:
		return nil, errno.ErrInvalidArgument.WithMessage("角色只能是空、admin 或 sre")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = model.UserStatusActive
	}
	if status != model.UserStatusActive && status != model.UserStatusDisabled {
		return nil, errno.ErrInvalidArgument.WithMessage("状态只能是 active 或 disabled")
	}

	hash, err := password.Hash(req.Password)
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}

	if existing, err := users.GetByUsername(ctx, username); err == nil && existing != nil {
		return nil, errno.ErrAlreadyExists.WithMessage("用户名已存在")
	} else if err != nil && !errors.Is(err, store.ErrRecordNotFound) {
		return nil, errno.ErrDBRead.WithMessage("检查用户名失败: %s", err.Error())
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if len([]rune(displayName)) > 32 {
		displayName = string([]rune(displayName)[:32])
	}
	now := time.Now()
	user := &model.UserM{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: hash,
		DisplayName:  displayName,
		Role:         role,
		Status:       status,
		Provider:     "local",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := users.Create(ctx, user); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("创建账号失败: %s", err.Error())
	}
	view := viewOf(user)
	return &view, nil
}

// Delete implements UserBiz.
//
// Deleting an account takes its Feishu binding with it: the binding's open_id
// would otherwise keep blocking the same Feishu identity from being attached to
// a fresh account. The bot's peer projection is removed outside the transaction
// (it may live in the control plane) and only while it still names this account.
// Personal access keys live in the agent service's schema (see internal/agent),
// so they are not reached from this transaction.
func (b *userBiz) Delete(ctx context.Context, req *DeleteRequest) error {
	users := b.users()
	if users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	if req.UserID == "" {
		return errno.ErrInvalidArgument.WithMessage("缺少账号 ID")
	}
	if req.UserID == req.ActorID {
		return errno.ErrPermissionDenied.WithMessage("不能删除当前登录的账号")
	}
	user, err := users.GetByID(ctx, req.UserID)
	if err != nil || user == nil {
		return errno.ErrNotFound.WithMessage("账号不存在")
	}
	if user.Role == model.UserRoleAdmin {
		admins, err := users.Count(ctx, model.UserRoleAdmin)
		if err != nil {
			return errno.ErrDBRead.WithMessage("读取管理员数量失败: %s", err.Error())
		}
		if admins <= 1 {
			return errno.ErrPermissionDenied.WithMessage("至少需要保留一个管理员")
		}
	}

	// Read the binding before the transaction: after it commits, this is the
	// only way to know which peer row the account used to own.
	binding, err := users.LarkBinding(ctx, req.UserID)
	if err != nil {
		return errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}

	if err := b.st.TX(ctx, func(ctx context.Context) error {
		if err := users.DeleteLarkBinding(ctx, req.UserID); err != nil {
			return err
		}
		if err := users.DeleteBindCodes(ctx, req.UserID); err != nil {
			return err
		}
		return users.Delete(ctx, where.F("id", req.UserID))
	}); err != nil {
		return errno.ErrDBWrite.WithMessage("删除账号失败: %s", err.Error())
	}

	// The bot's projection lives outside the transaction (and may live in the
	// control plane), so it is cleaned up after the account row is gone. Only a
	// row that still names this account is removed: a concurrent rebind by a
	// different account must not be deleted by this.
	if binding != nil && b.st != nil && b.st.PeerAgents() != nil {
		peers := b.st.PeerAgents()
		row, err := peers.GetByPeer(ctx, "lark", b.appID, binding.OpenID)
		if err != nil {
			slog.Error("user: failed to read the peer binding after deleting an account",
				"user_id", req.UserID, "peer_id", binding.OpenID, "error", err)
		} else if row != nil && row.Canonical == req.UserID {
			if err := peers.DeleteByPeer(ctx, "lark", b.appID, binding.OpenID); err != nil {
				slog.Error("user: failed to remove the peer binding after deleting an account",
					"user_id", req.UserID, "peer_id", binding.OpenID, "error", err)
			}
		}
	}
	return nil
}

// Update implements UserBiz.
func (b *userBiz) Update(ctx context.Context, req *UpdateRequest) (*UserView, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	user, err := users.GetByID(ctx, req.UserID)
	if err != nil || user == nil {
		return nil, errno.ErrNotFound.WithMessage("账号不存在")
	}

	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if len([]rune(name)) > 32 {
			name = string([]rune(name)[:32])
		}
		user.DisplayName = name
	}
	if req.Role != nil {
		role := strings.TrimSpace(*req.Role)
		switch role {
		case "", model.UserRoleAdmin, model.UserRoleSRE:
		default:
			return nil, errno.ErrInvalidArgument.WithMessage("角色只能是空、admin 或 sre")
		}
		if req.UserID == req.ActorID && role != user.Role {
			return nil, errno.ErrPermissionDenied.WithMessage("不能修改自己的角色")
		}
		// Keep at least one account-level administrator. This counts only
		// chat.user roles, not the control plane's grants; a deployment whose
		// admins come from there is not what this guard protects, and those
		// admins can always restore a role.
		if user.Role == model.UserRoleAdmin && role != model.UserRoleAdmin {
			admins, err := users.Count(ctx, model.UserRoleAdmin)
			if err != nil {
				return nil, errno.ErrDBRead.WithMessage("读取管理员数量失败: %s", err.Error())
			}
			if admins <= 1 {
				return nil, errno.ErrPermissionDenied.WithMessage("至少需要保留一个管理员")
			}
		}
		user.Role = role
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if status != model.UserStatusActive && status != model.UserStatusDisabled {
			return nil, errno.ErrInvalidArgument.WithMessage("状态只能是 active 或 disabled")
		}
		if req.UserID == req.ActorID && status == model.UserStatusDisabled {
			return nil, errno.ErrPermissionDenied.WithMessage("不能禁用当前登录的账号")
		}
		user.Status = status
	}

	user.UpdatedAt = time.Now()
	if err := users.Update(ctx, user); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("保存账号失败: %s", err.Error())
	}
	view := viewOf(user)
	if binding, _ := users.LarkBinding(ctx, user.ID); binding != nil {
		view.Feishu = bindingOf(binding)
	}
	return &view, nil
}

// ResetPassword implements UserBiz.
func (b *userBiz) ResetPassword(ctx context.Context, req *ResetPasswordRequest) error {
	users := b.users()
	if users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	user, err := users.GetByID(ctx, req.UserID)
	if err != nil || user == nil {
		return errno.ErrNotFound.WithMessage("账号不存在")
	}
	hash, err := password.Hash(req.NewPassword)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	user.PasswordHash = hash
	user.UpdatedAt = time.Now()
	if err := users.Update(ctx, user); err != nil {
		return errno.ErrDBWrite.WithMessage("保存密码失败: %s", err.Error())
	}
	return nil
}

// FeishuBindStart implements UserBiz.
func (b *userBiz) FeishuBindStart(ctx context.Context, req *BindStartRequest) (*BindStartResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	if existing, err := users.LarkBinding(ctx, req.UserID); err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	} else if existing != nil {
		return nil, errno.ErrAlreadyExists.WithMessage("本账号已绑定飞书，请先解绑")
	}
	if b.appID == "" {
		return nil, errno.ErrNotConfigured.WithMessage("本部署未配置飞书应用")
	}

	// One live code per account: minting a new one abandons the old rather than
	// leaving two commands whose claims the user might confuse.
	if err := users.DeleteBindCodes(ctx, req.UserID); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("清理旧绑定码失败: %s", err.Error())
	}

	code, err := store.NewBindCode()
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("%s", err.Error())
	}
	record := &model.UserBindCodeM{
		Code:      code,
		UserID:    req.UserID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(b.bindTTL),
	}
	if err := users.CreateBindCode(ctx, record); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("保存绑定码失败: %s", err.Error())
	}
	return &BindStartResponse{
		Code:      code,
		Command:   "/bind-onex " + code,
		ExpiresAt: record.ExpiresAt,
	}, nil
}

// FeishuBindStatus implements UserBiz.
func (b *userBiz) FeishuBindStatus(
	ctx context.Context, req *BindStatusRequest,
) (*BindStatusResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	if binding, err := users.LarkBinding(ctx, req.UserID); err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	} else if binding != nil {
		return &BindStatusResponse{Bound: true}, nil
	}

	record, err := users.GetLiveBindCode(ctx, req.Code, req.UserID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取绑定码失败: %s", err.Error())
	}
	if record == nil {
		return &BindStatusResponse{Expired: true}, nil
	}

	claims := b.st.PeerBindClaims()
	if claims == nil {
		return &BindStatusResponse{}, nil
	}
	claim, err := claims.GetLive(ctx, req.Code)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书确认状态失败: %s", err.Error())
	}
	return &BindStatusResponse{
		AwaitingConfirm: claim != nil && claim.PeerID != "",
	}, nil
}

// FeishuBindConfirm implements UserBiz.
//
// # The confirmation code is compared, not trusted
//
// The bot writes a digest of the code it spoke into the claim row. The account
// holder types that code here, and this is the only place the two halves meet:
// the console proves it holds the account session, the typed code proves the
// caller is the person who spoke in Feishu. Either alone binds nothing.
func (b *userBiz) FeishuBindConfirm(
	ctx context.Context, req *BindConfirmRequest,
) (*BindConfirmResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	record, err := users.GetLiveBindCode(ctx, req.Code, req.UserID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取绑定码失败: %s", err.Error())
	}
	if record == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("绑定码已失效，请重新获取")
	}

	claims := b.st.PeerBindClaims()
	if claims == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有飞书绑定握手存储")
	}
	claim, err := claims.GetLive(ctx, req.Code)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书确认状态失败: %s", err.Error())
	}
	if claim == nil || claim.PeerID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("还没有在飞书中发送绑定码")
	}
	// The claim must have been written by this deployment's Lark app. The bot
	// writes exactly this pair; anything else is a row whose identity belongs
	// to a different integration and must not be bound here.
	if claim.Backend != "lark" || claim.Scope != b.appID {
		return nil, errno.ErrInvalidArgument.WithMessage("绑定码不属于当前飞书应用")
	}
	if claim.ConfirmHash == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("绑定码没有确认码，请回飞书重新发送 /bind-onex")
	}

	got := store.HashBindConfirm(strings.TrimSpace(req.Confirm))
	if subtle.ConstantTimeCompare([]byte(got), []byte(claim.ConfirmHash)) != 1 {
		return nil, errno.ErrInvalidArgument.WithMessage("确认码不对，请查看机器人的最新回复")
	}

	if other, err := users.LarkBindingByOpenID(ctx, claim.PeerID); err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	} else if other != nil && other.UserID != req.UserID {
		return nil, errno.ErrAlreadyExists.WithMessage("该飞书已绑定其他账号，请先在那边解绑")
	}
	if mine, err := users.LarkBinding(ctx, req.UserID); err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	} else if mine != nil {
		return nil, errno.ErrAlreadyExists.WithMessage("本账号已绑定飞书，请先解绑")
	}

	// The peer binding is checked before the account binding is written: if the
	// identity belongs to another platform account, refusing first leaves both
	// tables as they were.
	peers := b.st.PeerAgents()
	var existing *model.PeerAgentM
	if peers != nil {
		existing, err = peers.GetByPeer(ctx, "lark", b.appID, claim.PeerID)
		if err != nil {
			return nil, errno.ErrDBRead.WithMessage("读取飞书身份绑定失败: %s", err.Error())
		}
		if existing != nil && existing.Canonical != "" && existing.Canonical != req.UserID {
			// A canonical id that names no account is a stale row: the account
			// it was written for has been deleted, or it came from an identity
			// system this deployment no longer has. Refusing here would leave
			// the Feishu identity permanently un-bindable, with no account left
			// that could unbind it. The caller has just proved it holds that
			// identity (the confirmation code came back from the speaker), so
			// it may adopt the row. A canonical that still names a live account
			// stays a conflict.
			owner, ownerErr := users.GetByID(ctx, existing.Canonical)
			if ownerErr != nil && !errors.Is(ownerErr, store.ErrRecordNotFound) {
				return nil, errno.ErrDBRead.WithMessage("读取飞书身份所属账号失败: %s", ownerErr.Error())
			}
			if owner != nil {
				return nil, errno.ErrAlreadyExists.WithMessage("该飞书身份已绑定其他账号")
			}
			slog.Info("user: adopting a stale peer binding for a Feishu identity",
				"user_id", req.UserID,
				"peer_id", claim.PeerID,
				"stale_canonical", existing.Canonical,
				"stale_source", existing.Source,
			)
		}
	}

	now := time.Now()
	binding := &model.UserLarkBindingM{
		UserID:    req.UserID,
		OpenID:    claim.PeerID,
		BoundVia:  "code",
		BoundAt:   now,
		UpdatedAt: now,
	}
	// The handshake only proves who spoke; the contact record behind the
	// open_id carries the tenant user_id and the display name the console
	// shows. Resolve it before the row is written so the first read after
	// binding already has both. A failure must not refuse the binding.
	if b.profile != nil {
		profile, profileErr := b.resolveProfile(ctx, claim.PeerID)
		if profileErr != nil {
			slog.WarnContext(ctx, "user: Feishu profile lookup failed; the binding has no lark_user_id",
				"user_id", req.UserID, "open_id", claim.PeerID, "error", profileErr)
		} else {
			binding.LarkUserID = profile.LarkUserID
			binding.UnionID = profile.UnionID
			binding.Name = profile.Name
			binding.AvatarURL = profile.AvatarURL
		}
	}
	if err := users.PutLarkBinding(ctx, binding); err != nil {
		if store.IsDuplicateError(err) {
			return nil, errno.ErrAlreadyExists.WithMessage("该飞书已绑定其他账号")
		}
		return nil, errno.ErrDBWrite.WithMessage("保存飞书绑定失败: %s", err.Error())
	}

	// The code and the claim are both single-use: consuming them is what makes
	// the second confirmation attempt say "expired" instead of binding again.
	_ = users.DeleteBindCode(ctx, req.Code)
	_ = claims.Delete(ctx, where.F("code", req.Code))

	// The bot reads chat.peer_agent to decide whose assistant a speaker runs.
	// Writing the same triple is what makes the binding visible outside the
	// console. It is best-effort after the account binding: the account is
	// already bound, and failing the whole request would make a retry answer
	// "already bound" without saying what actually went wrong.
	if peers != nil {
		row := &model.PeerAgentM{
			Backend:   "lark",
			Scope:     b.appID,
			PeerID:    claim.PeerID,
			Canonical: req.UserID,
			Agents:    "[]",
			Source:    "console",
			UpdatedAt: now,
		}
		// Source names the last writer, and that is the console now. Keeping an
		// older writer's source would make FeishuUnbind refuse to remove a row
		// the console owns, leaving the identity authorized after its owner
		// unbinds. Agents stay: they belong to the Feishu identity, not to the
		// account that held it before.
		if existing != nil && existing.Agents != "" {
			row.Agents = existing.Agents
		}
		if err := peers.Put(ctx, row); err != nil {
			slog.Error("user: failed to write the peer binding after a Feishu bind",
				"user_id", req.UserID, "peer_id", claim.PeerID, "error", err)
		}
	}

	return &BindConfirmResponse{Bound: true}, nil
}

// FeishuUnbind implements UserBiz.
//
// The signed-in account's own path: it only ever removes its own binding, so
// the ownership checks the management surface needs have nothing to decide
// here. The body is shared with UnbindFeishuBinding so the two cannot drift.
func (b *userBiz) FeishuUnbind(ctx context.Context, req *UnbindRequest) error {
	users := b.users()
	if users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	binding, err := users.LarkBinding(ctx, req.UserID)
	if err != nil {
		return errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	if binding == nil {
		return nil
	}
	return b.unbindFeishu(ctx, req.UserID, binding.OpenID)
}

// unbindFeishu removes an account's binding and, when the console owns it, the
// bot's projection for that open_id.
func (b *userBiz) unbindFeishu(ctx context.Context, userID, openID string) error {
	users := b.users()
	if users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	user, err := users.GetByID(ctx, userID)
	if err != nil || user == nil {
		return errno.ErrNotFound.WithMessage("账号不存在")
	}
	// Unbinding the only way into an account would lock its owner out. A
	// password is what makes the account reachable afterwards; an
	// administrator's reset also counts, which is why the message names it.
	if user.PasswordHash == "" {
		return errno.ErrPermissionDenied.WithMessage(
			"请先设置登录密码，再解绑飞书，否则将无法登录；也可以联系管理员重置密码")
	}

	if err := users.DeleteLarkBinding(ctx, userID); err != nil {
		return errno.ErrDBWrite.WithMessage("解绑飞书失败: %s", err.Error())
	}
	_ = users.DeleteBindCodes(ctx, userID)

	if b.st != nil && b.st.PeerAgents() != nil {
		row, err := b.st.PeerAgents().GetByPeer(ctx, "lark", b.appID, openID)
		if err == nil && row != nil && row.Canonical == userID && row.Source == "console" {
			if err := b.st.PeerAgents().DeleteByPeer(ctx, "lark", b.appID, openID); err != nil {
				slog.Error("user: failed to remove the peer binding after a Feishu unbind",
					"user_id", userID, "peer_id", openID, "error", err)
			}
		}
	}
	return nil
}

// FeishuBindings implements UserBiz.
func (b *userBiz) FeishuBindings(
	ctx context.Context, req *BindingsRequest,
) (*BindingsResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	binding, err := users.LarkBinding(ctx, req.UserID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	out := &BindingsResponse{Bindings: []FeishuBinding{}}
	if binding != nil {
		out.Bindings = append(out.Bindings, *bindingOf(binding))
	}
	return out, nil
}

// ListFeishuBindings implements UserBiz.
//
// The view joins two stores that disagree on their key: account bindings live
// in SQL keyed by user_id, peer projections live in the control plane keyed by
// (backend, scope, open_id). Joining in memory is what lets a row that exists
// on only one side be shown as such instead of disappearing.
func (b *userBiz) ListFeishuBindings(
	ctx context.Context, req *ListFeishuBindingsRequest,
) (*ListFeishuBindingsResponse, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	page, size := normalizePeerPage(req.Page, req.PageSize)
	source := strings.TrimSpace(req.Source)
	switch source {
	case "", "console", "onex-iam", "none":
	default:
		return nil, errno.ErrInvalidArgument.WithMessage("来源只能是 console、onex-iam 或 none")
	}

	targetUserID := strings.TrimSpace(req.UserID)
	if !req.IsAdmin {
		if targetUserID != "" && targetUserID != req.ActorID {
			return nil, errno.ErrPermissionDenied.WithMessage("只能查看自己的飞书绑定")
		}
		targetUserID = req.ActorID
	}

	var (
		bindings []*model.UserLarkBindingM
		err      error
	)
	if targetUserID != "" {
		var one *model.UserLarkBindingM
		one, err = users.LarkBinding(ctx, targetUserID)
		if err == nil && one != nil {
			bindings = []*model.UserLarkBindingM{one}
		}
	} else {
		bindings, err = users.ListLarkBindings(ctx)
	}
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}

	peers, err := b.listPeerBindings(ctx)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书身份绑定失败: %s", err.Error())
	}

	byPeerID := make(map[string]*model.PeerAgentM, len(peers))
	for _, row := range peers {
		byPeerID[row.PeerID] = row
	}

	items := make([]FeishuBindingItem, 0, len(bindings)+len(peers))
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		seen[binding.OpenID] = struct{}{}
		items = append(items, b.buildFeishuItem(
			req.ActorID, req.IsAdmin, binding, byPeerID[binding.OpenID]))
	}
	for _, row := range peers {
		if _, ok := seen[row.PeerID]; ok {
			continue
		}
		// A user_id filter narrows the peer-only half too; without this an
		// administrator filtering to one account would still see every orphan.
		if targetUserID != "" && row.Canonical != targetUserID {
			continue
		}
		// A peer row that names nobody the caller is allowed to see stays out of
		// a non-administrator's list; for an administrator it is exactly the
		// orphan the view exists to surface.
		if !req.IsAdmin && row.Canonical != req.ActorID {
			continue
		}
		items = append(items, b.buildFeishuItem(req.ActorID, req.IsAdmin, nil, row))
	}

	if err := attachPeerAccounts(ctx, users, items); err != nil {
		return nil, err
	}
	markAdminUnbindableOrphans(req.IsAdmin, items)

	keyword := strings.ToLower(strings.TrimSpace(req.Keyword))
	filtered := make([]FeishuBindingItem, 0, len(items))
	for _, item := range items {
		if !peerSourceMatches(item, source) {
			continue
		}
		if keyword != "" && !peerMatchesKeyword(item, keyword) {
			continue
		}
		filtered = append(filtered, item)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return peerUpdatedAfter(filtered[i], filtered[j])
	})

	total := int64(len(filtered))
	start := (page - 1) * size
	if start >= len(filtered) {
		filtered = []FeishuBindingItem{}
	} else {
		filtered = filtered[start:]
	}
	if len(filtered) > size {
		filtered = filtered[:size]
	}
	return &ListFeishuBindingsResponse{
		Total:    total,
		Page:     page,
		PageSize: size,
		Bindings: filtered,
	}, nil
}

// GetFeishuBinding implements UserBiz.
func (b *userBiz) GetFeishuBinding(
	ctx context.Context, req *GetFeishuBindingRequest,
) (*FeishuBindingItem, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	peerID := strings.TrimSpace(req.PeerID)
	if peerID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("缺少飞书身份标识")
	}
	binding, err := users.LarkBindingByOpenID(ctx, peerID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	peer, err := b.getPeerBinding(ctx, peerID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书身份绑定失败: %s", err.Error())
	}
	if binding == nil && peer == nil {
		return nil, errno.ErrNotFound.WithMessage("没有找到该飞书绑定")
	}
	// A row the caller may not see is reported as absent: "forbidden" would
	// confirm that this open_id is bound to somebody.
	if !req.IsAdmin && !peerVisible(req.ActorID, binding, peer) {
		return nil, errno.ErrNotFound.WithMessage("没有找到该飞书绑定")
	}
	item := b.buildFeishuItem(req.ActorID, req.IsAdmin, binding, peer)
	items := []FeishuBindingItem{item}
	if err := attachPeerAccounts(ctx, users, items); err != nil {
		return nil, err
	}
	markAdminUnbindableOrphans(req.IsAdmin, items)
	return &items[0], nil
}

// UpdateFeishuAgents implements UserBiz.
//
// Only the console's own rows are writable. A row written by onex-iam is that
// system's statement of what the identity may use; letting the console rewrite
// it would make two authorities disagree with no way to tell which one won.
func (b *userBiz) UpdateFeishuAgents(
	ctx context.Context, req *UpdateFeishuAgentsRequest,
) (*FeishuBindingItem, error) {
	users := b.users()
	if users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	peerID := strings.TrimSpace(req.PeerID)
	if peerID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("缺少飞书身份标识")
	}
	if b.appID == "" {
		return nil, errno.ErrNotConfigured.WithMessage("本部署未配置飞书应用")
	}
	peer, err := b.getPeerBinding(ctx, peerID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书身份绑定失败: %s", err.Error())
	}
	if peer == nil {
		return nil, errno.ErrNotFound.WithMessage("该飞书身份还没有机器人侧的绑定")
	}
	binding, err := users.LarkBindingByOpenID(ctx, peerID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	if ok, reason := peerEditable(req.ActorID, req.IsAdmin, binding, peer); !ok {
		return nil, peerEditError(reason)
	}

	normalized, err := b.validateAgentNames(ctx, req.Agents, peer.AgentNames())
	if err != nil {
		return nil, err
	}

	peer.Agents = model.EncodeNameList(normalized)
	peer.Source = "console"
	peer.UpdatedAt = time.Now()
	peers := b.st.PeerAgents()
	if err := peers.Put(ctx, peer); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("保存智能体绑定失败: %s", err.Error())
	}

	// Built from the row just written rather than read back: the control-plane
	// reader is an informer cache and may still hold the previous value.
	item := b.buildFeishuItem(req.ActorID, req.IsAdmin, binding, peer)
	items := []FeishuBindingItem{item}
	if err := attachPeerAccounts(ctx, users, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// UnbindFeishuBinding implements UserBiz.
//
// The administrator path shares the account-page body; what it adds is the
// ownership decision and the refusal to half-unbind a row another system owns.
func (b *userBiz) UnbindFeishuBinding(
	ctx context.Context, req *UnbindFeishuBindingRequest,
) error {
	users := b.users()
	if users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	peerID := strings.TrimSpace(req.PeerID)
	if peerID == "" {
		return errno.ErrInvalidArgument.WithMessage("缺少飞书身份标识")
	}
	binding, err := users.LarkBindingByOpenID(ctx, peerID)
	if err != nil {
		return errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}
	peer, err := b.getPeerBinding(ctx, peerID)
	if err != nil {
		return errno.ErrDBRead.WithMessage("读取飞书身份绑定失败: %s", err.Error())
	}
	if binding == nil && peer == nil {
		return nil
	}
	if !req.IsAdmin && !peerVisible(req.ActorID, binding, peer) {
		return errno.ErrNotFound.WithMessage("没有找到该飞书绑定")
	}

	// A row whose canonical names no account is an orphan: the account it
	// authorized was deleted, or a migration carried an id this deployment never
	// had. The source guards below exist so the console does not overrule a live
	// authority's statement; there is no live authority left here, and refusing
	// would strand the row forever because no account exists that could unbind
	// it. An administrator may remove it.
	orphan := false
	if peer != nil && peer.Canonical != "" {
		missing, lookupErr := accountMissing(ctx, users, peer.Canonical)
		if lookupErr != nil {
			return lookupErr
		}
		orphan = missing
	}
	adminOrphan := req.IsAdmin && orphan

	if peer != nil && peer.Source != "console" && !adminOrphan {
		if peer.Source == "onex-iam" {
			return errno.ErrPermissionDenied.WithMessage(
				"该绑定由 onex-iam 管理，请在那里撤销；在控制台解绑只会留下仍然生效的外部授权")
		}
		return errno.ErrPermissionDenied.WithMessage("该绑定的来源未知，控制台不可解绑")
	}
	// Same decision the view renders: a row the caller may read is not
	// necessarily one they may remove (a mismatch, for example, is an
	// administrator's repair).
	if ok, _ := peerUnbindable(req.ActorID, req.IsAdmin, binding, peer); !ok && !adminOrphan {
		return errno.ErrPermissionDenied.WithMessage("没有权限解绑该飞书身份")
	}

	userID := ""
	switch {
	case binding != nil:
		userID = binding.UserID
	case peer != nil && !orphan:
		// An orphan's canonical names nothing: there is no account row to
		// unbind, so falling through to the projection delete below is the
		// whole repair.
		userID = peer.Canonical
	}
	if userID == "" {
		// No account to unbind: an orphan (its canonical names no account, or
		// it has none) or a console row with no canonical. Only an
		// administrator can reach this branch (a caller's own id is never
		// empty).
		if peer == nil || b.st == nil || b.st.PeerAgents() == nil {
			return nil
		}
		if err := b.st.PeerAgents().DeleteByPeer(ctx, "lark", b.appID, peerID); err != nil {
			return errno.ErrDBWrite.WithMessage("解绑飞书失败: %s", err.Error())
		}
		return nil
	}

	if err := b.unbindFeishu(ctx, userID, peerID); err != nil {
		return err
	}
	// Repairing a mismatch: the account binding and the peer row named different
	// accounts, so unbindFeishu deliberately left the row alone.
	if req.IsAdmin && peer != nil && peer.Canonical != userID && b.st != nil && b.st.PeerAgents() != nil {
		if err := b.st.PeerAgents().DeleteByPeer(ctx, "lark", b.appID, peerID); err != nil {
			return errno.ErrDBWrite.WithMessage("解绑飞书身份失败: %s", err.Error())
		}
	}
	return nil
}

// listPeerBindings returns every projection of this deployment's Lark app.
func (b *userBiz) listPeerBindings(ctx context.Context) ([]*model.PeerAgentM, error) {
	if b == nil || b.st == nil || b.st.PeerAgents() == nil || b.appID == "" {
		return nil, nil
	}
	_, rows, err := b.st.PeerAgents().List(ctx, where.F("backend", "lark", "scope", b.appID))
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// getPeerBinding returns one projection, or nil when this deployment has no
// peer store or no Lark app configured.
func (b *userBiz) getPeerBinding(ctx context.Context, peerID string) (*model.PeerAgentM, error) {
	if b == nil || b.st == nil || b.st.PeerAgents() == nil || b.appID == "" || peerID == "" {
		return nil, nil
	}
	return b.st.PeerAgents().GetByPeer(ctx, "lark", b.appID, peerID)
}

func (b *userBiz) buildFeishuItem(
	actorID string, isAdmin bool,
	binding *model.UserLarkBindingM, peer *model.PeerAgentM,
) FeishuBindingItem {
	item := FeishuBindingItem{
		Backend: "lark",
		Scope:   b.appID,
		Agents:  []string{},
	}
	if binding != nil {
		item.PeerID = binding.OpenID
		item.Canonical = binding.UserID
		item.Feishu = bindingOf(binding)
	}
	if peer != nil {
		item.Backend = peer.Backend
		item.Scope = peer.Scope
		item.PeerID = peer.PeerID
		item.Canonical = peer.Canonical
		item.Source = peer.Source
		if names := peer.AgentNames(); len(names) > 0 {
			item.Agents = names
		}
		if !peer.UpdatedAt.IsZero() {
			updated := peer.UpdatedAt
			item.UpdatedAt = &updated
		}
	} else if binding != nil && !binding.UpdatedAt.IsZero() {
		updated := binding.UpdatedAt
		item.UpdatedAt = &updated
	}

	switch {
	case binding != nil && peer != nil:
		if peer.Canonical == binding.UserID {
			item.State = PeerStateBound
		} else {
			item.State = PeerStateMismatch
		}
	case binding != nil:
		item.State = PeerStateAccountOnly
	default:
		item.State = PeerStatePeerOnly
	}

	writable, editReason := peerEditable(actorID, isAdmin, binding, peer)
	canUnbind, unbindReason := peerUnbindable(actorID, isAdmin, binding, peer)
	item.Writable = writable
	item.CanUnbind = canUnbind
	item.ReadonlyReason = editReason
	if item.ReadonlyReason == "" {
		item.ReadonlyReason = unbindReason
	}
	return item
}

func peerEditable(
	actorID string, isAdmin bool,
	binding *model.UserLarkBindingM, peer *model.PeerAgentM,
) (bool, string) {
	if peer == nil {
		return false, PeerReadonlyNoPeer
	}
	if peer.Source != "console" {
		if peer.Source == "onex-iam" {
			return false, PeerReadonlyExternal
		}
		return false, PeerReadonlyUnknownSource
	}
	if peer.Canonical == "" {
		return false, PeerReadonlyNoCanonical
	}
	if binding != nil && peer.Canonical != binding.UserID {
		return false, PeerReadonlyMismatch
	}
	if !isAdmin && peer.Canonical != actorID {
		return false, PeerReadonlyNotOwner
	}
	return true, ""
}

func peerUnbindable(
	actorID string, isAdmin bool,
	binding *model.UserLarkBindingM, peer *model.PeerAgentM,
) (bool, string) {
	switch {
	case binding == nil && peer == nil:
		return false, PeerReadonlyNoPeer
	case peer != nil:
		if peer.Source != "console" {
			if peer.Source == "onex-iam" {
				return false, PeerReadonlyExternal
			}
			return false, PeerReadonlyUnknownSource
		}
		if peer.Canonical == "" && !isAdmin {
			return false, PeerReadonlyNoCanonical
		}
		if binding != nil && peer.Canonical != binding.UserID && !isAdmin {
			return false, PeerReadonlyMismatch
		}
		owns := peer.Canonical == actorID || (binding != nil && binding.UserID == actorID)
		if !isAdmin && !owns {
			return false, PeerReadonlyNotOwner
		}
		return true, ""
	default:
		if !isAdmin && binding.UserID != actorID {
			return false, PeerReadonlyNotOwner
		}
		return true, ""
	}
}

// peerVisible reports whether the caller may see this pair. A row on either
// side that names the caller is theirs to see; an administrator sees all.
func peerVisible(actorID string, binding *model.UserLarkBindingM, peer *model.PeerAgentM) bool {
	if binding != nil && binding.UserID == actorID {
		return true
	}
	if peer != nil && peer.Canonical == actorID {
		return true
	}
	return false
}

// accountMissing reports whether id names no platform account. A read error is
// returned rather than treated as "missing": deleting a binding because the
// account table was briefly unreadable would be the wrong kind of confidence.
func accountMissing(ctx context.Context, users store.UserStore, id string) (bool, error) {
	user, err := users.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return true, nil
		}
		return false, errno.ErrDBRead.WithMessage("读取账号信息失败: %s", err.Error())
	}
	return user == nil, nil
}

// markAdminUnbindableOrphans widens can_unbind for an administrator on a row
// whose canonical names no account.
//
// The source-based read-only rule protects a live authority's statement from
// being overruled by the console. An orphan has no authority left behind it and
// cannot be repaired into anything usable; leaving it undeletable strands it on
// every surface. Writable is deliberately untouched: the agent list still
// belongs to whoever wrote the row, and a dead row has nothing to configure.
func markAdminUnbindableOrphans(isAdmin bool, items []FeishuBindingItem) {
	if !isAdmin {
		return
	}
	for i := range items {
		item := &items[i]
		if item.CanUnbind || item.Canonical == "" || item.Account != nil {
			continue
		}
		item.CanUnbind = true
	}
}

func peerEditError(reason string) error {
	switch reason {
	case PeerReadonlyNoPeer:
		return errno.ErrNotFound.WithMessage("该飞书账号还没有机器人侧的绑定，请先重新绑定")
	case PeerReadonlyExternal:
		return errno.ErrPermissionDenied.WithMessage("该绑定由 onex-iam 管理，控制台只读")
	case PeerReadonlyUnknownSource:
		return errno.ErrPermissionDenied.WithMessage("该绑定的来源未知，控制台不可修改")
	case PeerReadonlyNoCanonical:
		return errno.ErrPermissionDenied.WithMessage("该飞书身份没有关联平台账号，无法配置智能体")
	case PeerReadonlyMismatch:
		return errno.ErrAlreadyExists.WithMessage("该飞书身份在机器人侧指向了另一个账号，请先解绑后重新绑定")
	default:
		return errno.ErrPermissionDenied.WithMessage("没有权限修改该绑定")
	}
}

// validateAgentNames normalises the list and checks the names being introduced.
//
// Only *new* names are checked against the directory: a binding may hold a name
// that has since been removed or disabled, and refusing the whole edit would
// leave the user unable to remove it or change anything else.
func (b *userBiz) validateAgentNames(
	ctx context.Context, names, previous []string,
) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("智能体名不能为空")
		}
		if strings.HasPrefix(name, "@") {
			return nil, errno.ErrInvalidArgument.WithMessage(
				"智能体列表只能填写具体名称，不能填写 @peer 这类引用")
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) > maxPeerAgents {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"每个飞书身份只能绑定 %d 个智能体", maxPeerAgents)
	}
	if b.catalog == nil {
		return out, nil
	}

	catalog, err := b.catalog.ListAgent(ctx)
	if err != nil {
		return nil, errno.ErrNotConfigured.WithMessage(
			"读取智能体目录失败，暂时无法校验绑定: %s", err.Error())
	}
	// An empty directory usually means this deployment has no agent service
	// configured; treating every name as unknown would refuse all writes.
	if catalog == nil || len(catalog.Agents) == 0 {
		return out, nil
	}

	known := make(map[string]string, len(catalog.Agents))
	for _, agent := range catalog.Agents {
		known[agent.Name] = agent.Status
	}
	prev := make(map[string]struct{}, len(previous))
	for _, name := range previous {
		prev[name] = struct{}{}
	}

	var unknown, disabled []string
	for _, name := range out {
		if _, existed := prev[name]; existed {
			continue
		}
		status, ok := known[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if status == "disabled" {
			disabled = append(disabled, name)
		}
	}
	if len(unknown) > 0 {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"未知智能体: %s", strings.Join(unknown, ", "))
	}
	if len(disabled) > 0 {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"智能体已停用: %s", strings.Join(disabled, ", "))
	}
	return out, nil
}

func attachPeerAccounts(
	ctx context.Context, users store.UserStore, items []FeishuBindingItem,
) error {
	seen := make(map[string]struct{}, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.Canonical == "" {
			continue
		}
		if _, ok := seen[item.Canonical]; ok {
			continue
		}
		seen[item.Canonical] = struct{}{}
		ids = append(ids, item.Canonical)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := users.UsersByIDs(ctx, ids)
	if err != nil {
		return errno.ErrDBRead.WithMessage("读取账号信息失败: %s", err.Error())
	}
	byID := make(map[string]*model.UserM, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for i := range items {
		if user := byID[items[i].Canonical]; user != nil {
			items[i].Account = &FeishuBindingAccount{
				ID:          user.ID,
				Username:    user.Username,
				DisplayName: user.EffectiveName(),
				Status:      user.Status,
			}
		}
	}
	return nil
}

func peerSourceMatches(item FeishuBindingItem, want string) bool {
	switch want {
	case "":
		return true
	case "none":
		return item.State == PeerStateAccountOnly
	default:
		return item.Source == want
	}
}

func peerMatchesKeyword(item FeishuBindingItem, keyword string) bool {
	fields := make([]string, 0, 8)
	fields = append(fields, item.PeerID, item.Canonical)
	if item.Feishu != nil {
		fields = append(fields, item.Feishu.Name, item.Feishu.UnionID, item.Feishu.LarkUserID)
	}
	if item.Account != nil {
		fields = append(fields, item.Account.Username, item.Account.DisplayName)
	}
	fields = append(fields, item.Agents...)
	for _, field := range fields {
		if field != "" && strings.Contains(strings.ToLower(field), keyword) {
			return true
		}
	}
	return false
}

func peerUpdatedAfter(a, b FeishuBindingItem) bool {
	switch {
	case a.UpdatedAt == nil && b.UpdatedAt == nil:
		return a.PeerID < b.PeerID
	case a.UpdatedAt == nil:
		return false
	case b.UpdatedAt == nil:
		return true
	case a.UpdatedAt.Equal(*b.UpdatedAt):
		return a.PeerID < b.PeerID
	default:
		return a.UpdatedAt.After(*b.UpdatedAt)
	}
}

func normalizePeerPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

// AccountStatus implements UserBiz.
func (b *userBiz) AccountStatus(
	ctx context.Context, userID string,
) (known, active bool, role string, err error) {
	users := b.users()
	if users == nil || userID == "" {
		return false, true, "", nil
	}
	user, err := users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return false, true, "", nil
		}
		return false, true, "", err
	}
	if user == nil {
		return false, true, "", nil
	}
	return true, user.IsActive(), user.Role, nil
}

func viewOf(user *model.UserM) UserView {
	return UserView{
		ID:          user.ID,
		Username:    user.Username,
		DisplayName: user.EffectiveName(),
		Email:       user.Email,
		Role:        user.Role,
		Status:      user.Status,
		Provider:    user.Provider,
		CreatedAt:   user.CreatedAt,
		LastLoginAt: user.LastLoginAt,
	}
}

func bindingOf(binding *model.UserLarkBindingM) *FeishuBinding {
	return &FeishuBinding{
		OpenID:     binding.OpenID,
		UnionID:    binding.UnionID,
		LarkUserID: binding.LarkUserID,
		Name:       binding.Name,
		AvatarURL:  binding.AvatarURL,
		BoundVia:   binding.BoundVia,
		BoundAt:    binding.BoundAt,
	}
}
