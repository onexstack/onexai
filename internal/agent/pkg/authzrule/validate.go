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

// Package authzrule owns the write path for authorization rules: the CRUD the
// /v1/agent/authorizationrules API exposes, the validation that keeps a malformed rule out
// of the table, and the reload that makes a written rule take effect on the
// next request instead of at the next periodic refresh.
//
// It deliberately does not make the rule table a cached AI resource: rules are
// read by the OPA rule store alone, so they use the standard table store
// directly rather than the snapshot decorator. The narrow Store interface
// below is satisfied by the same *sqlstore.Store.
package authzrule

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// nameMaxLen mirrors the column size on AuthzRuleRecord.Name. A longer value
// would be truncated or rejected by the driver rather than by the API.
const nameMaxLen = 128

// MaxSubjectsPerRule bounds one policy's principal list. A policy is not a
// bulk-import format: 3000 people belong in a user group, and the policy names
// the group. The cap keeps an accidental paste from turning one object into a
// megabyte-scale watch event.
const MaxSubjectsPerRule = 100

// ValidationError reports one rejected field. It carries the field name so the
// handler can name it in the 400 — "effect must be one of ..." is actionable,
// "invalid request" is not.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// validSubjectTypes is the vocabulary the Rego policy actually interprets for
// subject_type (see policy/authz.rego subject_matches): the generic branch
// compares it to input.subject.kind ("user"/"group"/"serviceaccount"), plus the
// two dedicated branches for "*" (any) and "namespace" (workspace inheritance).
var validSubjectTypes = map[string]bool{
	string(authz.SubjectKindUser):           true,
	string(authz.SubjectKindGroup):          true,
	string(authz.SubjectKindServiceAccount): true,
	"namespace":                             true,
	"*":                                     true,
}

// validResourceKinds is the set policy/authz.rego resource_matches can match:
// the kinds the Coordinator asks about (agent/workspace/skill/mcp/
// knowledgebase/tool) plus the CMDB/resource kinds (service/cluster/namespace),
// and the "*" wildcard. ResourceKindNone ("") is not a rule.
var validResourceKinds = map[string]bool{
	string(authz.ResourceKindAgent):         true,
	string(authz.ResourceKindMCP):           true,
	string(authz.ResourceKindTool):          true,
	string(authz.ResourceKindService):       true,
	string(authz.ResourceKindCluster):       true,
	string(authz.ResourceKindK8sNamespace):  true,
	string(authz.ResourceKindSkill):         true,
	string(authz.ResourceKindKnowledgeBase): true,
	string(authz.ResourceKindNamespace):     true,
	string(authz.ResourceKindRole):          true,
	// The pre-rename spelling stays writable for one release so a caller built
	// before the rename can still write a namespace grant; the rule store
	// normalizes it when the policy document is built.
	string(authz.LegacyResourceKindWorkspace): true,
	"*": true,
}

// validVerbs mirrors policy/authz.rego verb_matches: the rule's verbs are
// compared to input.verb, which is always authz.VerbRead or authz.VerbWrite.
var validVerbs = map[string]bool{
	string(authz.VerbRead):  true,
	string(authz.VerbWrite): true,
}

// validEffects mirrors policy/authz.rego: effect is compared to the literal
// "allow"/"deny".
var validEffects = map[string]bool{"allow": true, "deny": true}

// validStatuses mirrors model.AuthzRuleM.Enabled: only "disabled" is special,
// but the API accepts the two named values so a typo is rejected rather than
// silently treated as enabled.
var validStatuses = map[string]bool{
	model.StatusEnabled:  true,
	model.StatusDisabled: true,
}

// Validate rejects a rule the policy would misinterpret. The policy is
// fail-closed, so a malformed rule does not merely do nothing — an effect of
// "denny", an unknown subject_type, or attributes that fall back to "no
// filter" can deny everyone or grant far more than intended. Every check below
// corresponds to a way the stored value would not mean what its author meant.
func Validate(r *model.AuthzRuleM) error {
	if r == nil {
		return &ValidationError{Field: "authzRule", Message: "rule is required"}
	}
	if strings.TrimSpace(r.Name) == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	if len(r.Name) > nameMaxLen {
		return &ValidationError{Field: "name", Message: fmt.Sprintf("name must be at most %d characters", nameMaxLen)}
	}
	subjects := r.EffectiveSubjects()
	if err := validateSubjects(subjects); err != nil {
		return err
	}
	resources := r.EffectiveResources()
	if err := validateResources(resources); err != nil {
		return err
	}
	if r.SubjectNamespace != "" {
		namespaceSubjects := 0
		for _, subject := range subjects {
			if strings.EqualFold(strings.TrimSpace(subject.Kind), "namespace") {
				namespaceSubjects++
			}
		}
		if namespaceSubjects > 1 {
			return &ValidationError{
				Field:   "subjects",
				Message: "at most one namespace subject may be named; the object inherits through one namespace",
			}
		}
	}
	if err := validateAttributes(r.ResourceAttributes); err != nil {
		return err
	}
	// AgentNames is optional (empty means "not agent-scoped"), but when present
	// it must be a real scope: an empty entry or a duplicate is a typo that
	// would make the rule narrower or broader than its author meant, and a rule
	// that silently does not apply is indistinguishable from a missing one.
	if hasAnyNonEmpty(r.AgentNames) {
		if err := validateList("agentNames", r.AgentNames); err != nil {
			return err
		}
		if dup := firstDuplicate(r.AgentNames); dup != "" {
			return &ValidationError{Field: "agentNames", Message: fmt.Sprintf("agentNames contains %q more than once", dup)}
		}
	}
	if len(r.Verbs) == 0 {
		return &ValidationError{Field: "verbs", Message: "verbs must contain at least one of read, write"}
	}
	for _, v := range r.Verbs {
		if !validVerbs[v] {
			return &ValidationError{Field: "verbs", Message: fmt.Sprintf("verb %q is invalid; must be one of %s", v, sortedKeys(validVerbs))}
		}
	}
	if !validEffects[r.Effect] {
		return &ValidationError{Field: "effect", Message: fmt.Sprintf("effect must be one of %s", sortedKeys(validEffects))}
	}
	if !validStatuses[r.Status] {
		return &ValidationError{Field: "status", Message: fmt.Sprintf("status must be one of %s", sortedKeys(validStatuses))}
	}
	return nil
}

