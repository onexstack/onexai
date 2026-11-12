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

package authzrule

import (
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
)

// TestValidateRejectsMalformedRules pins which field each rejected shape is
// blamed on. The policy is fail-closed, so a typo in effect, subject_type or
// resource_kind, or attributes that fall back to "no filter", does not merely
// fail to match — it can deny everyone or widen a grant. Each case below is a
// way a stored value would not mean what its author meant.
func TestValidateRejectsMalformedRules(t *testing.T) {
	base := func() *model.AuthzRuleM {
		return &model.AuthzRuleM{
			Name:         "rule-1",
			SubjectType:  "user",
			SubjectID:    []string{"u_alice"},
			ResourceKind: "agent",
			ResourceName: []string{"default"},
			Verbs:        []string{"read"},
			Effect:       "allow",
			Status:       model.StatusEnabled,
		}
	}

	tests := []struct {
		name      string
		mutate    func(*model.AuthzRuleM)
		wantField string
	}{
		{name: "empty name", mutate: func(r *model.AuthzRuleM) { r.Name = "" }, wantField: "name"},
		{name: "name too long", mutate: func(r *model.AuthzRuleM) { r.Name = string(make([]byte, nameMaxLen+1)) }, wantField: "name"},
		{name: "unknown subject type", mutate: func(r *model.AuthzRuleM) { r.SubjectType = "person" }, wantField: "subjects[0].kind"},
		{name: "empty subject id", mutate: func(r *model.AuthzRuleM) { r.SubjectID = nil }, wantField: "subjects"},
		{name: "blank subject id element", mutate: func(r *model.AuthzRuleM) { r.SubjectID = []string{""} }, wantField: "subjects[0].name"},
		{name: "unknown resource kind", mutate: func(r *model.AuthzRuleM) { r.ResourceKind = "Agent" }, wantField: "resources[0].kind"},
		{name: "empty resource name", mutate: func(r *model.AuthzRuleM) { r.ResourceName = nil }, wantField: "resources[0].names"},
		{name: "blank resource name element", mutate: func(r *model.AuthzRuleM) { r.ResourceName = []string{" "} }, wantField: "resources[0].names"},
		{name: "no verbs", mutate: func(r *model.AuthzRuleM) { r.Verbs = nil }, wantField: "verbs"},
		{name: "unknown verb", mutate: func(r *model.AuthzRuleM) { r.Verbs = []string{"execute"} }, wantField: "verbs"},
		{name: "unknown effect", mutate: func(r *model.AuthzRuleM) { r.Effect = "denny" }, wantField: "effect"},
		{name: "empty effect", mutate: func(r *model.AuthzRuleM) { r.Effect = "" }, wantField: "effect"},
		{name: "unknown status", mutate: func(r *model.AuthzRuleM) { r.Status = "on" }, wantField: "status"},
		{name: "empty status", mutate: func(r *model.AuthzRuleM) { r.Status = "" }, wantField: "status"},
		{name: "attributes not JSON", mutate: func(r *model.AuthzRuleM) { r.ResourceAttributes = "env=test" }, wantField: "resourceAttributes"},
		{name: "attributes JSON array", mutate: func(r *model.AuthzRuleM) { r.ResourceAttributes = `["env"]` }, wantField: "resourceAttributes"},
		{name: "attributes JSON scalar", mutate: func(r *model.AuthzRuleM) { r.ResourceAttributes = `"prod"` }, wantField: "resourceAttributes"},
		{name: "attributes JSON null", mutate: func(r *model.AuthzRuleM) { r.ResourceAttributes = `null` }, wantField: "resourceAttributes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.mutate(r)
			err := Validate(r)
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("got %v, want a ValidationError", err)
			}
			if verr.Field != tt.wantField {
				t.Fatalf("blamed field %q, want %q (%v)", verr.Field, tt.wantField, verr)
			}
		})
	}
}

