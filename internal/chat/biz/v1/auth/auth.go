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

// Package auth implements authentication business logic.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/larkoauth"
	"github.com/onexstack/onexai/internal/chat/pkg/password"
	"github.com/onexstack/onexai/internal/chat/pkg/roleauthz"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/oidc"
)

// AuthBiz defines the authentication business logic interface.
type AuthBiz interface {
	Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error)
	Register(ctx context.Context, req *RegisterRequest) (*LoginResponse, error)
	LarkAuthorizeURL(ctx context.Context, req *LarkAuthorizeURLRequest) (*LarkAuthorizeURLResponse, error)
	LarkLogin(ctx context.Context, req *LarkLoginRequest) (*LoginResponse, error)
	Me(ctx context.Context, req *MeRequest) (*MeResponse, error)
	ChangePassword(ctx context.Context, req *ChangePasswordRequest) error
	Providers(ctx context.Context) *ProvidersResponse
}

// LoginRequest contains the credentials for authentication.
type LoginRequest struct {
	Provider  string `json:"provider"` // "lark" | "local" | "apikey"
	LarkToken string `json:"lark_token,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
}

// RegisterRequest creates a local account.
type RegisterRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
}

// LarkAuthorizeURLRequest asks for the URL the browser should be sent to.
type LarkAuthorizeURLRequest struct {
	// Redirect is the console path to land on after the callback. It is signed
	// into the state and therefore limited to a same-origin path.
	Redirect string `json:"redirect,omitempty"`
}

// LarkAuthorizeURLResponse is the Feishu authorization URL.
type LarkAuthorizeURLResponse struct {
	URL string `json:"url"`
}

// LarkLoginRequest is the callback half: the code Feishu returned plus the
// signed state that came with it.
type LarkLoginRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// MeRequest names the caller whose account is being read.
type MeRequest struct {
	UserID string
}

// ChangePasswordRequest changes or sets the local password.
type ChangePasswordRequest struct {
	UserID      string
	OldPassword string
	NewPassword string
}

// LoginResponse contains the issued JWT and basic user info.
type LoginResponse struct {
	Token string `json:"token"`
	// OIDCToken is the same identity signed as an OIDC-compatible RS256 token,
	// or empty when the deployment has not enabled OIDC. It is additive: the
	// browser keeps using Token.
	OIDCToken string `json:"oidc_token,omitempty"`
	UserID    string `json:"user_id"`
	Name      string `json:"name,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	// Redirect is only set by Feishu login: the path the SPA should return to.
	Redirect string `json:"redirect,omitempty"`
}

// UserInfo is the account view the console shows.
type UserInfo struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email,omitempty"`
	Role        string     `json:"role,omitempty"`
	Status      string     `json:"status"`
	Provider    string     `json:"provider"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// FeishuBindingInfo is the Feishu identity attached to an account.
type FeishuBindingInfo struct {
	OpenID     string    `json:"open_id"`
	UnionID    string    `json:"union_id,omitempty"`
	LarkUserID string    `json:"lark_user_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	AvatarURL  string    `json:"avatar_url,omitempty"`
	BoundVia   string    `json:"bound_via,omitempty"`
	BoundAt    time.Time `json:"bound_at"`
}

// MeResponse is the signed-in account and its Feishu binding.
type MeResponse struct {
	User         *UserInfo          `json:"user"`
	Feishu       *FeishuBindingInfo `json:"feishu,omitempty"`
	Providers    *ProvidersResponse `json:"providers"`
	CanChangePwd bool               `json:"can_change_password"`
}

// ProvidersResponse tells the console which login methods are available.
type ProvidersResponse struct {
	Local        bool `json:"local"`
	Registration bool `json:"registration"`
	Lark         bool `json:"lark"`
}

// ProviderOptions gates the login providers.
type ProviderOptions struct {
	AllowLocal        bool
	AllowRegistration bool
	// BootstrapAdmin grants the first account the administrator role while the
	// table is empty. Off unless the deployment opted in.
	BootstrapAdmin bool
	AllowLark      bool
	AllowAPIKey    bool
	// AllowLarkOAuth is set when the deployment has app id, secret and redirect
	// URI; it is not a YAML switch of its own.
	AllowLarkOAuth bool
	// TokenTTL is the lifetime of an issued token.
	TokenTTL time.Duration
}

