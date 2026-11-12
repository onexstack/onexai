// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package validation

import (
	"encoding/json"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// KnownResourceKinds are the resource kinds the Rego policy understands. "*"
// means any kind. Keep this list in step with internal/agent/pkg/authzrule's
// validResourceKinds: a kind the lower layer accepts but this one rejects is a
// grant the API refuses to write.
var KnownResourceKinds = []string{
	"agent", "mcp", "tool", "skill", "knowledgebase", "workspace", "namespace",
	"service", "cluster", "k8s-namespace", "role", "*",
}

// MaxSubjectsPerRule bounds one policy's principal list.
//
// A policy is not a bulk-import format: 3000 people belong in a user group, and
// the policy names the group. The cap keeps an accidental paste from turning
// one object into a megabyte-scale watch event that every replica carries.
const MaxSubjectsPerRule = 100

// KnownVerbs are the operations a rule may grant. The loader maps the
// Kubernetes read/write verbs onto the policy's read/write before evaluation.
var KnownVerbs = []string{
	"get", "list", "watch", "create", "update", "patch", "delete", "use", "*",
}

// subjectKinds are the principal kinds a rule may name.
var subjectKinds = map[string]bool{
	rbacv1.UserKind:           true,
	rbacv1.GroupKind:          true,
	rbacv1.ServiceAccountKind: true,
}

// ValidateAuthorizationRule checks a rule before it is persisted.
//
// The checks are deliberately fail-closed: a rule that names no subject, no
// resource or no verb is rejected rather than accepted as a no-op, because an
// accepted-but-inert rule reads like a grant to whoever wrote it.
func ValidateAuthorizationRule(spec *agent.AuthorizationRuleSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if len(spec.Subjects) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("subjects"),
			"a rule must name at least one subject"))
	}
	if len(spec.Subjects) > MaxSubjectsPerRule {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("subjects"), len(spec.Subjects), MaxSubjectsPerRule))
	}
	for i, subject := range spec.Subjects {
		allErrs = append(allErrs, validateSubject(subject, fldPath.Child("subjects").Index(i))...)
	}

	if spec.SubjectNamespace != "" {
		for _, msg := range validation.IsDNS1123Label(spec.SubjectNamespace) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("subjectNamespace"), spec.SubjectNamespace, msg))
		}
	}

	for i, name := range spec.AgentNames {
		for _, msg := range validation.IsDNS1123Subdomain(name) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("agentNames").Index(i), name, msg))
		}
	}
	if spec.AgentSelector != nil {
		if _, err := metav1.LabelSelectorAsSelector(spec.AgentSelector); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("agentSelector"), spec.AgentSelector, err.Error()))
		}
	}

	// Either the single-resource field or the list must be present. Both are
	// complete statements; when both are set their union is the rule.
	if spec.Resource.Kind == "" && len(spec.Resources) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("resource", "kind"),
			"a rule must name the resource kind it addresses"))
	}
	if spec.Resource.Kind != "" {
		allErrs = append(allErrs, validateResource(spec.Resource, fldPath.Child("resource"))...)
	}
	for i, resource := range spec.Resources {
		allErrs = append(allErrs, validateResource(resource, fldPath.Child("resources").Index(i))...)
	}

	if len(spec.Verbs) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("verbs"),
			"a rule must grant at least one verb"))
	}
	for i, verb := range spec.Verbs {
		if !contains(KnownVerbs, verb) {
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("verbs").Index(i), verb, KnownVerbs))
		}
	}

	switch spec.Effect {
	case agent.EffectAllow, agent.EffectDeny:
	default:
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("effect"), spec.Effect,
			[]string{string(agent.EffectAllow), string(agent.EffectDeny)}))
	}

	allErrs = append(allErrs, validateAttributes(spec.Attributes, fldPath.Child("attributes"))...)

	return allErrs
}

// validateResource checks one resource entry. It is shared by the single and
// list shapes so the two cannot disagree about what a valid target is.
func validateResource(resource agent.AuthorizationResource, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if resource.Kind == "" {
		return field.ErrorList{field.Required(fldPath.Child("kind"),
			"a rule must name the resource kind it addresses")}
	}
	if !contains(KnownResourceKinds, resource.Kind) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("kind"), resource.Kind, KnownResourceKinds))
	}
	if len(resource.Names) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("names"),
			`a rule must name at least one resource, or "*" for every resource of that kind`))
	}
	for i, name := range resource.Names {
		if name == "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("names").Index(i), name, "must not be empty"))
		}
	}
	return allErrs
}

func validateSubject(subject rbacv1.Subject, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if !subjectKinds[subject.Kind] {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("kind"), subject.Kind,
			[]string{rbacv1.UserKind, rbacv1.GroupKind, rbacv1.ServiceAccountKind}))
	}
	if subject.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "a subject must name a principal"))
	}
	if subject.Kind == rbacv1.ServiceAccountKind && subject.Namespace == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("namespace"),
			"a ServiceAccount subject must name its namespace"))
	}
	return allErrs
}

// validateAttributes checks that the attribute filter is a JSON object whose
// values are either a string or a list of strings; the Rego policy treats a
// value of any other shape as a mismatch, which would silently narrow a grant.
func validateAttributes(attributes *runtime.RawExtension, fldPath *field.Path) field.ErrorList {
	if attributes == nil || len(attributes.Raw) == 0 {
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(attributes.Raw, &decoded); err != nil {
		return field.ErrorList{field.Invalid(fldPath, string(attributes.Raw),
			fmt.Sprintf("must be a JSON object: %v", err))}
	}

	var allErrs field.ErrorList
	for key, value := range decoded {
		switch typed := value.(type) {
		case string:
		case []any:
			for i, item := range typed {
				if _, ok := item.(string); !ok {
					allErrs = append(allErrs, field.Invalid(fldPath.Key(key).Index(i), item,
						"attribute list values must be strings"))
				}
			}
		default:
			allErrs = append(allErrs, field.Invalid(fldPath.Key(key), value,
				"attribute values must be a string or a list of strings"))
		}
	}
	return allErrs
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
