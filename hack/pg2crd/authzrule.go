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

package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// allAgents is how the rule table says "every agent". The object says the same
// thing with an explicitly empty selector — an absent selector means "not
// agent-scoped", which is a different statement.
const allAgents = "*"

// verbsFor maps the row's two-verb vocabulary onto the API's Kubernetes verbs.
//
// The runtime's loader maps get|list|watch back to read and
// create|update|patch|delete back to write (see controlplane/health.go's
// neighbour, the rule source), so this round trip does not change what the
// policy evaluates.
var verbsFor = map[string][]string{
	"read":  {"get", "list", "watch"},
	"write": {"create", "update", "patch", "delete"},
}

// migrateAuthorizationRules converts the rule table into AuthorizationRules.
//
// Unlike every other kind here, there is no `XxxToCR` to reuse: the rule table
// was never served through the control-plane store (the runtime reads rules
// straight off the objects), so the mapping between the two shapes is written
// once, here.
func migrateAuthorizationRules(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	// Rules are read across every namespace (controlplane/rulesource.go lists
	// them cluster-wide), so the namespace is a container and not part of the
	// rule's meaning. They all go to the fallback namespace, which keeps them in
	// one place an operator can list.
	ns := place.fallback
	rows, err := find[agentmodel.AuthzRuleM](ctx, db)
	if err != nil {
		return fmt.Errorf("read authorization rules: %w", err)
	}

	converted := 0
	for i := range rows {
		row := &rows[i]
		objects, err := authzRuleToCRs(row, ns)
		if err != nil {
			return fmt.Errorf("authorization rule %q: %w", row.Name, err)
		}
		if len(objects) == 0 {
			fmt.Fprintf(stderr, "skip AuthorizationRule %s: it names no subject or no resource\n", row.Name)
			continue
		}
		for _, obj := range objects {
			if out.add("30-authorizationrules.yaml", "AuthorizationRule", ns, obj, existing) {
				converted++
			}
		}
	}
	fmt.Fprintf(stderr, "authorization rules: %d row(s) -> %d object(s)\n", len(rows), converted)
	return nil
}

// authzRuleToCRs turns one row into the objects that mean the same thing.
//
// One row is usually one object. It is more than one only for the inherited
// subject match: `subject_type: namespace` names a namespace to inherit through,
// and the object carries that as a single string, so a row listing two
// namespaces becomes two objects.
func authzRuleToCRs(row *agentmodel.AuthzRuleM, namespace string) ([]*agentv1alpha1.AuthorizationRule, error) {
	verbs, err := mapVerbs(row.Verbs)
	if err != nil {
		return nil, err
	}
	if len(verbs) == 0 || len(row.ResourceName) == 0 || row.ResourceKind == "" {
		return nil, nil
	}

	spec := agentv1alpha1.AuthorizationRuleSpec{
		Resource: agentv1alpha1.AuthorizationResource{
			Kind:  row.ResourceKind,
			Names: []string(row.ResourceName),
		},
		Verbs:       verbs,
		Effect:      effectOf(row.Effect),
		Enabled:     row.Status != agentmodel.StatusDisabled,
		Description: row.Description,
	}
	if row.ResourceAttributes != "" {
		spec.Attributes = &runtime.RawExtension{Raw: []byte(row.ResourceAttributes)}
	}
	agentNames := []string(row.AgentNames)
	if len(agentNames) == 1 && agentNames[0] == allAgents {
		spec.AgentSelector = &metav1.LabelSelector{}
	} else if len(agentNames) > 0 {
		spec.AgentNames = agentNames
	}

	subjectType := strings.ToLower(strings.TrimSpace(row.SubjectType))
	ids := []string(row.SubjectID)
	if len(ids) == 0 {
		return nil, nil
	}

	newObject := func(name string) *agentv1alpha1.AuthorizationRule {
		obj := &agentv1alpha1.AuthorizationRule{
			TypeMeta:   typeMeta("agent", "AuthorizationRule"),
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       spec,
		}
		return obj
	}

	switch subjectType {
	case "user", "group", "serviceaccount":
		spec.Subjects = subjectsOf(kindOf(subjectType), ids)
		return []*agentv1alpha1.AuthorizationRule{newObject(row.Name)}, nil

	case "*":
		// "any subject" stays a literal wildcard: the policy reads it as one, and
		// translating it to system:authenticated would quietly exclude the
		// principals that are not in that group.
		spec.Subjects = []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: allAgents}}
		return []*agentv1alpha1.AuthorizationRule{newObject(row.Name)}, nil

	case "namespace":
		objects := make([]*agentv1alpha1.AuthorizationRule, 0, len(ids))
		for i, ns := range ids {
			instance := spec
			instance.SubjectNamespace = ns
			name := row.Name
			if i > 0 {
				// Two namespaces in one row: the object can only inherit through
				// one, so the object that carries the second needs a name of its
				// own. Indexed rather than hashed so it stays readable.
				name = row.Name + "-" + strconv.Itoa(i+1)
			}
			obj := newObject(name)
			obj.Spec = instance
			objects = append(objects, obj)
		}
		return objects, nil

	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown subject type %q", row.SubjectType)
	}
}

func subjectsOf(kind string, ids []string) []rbacv1.Subject {
	out := make([]rbacv1.Subject, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out = append(out, rbacv1.Subject{Kind: kind, Name: id})
	}
	return out
}

func kindOf(subjectType string) string {
	switch subjectType {
	case "user":
		return rbacv1.UserKind
	case "group":
		return rbacv1.GroupKind
	case "serviceaccount":
		return rbacv1.ServiceAccountKind
	default:
		return rbacv1.UserKind
	}
}

// mapVerbs expands the row's verbs, keeping any verb that is already in the
// API's vocabulary as it is.
func mapVerbs(rowVerbs []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(rowVerbs)*3)
	add := func(verb string) {
		if _, ok := seen[verb]; ok {
			return
		}
		seen[verb] = struct{}{}
		out = append(out, verb)
	}

	for _, verb := range rowVerbs {
		verb = strings.ToLower(strings.TrimSpace(verb))
		if verb == "" {
			continue
		}
		if mapped, ok := verbsFor[verb]; ok {
			for _, m := range mapped {
				add(m)
			}
			continue
		}
		switch verb {
		case "get", "list", "watch", "create", "update", "patch", "delete", "use", "*":
			add(verb)
		default:
			return nil, fmt.Errorf("unknown verb %q", verb)
		}
	}
	return out, nil
}

func effectOf(effect string) agentv1alpha1.AuthorizationEffect {
	if strings.EqualFold(strings.TrimSpace(effect), string(agentv1alpha1.EffectDeny)) {
		return agentv1alpha1.EffectDeny
	}
	return agentv1alpha1.EffectAllow
}