// RoleResolver answers role membership for the identity a login provider
// reports (see the identity each provider assigns below).
//
// # Why membership is resolved rather than handed in
//
// The lists live in chat.setting and change while the process runs, so a
// snapshot taken at start-up would issue tokens that disagree with the surface
// they are used on: one reader would grant a role the other denies, and the
// disagreement would be invisible until someone could not do their job. The
// interface is the surface a caller needs (two questions), and the live cache
// behind it is the same one the Lark and MCP surfaces consult, which is what
// makes "one list, one answer" true by construction rather than by convention.
//
// A resolver that answers false for everyone is the safe direction, and it is
// also what a nil resolver does: no lists means no roles, never all of them.
// RoleResolver is the role surface chat's consumers share. It is an alias of
// roleauthz.Resolver so the package that can answer from the rule table and the
// package that answers from the lists are interchangeable without a conversion:
// the deployment picks one at start-up (see roleauthz.New), and nothing
// downstream knows which it got.
type RoleResolver = roleauthz.Resolver

// RoleSets is a fixed pair of role lists, satisfying RoleResolver. It exists for
// tests and for any construction with no database behind it.
type RoleSets struct {
	SRE   map[string]struct{}
	Admin map[string]struct{}
}

// IsSREUser implements RoleResolver.
func (r RoleSets) IsSREUser(identity string) bool {
	if identity == "" {
		return false
	}
	_, ok := r.SRE[identity]
	return ok
}

// IsAdminUser implements RoleResolver. The union with the SRE list is the
// caller's business, as it is for every implementation: the cache behind the
// live path builds the union when it reads the two rows.
func (r RoleSets) IsAdminUser(identity string) bool {
	if identity == "" {
		return false
	}
	_, ok := r.Admin[identity]
	return ok
}

// UserDirectory is the slice of the account store authentication needs.
//
// It is narrow on purpose: the auth package must not grow a dependency on the
// admin/binding surface, and a test of the login rules should not have to
// implement them. *store.UserStore satisfies this interface.
type UserDirectory interface {
	Create(ctx context.Context, obj *model.UserM) error
	Update(ctx context.Context, obj *model.UserM) error
	GetByUsername(ctx context.Context, username string) (*model.UserM, error)
	GetByID(ctx context.Context, id string) (*model.UserM, error)
	CountAll(ctx context.Context) (int64, error)
	LarkBindingByOpenID(ctx context.Context, openID string) (*model.UserLarkBindingM, error)
	PutLarkBinding(ctx context.Context, m *model.UserLarkBindingM) error
}

// authBiz handles authentication.
type authBiz struct {
	jwtSecret string
	providers ProviderOptions
	roles     RoleResolver
	users     UserDirectory
	lark      *larkoauth.Client
	state     *larkoauth.StateSigner
	// oidc is nil when the deployment has not enabled OIDC. It only adds a
	// second, Kubernetes-verifiable token to the login response.
	oidc *oidc.Signer
}

// New creates an authBiz instance. A nil resolver denies every role: a caller
// wired without the role lists grants nobody, which is the same direction as an
// empty list.
func New(
	jwtSecret string, providers ProviderOptions, roles RoleResolver,
	users UserDirectory, lark *larkoauth.Client, oidcSigner *oidc.Signer,
) *authBiz {
	if providers.TokenTTL <= 0 {
		providers.TokenTTL = 24 * time.Hour
	}
	return &authBiz{
		jwtSecret: jwtSecret,
		providers: providers,
		roles:     roles,
		users:     users,
		lark:      lark,
		state:     larkoauth.NewStateSigner(jwtSecret),
		oidc:      oidcSigner,
	}
}