// TestValidateAcceptsThePolicyVocabulary is the other half: the values the Rego
// policy actually interprets must all pass, including the wildcards and the
// namespace-inheritance subject type.
func TestValidateAcceptsThePolicyVocabulary(t *testing.T) {
	for _, subjectType := range []string{"user", "group", "serviceaccount", "namespace", "*"} {
		for _, resourceKind := range []string{"agent", "mcp", "tool", "skill", "knowledgebase", "workspace", "namespace", "service", "cluster", "*"} {
			r := &model.AuthzRuleM{
				Name:         "accepted",
				SubjectType:  subjectType,
				SubjectID:    []string{"*"},
				ResourceKind: resourceKind,
				ResourceName: []string{"*"},
				Verbs:        []string{"read", "write"},
				Effect:       "deny",
				Status:       model.StatusDisabled,
			}
			if err := Validate(r); err != nil {
				t.Fatalf("subjectType=%q resourceKind=%q rejected: %v", subjectType, resourceKind, err)
			}
		}
	}
}

func TestValidateAcceptsEmptyOrObjectAttributes(t *testing.T) {
	r := &model.AuthzRuleM{
		Name: "attrs", SubjectType: "user", SubjectID: []string{"u"},
		ResourceKind: "tool", ResourceName: []string{"t"},
		Verbs: []string{"write"}, Effect: "allow", Status: model.StatusEnabled,
	}
	if err := Validate(r); err != nil {
		t.Fatalf("empty attributes must mean no filter, got %v", err)
	}
	r.ResourceAttributes = `{"env":"prod","cluster":["a","b"]}`
	if err := Validate(r); err != nil {
		t.Fatalf("object attributes rejected: %v", err)
	}
}

func TestValidateRejectsNilRule(t *testing.T) {
	var verr *ValidationError
	if err := Validate(nil); !errors.As(err, &verr) {
		t.Fatalf("nil rule: got %v, want ValidationError", err)
	}
}

// TestValidateRejectsAttributesThePolicyCannotEvaluate pins the value types the
// Rego policy cannot interpret. normalize_to_list only understands a string or
// an array, so a number, boolean, null or nested object makes the attribute
// comparison undefined and the rule never applies — including a deny rule,
// which is the direction that fails open.
func TestValidateRejectsAttributesThePolicyCannotEvaluate(t *testing.T) {
	base := func() *model.AuthzRuleM {
		return &model.AuthzRuleM{
			Name: "attrs", SubjectType: "user", SubjectID: []string{"u"},
			ResourceKind: "tool", ResourceName: []string{"t"},
			Verbs: []string{"read"}, Effect: "deny", Status: model.StatusEnabled,
		}
	}

	for _, raw := range []string{
		`{"replicas":3}`,
		`{"enabled":true}`,
		`{"owner":null}`,
		`{"nested":{"env":"prod"}}`,
		`{"mixed":["a",1]}`,
	} {
		r := base()
		r.ResourceAttributes = raw
		err := Validate(r)
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.Field != "resourceAttributes" {
			t.Fatalf("attributes %s: got %v, want a resourceAttributes ValidationError", raw, err)
		}
	}
}

// TestValidateAgentNames covers the optional agent scope. Empty means "not
// scoped" and must stay valid; a present scope must be a real one.
func TestValidateAgentNames(t *testing.T) {
	base := func() *model.AuthzRuleM {
		return &model.AuthzRuleM{
			Name: "scoped", SubjectType: "user", SubjectID: []string{"u"},
			ResourceKind: "tool", ResourceName: []string{"t"},
			Verbs: []string{"read"}, Effect: "deny", Status: model.StatusEnabled,
		}
	}

	r := base()
	if err := Validate(r); err != nil {
		t.Fatalf("empty agentNames must mean unscoped: %v", err)
	}

	r.AgentNames = []string{"a", "b"}
	if err := Validate(r); err != nil {
		t.Fatalf("well-formed agentNames rejected: %v", err)
	}

	r.AgentNames = []string{"a", "a"}
	err := Validate(r)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "agentNames" {
		t.Fatalf("duplicate agent name: got %v, want an agentNames ValidationError", err)
	}

	r.AgentNames = []string{"a", "  "}
	if err := Validate(r); !errors.As(err, &verr) || verr.Field != "agentNames" {
		t.Fatalf("blank agent name: got %v, want an agentNames ValidationError", err)
	}
}
