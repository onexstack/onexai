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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&SkillM{}) }

// SkillM describes a skill repository (a bundle of prompt/context files loaded
// into an agent from a provider such as GitHub or GitLab).
type SkillM struct {
	ID              uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name            string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName     string `gorm:"size:256" json:"display_name"`
	Status          string `gorm:"size:32" json:"status"`
	Description     string `gorm:"type:text" json:"description"`
	Provider        string `gorm:"size:64" json:"provider"` // "github" | "gitlab" | "lark_doc"
	Params          string `gorm:"type:text" json:"params"` // JSON string decoded into the provider params below
	CacheTTLSeconds int    `json:"cache_ttl_seconds"`
	AsCmd           string `gorm:"column:as_cmd;size:16" json:"as_cmd"`
	Namespace       string `gorm:"column:namespace;size:128;index" json:"namespace"`
	// Mode decides what a client receives: "hosted" (default for old rows)
	// loads the body into the server-side agent, "tool" runs Entrypoint, and
	// "guide" returns only Contract.
	Mode string `gorm:"column:mode;size:16" json:"mode"`
	// Contract is the compact description a local model needs to delegate.
	Contract string `gorm:"column:contract;type:text" json:"contract"`
	// Entrypoint is the executable a "tool" skill runs.
	Entrypoint string    `gorm:"column:entrypoint;size:256" json:"entrypoint"`
	Labels     StringMap `gorm:"-" json:"labels"`
	CreatedAt  int64     `json:"created_at"`
	UpdatedAt  int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the skill table name.
func (SkillM) TableName() string { return dbschema.Table(dbschema.Agent, "skill") }

// Enabled reports whether the skill may be loaded.
func (s SkillM) Enabled() bool { return s.Status != StatusDisabled }

// GetNamespace returns the normalized namespace.
func (s SkillM) GetNamespace() string { return NormalizeNamespace(s.Namespace) }

// GitHubParams is the decoded shape of SkillM.Params when Provider == "github".
type GitHubParams struct {
	RepoURL string `json:"repo_url"`
	Branch  string `json:"branch"`
	SubDir  string `json:"sub_dir"`
}

// GitLabParams is the decoded shape of SkillM.Params when Provider == "gitlab".
type GitLabParams struct {
	BaseURL     string `json:"base_url"`
	ProjectPath string `json:"project_path"`
	Branch      string `json:"branch"`
	SubDir      string `json:"sub_dir"`
	// Token is an inline credential, kept for records written before secrets
	// became a first-class resource. Prefer TokenSecretID.
	Token string `json:"token"`
	// TokenSecretID names a row in the secret store; when set it wins over Token.
	// It keeps the credential out of the params text, which is stored beside the
	// skill's description and logged by nothing but read by everything.
	TokenSecretID string `json:"token_secret_id"`
}

// LarkDocParams is the decoded shape of SkillM.Params when Provider == "lark_doc".
type LarkDocParams struct {
	DocToken string `json:"doc_token"`
}