// Login authenticates a user and returns a signed JWT.
//
// # Local accounts
//
// The local provider now checks a bcrypt hash in chat.user. It answers the same
// message for "no such user" and "wrong password" so the endpoint cannot be
// used to enumerate account names.
//
// # The other providers
//
// lark_token and api_key are still opaque identities with no verification; they
// remain off unless a deployment deliberately enables them. Feishu login is the
// OAuth flow (LarkLogin), which proves the identity with a one-time code.
func (b *authBiz) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	switch req.Provider {
	case "local":
		return b.loginLocal(ctx, req)
	case "lark":
		if !b.providers.AllowLark {
			return nil, errno.ErrPermissionDenied.WithMessage(
				"auth provider %q is disabled; set chat.login.allow-lark to enable it (the lark_token is not verified)", req.Provider)
		}
		if req.LarkToken == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("lark_token is required for provider=lark")
		}
		return b.issue(ctx, issueInput{
			userID:   uuid.NewString(),
			name:     "Lark User",
			openID:   req.LarkToken,
			provider: "lark",
			identity: req.LarkToken,
		})
	case "apikey":
		if !b.providers.AllowAPIKey {
			return nil, errno.ErrPermissionDenied.WithMessage(
				"auth provider %q is disabled; set chat.login.allow-apikey to enable it (no key is verified)", req.Provider)
		}
		if req.APIKey == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("api_key is required for provider=apikey")
		}
		return b.issue(ctx, issueInput{
			userID:   uuid.NewString(),
			name:     "API Key User",
			provider: "apikey",
		})
	default:
		return nil, errno.ErrInvalidArgument.WithMessage("unsupported auth provider: %q", req.Provider)
	}
}

func (b *authBiz) loginLocal(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	if !b.providers.AllowLocal {
		return nil, errno.ErrPermissionDenied.WithMessage(
			"auth provider %q is disabled; set chat.login.allow-local to enable it", req.Provider)
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("username and password are required for provider=local")
	}
	if b.users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储，无法使用密码登录")
	}

	user, err := b.users.GetByUsername(ctx, strings.TrimSpace(req.Username))
	if err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return nil, errno.ErrUnauthenticated.WithMessage("用户名或密码错误")
		}
		return nil, errno.ErrDBRead.WithMessage("读取账号失败: %s", err.Error())
	}
	if user == nil {
		return nil, errno.ErrUnauthenticated.WithMessage("用户名或密码错误")
	}
	if !user.IsActive() {
		return nil, errno.ErrPermissionDenied.WithMessage("账号已被禁用，请联系管理员")
	}
	if user.PasswordHash == "" {
		return nil, errno.ErrPermissionDenied.WithMessage("该账号未设置密码，请使用飞书登录")
	}
	if !password.Verify(user.PasswordHash, req.Password) {
		return nil, errno.ErrUnauthenticated.WithMessage("用户名或密码错误")
	}

	now := time.Now()
	user.LastLoginAt = &now
	// Best effort: a failed timestamp write must not turn a successful login
	// into an error, but it is worth keeping the row current.
	_ = b.users.Update(ctx, user)

	return b.issue(ctx, issueInput{
		userID:   user.ID,
		name:     user.EffectiveName(),
		provider: "local",
		identity: user.Username,
		role:     user.Role,
	})
}

// Register creates a local account and signs it in.
//
// Registration is a login-shaped response rather than "201 Created": the
// console's next action is always to enter the application, and making the
// caller perform a second request would give the two steps two failure modes
// for one user action.
func (b *authBiz) Register(ctx context.Context, req *RegisterRequest) (*LoginResponse, error) {
	if !b.providers.AllowRegistration {
		return nil, errno.ErrPermissionDenied.WithMessage("本部署未开放注册")
	}
	if b.users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储，无法注册")
	}

	username := strings.TrimSpace(req.Username)
	if err := password.ValidateUsername(username); err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	hash, err := password.Hash(req.Password)
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if len([]rune(displayName)) > 32 {
		displayName = string([]rune(displayName)[:32])
	}

	now := time.Now()
	user := &model.UserM{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: hash,
		DisplayName:  displayName,
		// The first account is the bootstrap administrator. Without this rule an
		// open-registration deployment has no way to reach the user-management
		// page it just gained: every account starts with no role, and the only
		// other role source is the control plane. The rule is "empty table", not
		// "first registration wins a race": a deployment that already has
		// accounts never grants this.
		Role:      bootstrapRole(ctx, b.users, b.providers.BootstrapAdmin),
		Status:    model.UserStatusActive,
		Provider:  "local",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := b.users.Create(ctx, user); err != nil {
		if isDuplicate(err) {
			return nil, errUsernameTaken()
		}
		return nil, errno.ErrDBWrite.WithMessage("创建账号失败: %s", err.Error())
	}

	return b.issue(ctx, issueInput{
		userID:   user.ID,
		name:     user.EffectiveName(),
		provider: "local",
		identity: user.Username,
	})
}

