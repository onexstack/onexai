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
	"strings"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&AuthzRuleM{}) }

// AuthzRuleM is one row of the authorization rule table, and the data source
// the OPA Authorizer evaluates against (see internal/pkg/authz/opa).
//
// One row = one "subject -> resource" grant, synced in full into memory on a
// timer. SubjectID/ResourceName support the wildcard "*" (meaning "any"); the
// exact semantics are interpreted by the Rego policy.
//
// It was ported field-for-field from the Feishu Bitable table the upstream
// system used, with the Bitable-specific wrappers (multi-value text fields,
// millisecond timestamps) replaced by their native Go types: list columns
// carry a JSON array in a text column (see StringList), timestamps are unix
// milliseconds.
type AuthzRuleM struct {
	ID   uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name string `gorm:"uniqueIndex;size:128" json:"name"` // Rule identifier, e.g. "rule-001" — unique, used for audit.

	// ── Subject (who is granted) ──
	// "namespace" is the Subject-side namespace-inheritance match
	// (Namespace -> Group -> User), interpreted by the Rego policy;
	// "serviceaccount" reuses the generic exact-match branch.
	SubjectType string     `gorm:"size:32;index" json:"subject_type"` // "user" | "group" | "serviceaccount" | "namespace" | "*"
	SubjectID   StringList `gorm:"type:text" json:"subject_id"`       // UserID / GroupCode / ServiceName / Namespace list; ["*"] means any subject

	// ── Resource (what is granted) ──
	ResourceKind string `gorm:"size:32;index" json:"resource_kind"` // "agent" | "mcp" | "tool" | "skill" | "knowledgebase" | "namespace" | "*"
	// Agent/MCP/Tool/Skill/KnowledgeBase name list; ["*"] means any resource
	// of that kind. For namespaced resources a bare entry matches the name in
	// any namespace (the historical contract), while "namespace/name" matches
	// that one object exactly. See the Rego policy's resource_matches.
	ResourceName StringList `gorm:"type:text" json:"resource_name"`

	// ResourceAttributes is a JSON object string (e.g. `{"env":"test"}`).
	// When non-empty, this rule only applies if every key-value matches the
	// Resource.Attributes extracted from the Tool call (subset semantics, see
	// ToolResourceMapping.AttributePaths). An empty string means no attribute
	// filter — the backward-compatible default.
	//
	// Each value may be:
	//   - a single string: "111" (exact) or "test-*" (glob)
	//   - an array of strings: ["111","222"] (any one matching hits)
	ResourceAttributes string `gorm:"type:text" json:"resource_attributes"`

	// ── Agent scope (which Agent this grant is for) ──
	//
	// AgentNames scopes the grant to particular Agents, matched against the
	// requesting Agent's name. **Empty means the rule is not agent-scoped** and
	// answers for whatever Agent asked — the behaviour every rule had before
	// this dimension existed, so a row written earlier keeps its meaning and no
	// existing grant changes.
	//
	// # Why the policy needed it
	//
	// Without it, a grant can only say "this subject may use this MCP", never
	// "…when running that Agent". The rule set was written when there was a
	// single built-in Agent, so its MCP grant named zke-kubeagent for every
	// subject and, being unscoped, answered for the second Agent too. That is
	// every dimension is fail-closed, so an MCP with no rule is refused — but a
	// grant is the thing an Agent needs to work at all, and a grant with no
	// scope answers for every Agent. Keeping one Agent away from an MCP that
	// another may use therefore takes an explicit agent-scoped **deny**.
	//
	// The column has no Feishu ancestor and is nullable-by-emptiness rather
	// than by SQL: an empty string means "not agent-scoped", which is what
	// every row written before it existed means.
	AgentNames StringList `gorm:"type:text" json:"agent_names"`

	// ── Grant ──
	Verbs  StringList `gorm:"type:text" json:"verbs"` // e.g. ["read"] / ["write"]; write does NOT imply read (least privilege)
	Effect string     `gorm:"size:16" json:"effect"`  // "allow" | "deny" (deny wins over allow)
	Status string     `gorm:"size:16" json:"status"`  // "enabled" | "disabled"

	Description string `gorm:"type:text" json:"description"`
	Owner       string `gorm:"size:256" json:"owner"`
	// Subjects and Resources are the multi-principal / multi-resource shapes.
	// They are transport fields (`gorm:"-"`): the persisted row stays the flat
	// subject_type/resource_kind form, and the control-plane adapter expands the
	// plural shape into the CRD's lists.
	Subjects         []AuthzSubject  `gorm:"-" json:"subjects,omitempty"`
	Resources        []AuthzResource `gorm:"-" json:"resources,omitempty"`
	SubjectNamespace string          `gorm:"-" json:"subject_namespace,omitempty"`
	CreatedAt        int64           `json:"created_at"` // unix millis
	// UpdatedAt carries no autoUpdateTime tag: authzrule writes both timestamps
	// itself, in Unix milliseconds like CreatedAt and the seed SQL. Changing the
	// units would rewrite every row on the next write, so the convention is
	// fixed here rather than derived.
	UpdatedAt int64 `json:"updated_at"`
}

