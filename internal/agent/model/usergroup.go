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
	registry.Register(&UserGroupM{})
	registry.Register(&UserGroupMemberM{})
}

// TableNameUserGroupM returns the physical table name under the configured
// dialect: agent.user_group on PostgreSQL, onexai_agent_user_group on MySQL.
func TableNameUserGroupM() string { return dbschema.Table(dbschema.Agent, "user_group") }

// TableNameUserGroupMemberM returns the physical member table name.
func TableNameUserGroupMemberM() string {
	return dbschema.Table(dbschema.Agent, "user_group_member")
}

// UserGroupM is one console-managed user group.
//
// # Why this table lives in the agent schema
//
// The group is consumed where authorization is evaluated, which is
// oai-agent-apiserver. Keeping it beside the policy engine means the membership
// expansion is a local indexed read, not a cross-service call on the hot path.
// It is deliberately not a declarative object: membership is identity hot data
// that changes far more often than a policy, and the only writer is this
// service.
//
// Name is the identifier a policy names as `subject_type=group`. It is unique
// and immutable: renaming a group would silently detach every rule that names
// it.
type UserGroupM struct {
	ID          string `gorm:"column:id;primaryKey;size:64" json:"id"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uq_agent_user_group_name" json:"name"`
	DisplayName string `gorm:"column:display_name;size:128;not null;default:''" json:"displayName"`
	Description string `gorm:"column:description;size:512;not null;default:''" json:"description"`
	// No database default: GORM omits a zero value when the column declares
	// one, which would turn an explicit `enabled: false` into `true` on insert.
	// The business layer always writes the intended value.
	Enabled bool `gorm:"column:enabled;not null" json:"enabled"`
	// Owner is the account ID that created the group. It is audit metadata; it
	// does not make the group private.
	Owner     string    `gorm:"column:owner;size:64;not null;default:''" json:"owner"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:current_timestamp" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:current_timestamp" json:"updatedAt"`

	// Members is loaded explicitly by the store. It is not a GORM association:
	// every write replaces the set as a whole, so an implicit save would add a
	// second way to mutate membership.
	Members []UserGroupMemberM `gorm:"-" json:"members"`
}

// TableName returns the physical table name.
func (*UserGroupM) TableName() string { return TableNameUserGroupM() }

// UserGroupMemberM is one person in a group, carrying every identifier the
// platform may see for them.
//
// # Why four identifiers
//
// onexai has more than one way to name the same person:
//
//   - UserID is the stable account UUID carried by a browser token and by the
//     console's agent runs.
//   - Username is the login name chat uses when it resolves roles.
//   - LarkUserID is the tenant user id chat forwards for IM traffic and which
//     historical rules were written against.
//   - OpenID is the fallback in tenants that cannot expose a user_id.
//
// Matching is exact on any non-empty alias. A member with only a browser
// identity will not match IM traffic, which is why the API warns about partial
// coverage rather than pretending the row is complete.
type UserGroupMemberM struct {
	ID      string `gorm:"column:id;primaryKey;size:64" json:"id"`
	GroupID string `gorm:"column:group_id;size:64;not null;index:idx_agent_user_group_member_group" json:"groupId"`

	UserID     string `gorm:"column:user_id;size:64;not null;default:'';index:idx_agent_user_group_member_user_id" json:"userID,omitempty"`
	Username   string `gorm:"column:username;size:64;not null;default:'';index:idx_agent_user_group_member_username" json:"username,omitempty"`
	LarkUserID string `gorm:"column:lark_user_id;size:64;not null;default:'';index:idx_agent_user_group_member_lark_user_id" json:"larkUserID,omitempty"`
	OpenID     string `gorm:"column:open_id;size:64;not null;default:'';index:idx_agent_user_group_member_open_id" json:"openID,omitempty"`

	// DisplayName and Email are for the console; they never participate in
	// matching.
	DisplayName string `gorm:"column:display_name;size:128;not null;default:''" json:"displayName,omitempty"`
	Email       string `gorm:"column:email;size:255;not null;default:''" json:"email,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at;not null;default:current_timestamp" json:"createdAt"`
}

// TableName returns the physical table name.
func (*UserGroupMemberM) TableName() string { return TableNameUserGroupMemberM() }

// Aliases returns every non-empty identifier this member can be matched by.
// The order is stable so callers can build deterministic keys.
func (m *UserGroupMemberM) Aliases() []string {
	out := make([]string, 0, 4)
	for _, v := range []string{m.UserID, m.Username, m.LarkUserID, m.OpenID} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// HasAlias reports whether identity names this member.
func (m *UserGroupMemberM) HasAlias(identity string) bool {
	if identity == "" {
		return false
	}
	return m.UserID == identity || m.Username == identity ||
		m.LarkUserID == identity || m.OpenID == identity
}