// LarkAuthorizeURL returns the Feishu authorization URL for a login round-trip.
func (b *authBiz) LarkAuthorizeURL(
	_ context.Context, req *LarkAuthorizeURLRequest,
) (*LarkAuthorizeURLResponse, error) {
	if !b.providers.AllowLarkOAuth || b.lark == nil || !b.lark.Enabled() {
		return nil, errno.ErrNotConfigured.WithMessage("本部署未配置飞书登录（lark.oauth-redirect-uri）")
	}
	state, err := b.state.Sign(req.Redirect)
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	return &LarkAuthorizeURLResponse{URL: b.lark.AuthorizeURL(state)}, nil
}

// LarkLogin exchanges the Feishu code and signs the account in.
//
// # An unbound Feishu identity becomes an account, unless registration is closed
//
// The reference implementation only binds an existing account, because its
// Feishu app could not authorize the external users who needed binding. This
// deployment's app covers its own tenant, so the natural first visit is "I have
// never used this console". Auto-provisioning keeps that visit from ending in
// an error; the account has no password, and can set one later (or be reset by
// an administrator). Closing chat.login.allow-registration closes this path too,
// which is what a deployment that hands out accounts by hand wants.
func (b *authBiz) LarkLogin(ctx context.Context, req *LarkLoginRequest) (*LoginResponse, error) {
	if !b.providers.AllowLarkOAuth || b.lark == nil || !b.lark.Enabled() {
		return nil, errno.ErrNotConfigured.WithMessage("本部署未配置飞书登录")
	}
	redirect, err := b.state.Verify(req.State)
	if err != nil {
		return nil, errno.ErrUnauthenticated.WithMessage("%s", err.Error())
	}
	identity, err := b.lark.Exchange(ctx, req.Code)
	if err != nil {
		return nil, errno.ErrUnauthenticated.WithMessage("%s", err.Error())
	}
	if b.users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储，无法使用飞书登录")
	}

	binding, err := b.users.LarkBindingByOpenID(ctx, identity.OpenID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取飞书绑定失败: %s", err.Error())
	}

	var user *model.UserM
	if binding != nil {
		user, err = b.users.GetByID(ctx, binding.UserID)
		if err != nil || user == nil {
			return nil, errno.ErrUnauthenticated.WithMessage("飞书绑定的账号不存在，请联系管理员")
		}
		if !user.IsActive() {
			return nil, errno.ErrPermissionDenied.WithMessage("账号已被禁用，请联系管理员")
		}
	} else {
		if !b.providers.AllowRegistration {
			return nil, errno.ErrPermissionDenied.WithMessage(
				"该飞书账号尚未绑定平台账号；请先用密码登录并在账号设置中绑定飞书，或联系管理员")
		}
		user, err = b.provisionLarkUser(ctx, identity)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()
	user.LastLoginAt = &now
	_ = b.users.Update(ctx, user)

	resp, err := b.issue(ctx, issueInput{
		userID:   user.ID,
		name:     user.EffectiveName(),
		openID:   identity.OpenID,
		provider: "lark",
		identity: larkRoleIdentity(identity),
		role:     user.Role,
	})
	if err != nil {
		return nil, err
	}
	resp.Redirect = redirect
	return resp, nil
}