// TableName returns the authorization rule table name.
func (AuthzRuleM) TableName() string { return dbschema.Table(dbschema.Agent, "authz_rule") }

// Enabled reports whether this rule should participate in evaluation.
func (r AuthzRuleM) Enabled() bool { return r.Status != StatusDisabled }

// AuthzSubject is one principal a policy names.
type AuthzSubject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// AuthzResource is one resource target of a policy.
type AuthzResource struct {
	Kind  string   `json:"kind"`
	Names []string `json:"names"`
}

// EffectiveSubjects returns the plural subject list, backfilled from the legacy
// single-subject columns when the caller only filled those.
//
// Callers must treat the result as read-only: it may be the row's own slice.
func (r *AuthzRuleM) EffectiveSubjects() []AuthzSubject {
	if len(r.Subjects) > 0 {
		return r.Subjects
	}
	if r.SubjectType == "" {
		return nil
	}
	out := make([]AuthzSubject, 0, len(r.SubjectID))
	for _, id := range r.SubjectID {
		out = append(out, AuthzSubject{Kind: r.SubjectType, Name: id})
	}
	return out
}

// EffectiveResources returns the plural resource list, backfilled from the
// legacy single-resource columns when the caller only filled those.
func (r *AuthzRuleM) EffectiveResources() []AuthzResource {
	if len(r.Resources) > 0 {
		return r.Resources
	}
	if r.ResourceKind == "" {
		return nil
	}
	return []AuthzResource{{Kind: r.ResourceKind, Names: []string(r.ResourceName)}}
}

// NormalizeLegacyFields derives the flat columns from the plural shape when it
// is representable, so a reader that only knows the old fields still sees the
// policy. A mixed-kind policy has no flat spelling and leaves them empty.
func (r *AuthzRuleM) NormalizeLegacyFields() {
	subjects := r.EffectiveSubjects()
	kind := ""
	mixed := false
	for _, subject := range subjects {
		k := strings.ToLower(strings.TrimSpace(subject.Kind))
		if kind == "" {
			kind = k
			continue
		}
		if kind != k {
			mixed = true
			break
		}
	}
	if !mixed && kind != "" {
		r.SubjectType = kind
		r.SubjectID = make(StringList, 0, len(subjects))
		for _, subject := range subjects {
			if subject.Name != "" {
				r.SubjectID = append(r.SubjectID, subject.Name)
			}
		}
	} else {
		r.SubjectType = ""
		r.SubjectID = nil
	}

	resources := r.EffectiveResources()
	if len(resources) == 1 {
		r.ResourceKind = resources[0].Kind
		r.ResourceName = StringList(resources[0].Names)
	} else {
		r.ResourceKind = ""
		r.ResourceName = nil
	}
}
