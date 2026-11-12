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

package model

import (
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() {
	registry.Register(&UserM{})
	registry.Register(&UserLarkBindingM{})
	registry.Register(&UserBindCodeM{})
}

// TableNameUserM returns the physical table name of chat.user.
func TableNameUserM() string { return dbschema.Table(dbschema.Chat, "user") }

// TableNameUserLarkBindingM returns the physical table name of
// chat.user_lark_binding.
func TableNameUserLarkBindingM() string {
	return dbschema.Table(dbschema.Chat, "user_lark_binding")
}

// TableNameUserBindCodeM returns the physical table name of chat.user_bind_code.
func TableNameUserBindCodeM() string {
	return dbschema.Table(dbschema.Chat, "user_bind_code")
}

// Account statuses.
const (
	// UserStatusActive can log in and use the platform.
	UserStatusActive = "active"
	// UserStatusDisabled cannot log in. A token issued before the account was
	// disabled is refused by the chat service's status guard within
	// UserStatusCacheTTL.
	UserStatusDisabled = "disabled"
)

// Account roles managed by the console. They are unions with the deployment's
// existing role lists (RBAC / setting rows): a role granted there still applies,
// and the console's grant is an additional source rather than a replacement.
const (
	UserRoleAdmin = "admin"
	UserRoleSRE   = "sre"
)

// UserM is one platform account.
//
// # Why this table exists
//
// /v1/auth/login used to mint a token from any non-empty username and password
// because there was nothing to check them against. This row is that "something":
// the password hash is the credential, the stable ID is what tokens name, and
// the status is what an administrator can turn off.
type UserM struct {
	// ID is the stable identity a token carries. It is a UUID rather than the
	// username so renaming an account (or changing a login provider) does not
	// rewrite every record that names the person.
	ID string `gorm:"column:id;primaryKey;size:64" json:"id"`

	// Username is the login name, unique across accounts.
	Username string `gorm:"column:username;size:64;uniqueIndex:uq_chat_user_username" json:"username"`

	// PasswordHash is the bcrypt hash. Empty means "no password": an account
	// provisioned by Feishu OAuth has no local credential until it sets one.
	PasswordHash string `gorm:"column:password_hash;size:255" json:"-"`

	// DisplayName is what the console and the bot show. Empty falls back to the
	// username.
	DisplayName string `gorm:"column:display_name;size:128" json:"displayName"`

	// Email is optional contact information, not a login credential.
	Email string `gorm:"column:email;size:255" json:"email"`

	// Role is one of "" (ordinary user), "admin" or "sre". It is a union with
	// the deployment's role lists, never a replacement for them.
	Role string `gorm:"column:role;size:32" json:"role"`

	// Status is active or disabled.
	Status string `gorm:"column:status;size:32;index" json:"status"`

	// Provider names how the account was created: "local" (registration) or
	// "lark" (Feishu OAuth auto-provisioning). It is informational; login is
	// decided by the credentials the account actually has.
	Provider string `gorm:"column:provider;size:32" json:"provider"`

	// LastLoginAt is when the account last authenticated successfully.
	LastLoginAt *time.Time `gorm:"column:last_login_at" json:"lastLoginAt,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TableName returns the physical table name.
func (*UserM) TableName() string { return TableNameUserM() }

// IsActive reports whether the account may authenticate.
func (m *UserM) IsActive() bool { return m != nil && m.Status != UserStatusDisabled }

// EffectiveName is the name to show for the account.
func (m *UserM) EffectiveName() string {
	if m == nil {
		return ""
	}
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Username
}

// UserLarkBindingM is one Feishu identity attached to one platform account.
//
// # One row per account
//
// The account has a unique index on user_id: binding a second Feishu identity
// would make "which person is this message from" ambiguous, and the reference
// implementation enforces the same 1:1 rule. open_id is unique too, so one
// Feishu identity cannot be attached to two accounts.
//
// # open_id, not user_id
//
// The chat service keys its own peer bindings by open_id (it is what the event
// always carries, and external users have no user_id). The user_id is recorded
// for display and for role lists, but the join key is the open_id.
type UserLarkBindingM struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`

	UserID string `gorm:"column:user_id;size:64;uniqueIndex:uq_chat_user_lark_user" json:"userId"`
	OpenID string `gorm:"column:open_id;size:128;uniqueIndex:uq_chat_user_lark_open" json:"openId"`

	UnionID    string `gorm:"column:union_id;size:128" json:"unionId,omitempty"`
	LarkUserID string `gorm:"column:lark_user_id;size:128" json:"larkUserId,omitempty"`

	// Name and AvatarURL are the profile the Feishu identity reported, kept so
	// the console can show what was bound without calling Feishu again.
	Name      string `gorm:"column:name;size:128" json:"name"`
	AvatarURL string `gorm:"column:avatar_url;size:512" json:"avatarUrl,omitempty"`

	// BoundVia is "code" for the /bind-onex handshake, or "oauth" when the
	// account was provisioned by Feishu login.
	BoundVia string `gorm:"column:bound_via;size:16" json:"boundVia"`

	BoundAt   time.Time `gorm:"column:bound_at" json:"boundAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TableName returns the physical table name.
func (*UserLarkBindingM) TableName() string { return TableNameUserLarkBindingM() }

// LarkProfile is the contact record behind an open_id, restricted to the
// fields a binding stores. An empty field means "unknown" — a profile with a
// name but no user_id is a normal answer for an app without the user-id scope,
// not an error.
type LarkProfile struct {
	// LarkUserID is the tenant-scoped Feishu user_id, not onexai's account ID.
	LarkUserID string
	UnionID    string
	Name       string
	AvatarURL  string
}

// UserBindCodeM is a one-time code the console minted for the /bind-onex
// handshake.
//
// # Why the code has to be stored here as well as in chat.peer_bind_claim
//
// The bot writes its half of the handshake into chat.peer_bind_claim when it
// hears the code, but it does not know which platform account minted it. This
// row is that mapping: only the account that minted a code may poll or confirm
// it. The two tables are joined by the code, and both rows are deleted when the
// binding completes.
type UserBindCodeM struct {
	Code string `gorm:"column:code;primaryKey;size:64" json:"code"`

	UserID string `gorm:"column:user_id;size:64;index" json:"userId"`

	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	ExpiresAt time.Time `gorm:"column:expires_at;index" json:"expiresAt"`
}

// TableName returns the physical table name.
func (*UserBindCodeM) TableName() string { return TableNameUserBindCodeM() }