// provisionLarkUser creates an account for a Feishu identity that has none.
func (b *authBiz) provisionLarkUser(
	ctx context.Context, identity *larkoauth.Identity,
) (*model.UserM, error) {
	username, err := b.freeUsername(ctx, identity)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &model.UserM{
		ID:          uuid.NewString(),
		Username:    username,
		DisplayName: firstNonEmpty(identity.Name, username),
		Email:       identity.Email,
		Role:        bootstrapRole(ctx, b.users, b.providers.BootstrapAdmin),
		Status:      model.UserStatusActive,
		Provider:    "lark",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := b.users.Create(ctx, user); err != nil {
		if isDuplicate(err) {
			return nil, errUsernameTaken()
		}
		return nil, errno.ErrDBWrite.WithMessage("创建飞书账号失败: %s", err.Error())
	}

	binding := &model.UserLarkBindingM{
		UserID:     user.ID,
		OpenID:     identity.OpenID,
		UnionID:    identity.UnionID,
		LarkUserID: identity.UserID,
		Name:       identity.Name,
		AvatarURL:  identity.AvatarURL,
		BoundVia:   "oauth",
		BoundAt:    now,
		UpdatedAt:  now,
	}
	if err := b.users.PutLarkBinding(ctx, binding); err != nil {
		if isDuplicate(err) {
			return nil, errno.ErrPermissionDenied.WithMessage("该飞书身份已绑定其他账号")
		}
		return nil, errno.ErrDBWrite.WithMessage("保存飞书绑定失败: %s", err.Error())
	}
	return user, nil
}

// freeUsername derives a login name from the Feishu identity and makes it
// unique. The suffix is only consulted on a collision, so the common case is a
// readable name rather than a random one.
func (b *authBiz) freeUsername(ctx context.Context, identity *larkoauth.Identity) (string, error) {
	base := deriveUsername(identity)
	taken, err := b.usernameTaken(ctx, base)
	if err != nil {
		return "", err
	}
	if !taken {
		return base, nil
	}
	for i := 0; i < 5; i++ {
		suffix, err := randomHex(3)
		if err != nil {
			return "", errno.ErrInternal.WithMessage("生成用户名失败: %s", err.Error())
		}
		candidate := base
		if len(candidate) > 26 {
			candidate = candidate[:26]
		}
		candidate = candidate + "_" + suffix
		taken, err = b.usernameTaken(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errUsernameTaken()
}

// usernameTaken distinguishes "the name is free" from "the lookup failed".
// Treating a database error as free is how a provisioning step hands out a name
// it cannot actually create.
func (b *authBiz) usernameTaken(ctx context.Context, username string) (bool, error) {
	_, err := b.users.GetByUsername(ctx, username)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, store.ErrRecordNotFound):
		return false, nil
	default:
		return false, errno.ErrDBRead.WithMessage("检查用户名失败: %s", err.Error())
	}
}

func deriveUsername(identity *larkoauth.Identity) string {
	raw := identity.UserID
	if raw == "" {
		raw = identity.OpenID
	}
	var out strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			out.WriteRune(r)
		}
		if out.Len() >= 24 {
			break
		}
	}
	name := strings.Trim(out.String(), "_.-")
	if name == "" {
		suffix, _ := randomHex(4)
		name = "user_" + suffix
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "u_" + name
	}
	if len(name) < 3 {
		name = name + "_lark"
	}
	return name
}

// Me returns the signed-in account and its Feishu binding.
func (b *authBiz) Me(ctx context.Context, req *MeRequest) (*MeResponse, error) {
	if b.users == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	user, err := b.users.GetByID(ctx, req.UserID)
	if err != nil || user == nil {
		return nil, errno.ErrNotFound.WithMessage("账号不存在")
	}
	feishu := b.feishuInfo(ctx, req.UserID)

	return &MeResponse{
		User:   userInfo(user),
		Feishu: feishu,
		Providers: &ProvidersResponse{
			Local:        b.providers.AllowLocal,
			Registration: b.providers.AllowRegistration,
			Lark:         b.providers.AllowLarkOAuth && b.lark != nil && b.lark.Enabled(),
		},
		CanChangePwd: true,
	}, nil
}

// ChangePassword verifies the current password and stores a new one.
//
// An account provisioned by Feishu has no hash; for it, the old password is
// allowed to be empty, which is the "set a password for the first time" case.
func (b *authBiz) ChangePassword(ctx context.Context, req *ChangePasswordRequest) error {
	if b.users == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有账号存储")
	}
	user, err := b.users.GetByID(ctx, req.UserID)
	if err != nil || user == nil {
		return errno.ErrNotFound.WithMessage("账号不存在")
	}
	if user.PasswordHash != "" && !password.Verify(user.PasswordHash, req.OldPassword) {
		return errno.ErrPermissionDenied.WithMessage("当前密码不正确")
	}
	hash, err := password.Hash(req.NewPassword)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	user.PasswordHash = hash
	user.UpdatedAt = time.Now()
	if err := b.users.Update(ctx, user); err != nil {
		return errno.ErrDBWrite.WithMessage("保存密码失败: %s", err.Error())
	}
	return nil
}