// validateSubjects checks the canonical principal list. The plural shape is
// always present on a validated model (conversion fills it from the singular
// fields), so this is the one place the subject vocabulary is enforced.
func validateSubjects(subjects []model.AuthzSubject) error {
	if len(subjects) == 0 {
		return &ValidationError{Field: "subjects", Message: "a rule must name at least one subject"}
	}
	if len(subjects) > MaxSubjectsPerRule {
		return &ValidationError{
			Field:   "subjects",
			Message: fmt.Sprintf("a rule may name at most %d subjects; put larger sets in a user group", MaxSubjectsPerRule),
		}
	}
	for i, subject := range subjects {
		kind := strings.ToLower(strings.TrimSpace(subject.Kind))
		if kind == "*" {
			continue
		}
		if !validSubjectTypes[kind] {
			return &ValidationError{
				Field:   fmt.Sprintf("subjects[%d].kind", i),
				Message: fmt.Sprintf("subject kind %q is invalid; must be one of %s", subject.Kind, sortedKeys(validSubjectTypes)),
			}
		}
		if strings.TrimSpace(subject.Name) == "" {
			return &ValidationError{
				Field:   fmt.Sprintf("subjects[%d].name", i),
				Message: "a subject must name a principal",
			}
		}
	}
	return nil
}

// validateResources checks the canonical resource list.
func validateResources(resources []model.AuthzResource) error {
	if len(resources) == 0 {
		return &ValidationError{Field: "resources", Message: "a rule must name at least one resource kind"}
	}
	for i, resource := range resources {
		kind := strings.TrimSpace(resource.Kind)
		if !validResourceKinds[kind] {
			return &ValidationError{
				Field:   fmt.Sprintf("resources[%d].kind", i),
				Message: fmt.Sprintf("resource kind %q is invalid; must be one of %s", resource.Kind, sortedKeys(validResourceKinds)),
			}
		}
		if err := validateList(fmt.Sprintf("resources[%d].names", i), resource.Names); err != nil {
			return err
		}
	}
	return nil
}

// validateList requires at least one non-empty element. An empty list (or one
// holding only "") matches nothing in the policy, so writing it would create a
// rule that looks like a grant and grants nothing.
func validateList(field string, values []string) error {
	if len(values) == 0 {
		return &ValidationError{Field: field, Message: field + " must contain at least one value"}
	}
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return &ValidationError{Field: field, Message: field + " must not contain an empty value"}
		}
	}
	return nil
}

// validateAttributes accepts an empty string (no filter) or a JSON object whose
// values are strings or arrays of strings.
//
// Anything else is rejected: rulesToRegoData silently turns malformed JSON into
// "no filter", which widens the rule to every resource of its kind — the
// opposite of what a fail-safe write should do. Scalar membership is checked
// too, because the policy's normalize_to_list only understands a string or an
// array (authz.rego): a number, boolean or null value makes the attribute match
// undefined, so the rule — including a deny rule — would simply never apply.
func validateAttributes(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil || attrs == nil {
		return &ValidationError{Field: "resourceAttributes", Message: "resourceAttributes must be a JSON object, e.g. {\"env\":\"test\"}"}
	}
	for key, value := range attrs {
		switch v := value.(type) {
		case string:
		case []any:
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return &ValidationError{
						Field:   "resourceAttributes",
						Message: fmt.Sprintf("resourceAttributes[%q] must be a string or an array of strings", key),
					}
				}
			}
		default:
			return &ValidationError{
				Field:   "resourceAttributes",
				Message: fmt.Sprintf("resourceAttributes[%q] must be a string or an array of strings", key),
			}
		}
	}
	return nil
}

// hasAnyNonEmpty reports whether values holds a non-empty string. It keeps the
// "empty list means unscoped" convention from tripping validateList, which
// requires at least one element for the fields where that is mandatory.
func hasAnyNonEmpty(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// firstDuplicate returns the first value that appears twice, or "".
func firstDuplicate(values []string) string {
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if seen[v] {
			return v
		}
		seen[v] = true
	}
	return ""
}

// sortedKeys renders a value set for an error message in a stable order.
func sortedKeys(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	// Small sets; insertion sort keeps the package free of a sort import for
	// what is a handful of elements. Stable output matters because this string
	// is asserted in tests.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return strings.Join(keys, ", ")
}