// Providers reports the login methods the console may offer.
func (b *authBiz) Providers(context.Context) *ProvidersResponse {
	return &ProvidersResponse{
		Local:        b.providers.AllowLocal,
		Registration: b.providers.AllowRegistration,
		Lark:         b.providers.AllowLarkOAuth && b.lark != nil && b.lark.Enabled(),
	}
}

type issueInput struct {
	userID   string
	name     string
	openID   string
	provider string
	identity string
	role     string
}

// issue signs a token for the given identity.
func (b *authBiz) issue(_ context.Context, in issueInput) (*LoginResponse, error) {
	externalSRE := b.roles != nil && b.roles.IsSREUser(in.identity)
	externalAdmin := b.roles != nil && b.roles.IsAdminUser(in.identity)
	// An account role of SRE implies admin, matching the roster's documented
	// "every SRE is an administrator". The two sources are kept in separate
	// claims so the middleware can recompute the account's half after an
	// administrator changes it without discarding the external grant.
	accountSRE := in.role == model.UserRoleSRE
	accountAdmin := in.role == model.UserRoleAdmin || in.role == model.UserRoleSRE

	expiresAt := time.Now().Add(b.providers.TokenTTL)
	claims := &bearer.Claims{
		UserID:        in.userID,
		Name:          in.name,
		OpenID:        in.openID,
		Provider:      in.provider,
		AccountRole:   in.role,
		ExternalAdmin: externalAdmin,
		ExternalSRE:   externalSRE,
		IsSRE:         accountSRE || externalSRE,
		IsAdmin:       accountAdmin || externalAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   in.userID,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(b.jwtSecret))
	if err != nil {
		return nil, errno.ErrSignToken.WithMessage("failed to sign token: %s", err.Error())
	}

	resp := &LoginResponse{
		Token:     signed,
		UserID:    in.userID,
		Name:      in.name,
		ExpiresAt: expiresAt.Unix(),
	}
	if b.oidc != nil {
		oidcToken, err := b.oidc.SignClaims(claims)
		if err != nil {
			return nil, errno.ErrSignToken.WithMessage("failed to sign OIDC token: %s", err.Error())
		}
		resp.OIDCToken = oidcToken
	}
	return resp, nil
}

func userInfo(user *model.UserM) *UserInfo {
	return &UserInfo{
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

// feishuBindingReader is implemented by the full account store; the narrow
// directory does not carry it because login does not need it.
type feishuBindingReader interface {
	LarkBinding(ctx context.Context, userID string) (*model.UserLarkBindingM, error)
}

func (b *authBiz) feishuInfo(ctx context.Context, userID string) *FeishuBindingInfo {
	reader, ok := b.users.(feishuBindingReader)
	if !ok {
		return nil
	}
	binding, err := reader.LarkBinding(ctx, userID)
	if err != nil || binding == nil {
		return nil
	}
	return &FeishuBindingInfo{
		OpenID:     binding.OpenID,
		UnionID:    binding.UnionID,
		LarkUserID: binding.LarkUserID,
		Name:       binding.Name,
		AvatarURL:  binding.AvatarURL,
		BoundVia:   binding.BoundVia,
		BoundAt:    binding.BoundAt,
	}
}

// errUsernameTaken is the conflict both registration paths answer with.
func errUsernameTaken() error {
	return errno.ErrAlreadyExists.WithMessage("用户名已存在")
}

func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique violation")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read randomness: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func larkRoleIdentity(identity *larkoauth.Identity) string {
	if identity.UserID != "" {
		return identity.UserID
	}
	return identity.OpenID
}

// bootstrapRole grants the administrator role to the very first account.
//
// # Why an empty table, not "the first registration"
//
// The user-management page needs an administrator, and a fresh deployment has
// neither an account nor a control-plane role binding for one. The rule "the
// first account is the administrator" is the smallest thing that makes the
// page reachable. It is deliberately conditioned on the table being empty: once
// any account exists, this never fires again, so it cannot promote a later
// registrant. A deployment that wants a different bootstrap can create the
// first account and then demote/rename it through the user-management page.
func bootstrapRole(ctx context.Context, users UserDirectory, enabled bool) string {
	if !enabled || users == nil {
		return ""
	}
	count, err := users.CountAll(ctx)
	if err != nil || count > 0 {
		return ""
	}
	return model.UserRoleAdmin
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
