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

package conversion

import (
	"strings"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// AuthzRuleV1ToAuthzRuleModel converts a proto authorization rule to the domain
// model.
//
// Both shapes are accepted. The result always carries the plural form (filled
// from the singular fields when the caller used those) and the singular form
// whenever the plural set can be expressed as one kind, so every reader of the
// model sees one statement rather than two that might disagree.
func AuthzRuleV1ToAuthzRuleModel(r *v1.AuthzRule) *model.AuthzRuleM {
	if r == nil {
		return &model.AuthzRuleM{}
	}
	m := &model.AuthzRuleM{
		Name:               r.GetName(),
		SubjectType:        r.GetSubjectType(),
		SubjectID:          r.GetSubjectIds(),
		ResourceKind:       r.GetResourceKind(),
		ResourceName:       r.GetResourceNames(),
		ResourceAttributes: r.GetResourceAttributes(),
		AgentNames:         r.GetAgentNames(),
		Verbs:              r.GetVerbs(),
		Effect:             r.GetEffect(),
		Status:             r.GetStatus(),
		Description:        r.GetDescription(),
		Owner:              r.GetOwner(),
		CreatedAt:          r.GetCreatedAt(),
		UpdatedAt:          r.GetUpdatedAt(),
		SubjectNamespace:   r.GetSubjectNamespace(),
	}
	if len(r.GetSubjects()) > 0 {
		m.Subjects = make([]model.AuthzSubject, 0, len(r.GetSubjects()))
		for _, subject := range r.GetSubjects() {
			m.Subjects = append(m.Subjects, model.AuthzSubject{
				Kind:      subject.GetKind(),
				Name:      subject.GetName(),
				Namespace: subject.GetNamespace(),
			})
		}
	}
	if len(r.GetResources()) > 0 {
		m.Resources = make([]model.AuthzResource, 0, len(r.GetResources()))
		for _, resource := range r.GetResources() {
			m.Resources = append(m.Resources, model.AuthzResource{
				Kind:  resource.GetKind(),
				Names: splitOptionalList(resource.GetNames()),
			})
		}
	}

	// Canonicalize: fill the plural list from the singular fields, then the
	// singular fields from the plural list. A legacy wildcard subject becomes
	// the object's group "*" spelling, which is how the migration encoded it.
	m.Subjects = normalizeSubjects(m.EffectiveSubjects())
	m.Resources = normalizeResources(m.EffectiveResources())
	m.NormalizeLegacyFields()
	return m
}

// AuthzRuleModelToAuthzRuleV1 converts the domain model back to the wire shape.
func AuthzRuleModelToAuthzRuleV1(m *model.AuthzRuleM) *v1.AuthzRule {
	if m == nil {
		return nil
	}
	m.NormalizeLegacyFields()

	subjects := make([]*v1.AuthzSubject, 0, len(m.Subjects))
	for _, subject := range m.Subjects {
		subjects = append(subjects, &v1.AuthzSubject{
			Kind:      subject.Kind,
			Name:      subject.Name,
			Namespace: subject.Namespace,
		})
	}
	resources := make([]*v1.AuthzResource, 0, len(m.Resources))
	for _, resource := range m.Resources {
		resources = append(resources, &v1.AuthzResource{
			Kind:  resource.Kind,
			Names: append([]string(nil), resource.Names...),
		})
	}

	return &v1.AuthzRule{
		Name:               m.Name,
		SubjectType:        m.SubjectType,
		SubjectIds:         []string(m.SubjectID),
		ResourceKind:       m.ResourceKind,
		ResourceNames:      []string(m.ResourceName),
		ResourceAttributes: m.ResourceAttributes,
		AgentNames:         m.AgentNames,
		Verbs:              m.Verbs,
		Effect:             m.Effect,
		Status:             StatusToV1(m.Status),
		Description:        m.Description,
		Owner:              m.Owner,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
		SubjectNamespace:   m.SubjectNamespace,
		Subjects:           subjects,
		Resources:          resources,
	}
}

// normalizeSubjects keeps the legacy wildcard spelling working in the plural
// shape: the CRD can only carry User/Group/ServiceAccount subjects, so the row's
// subject_type "*" is the group "*" the migration writes.
func normalizeSubjects(in []model.AuthzSubject) []model.AuthzSubject {
	out := make([]model.AuthzSubject, 0, len(in))
	for _, subject := range in {
		subject.Kind = strings.TrimSpace(subject.Kind)
		subject.Name = strings.TrimSpace(subject.Name)
		if subject.Kind == "*" {
			// The legacy any-subject spelling. The object model matches a
			// subject kind before the name, so "any kind" has to become one
			// entry per kind: a single Group "*" would only ever match group
			// subjects.
			for _, kind := range []string{"User", "Group", "ServiceAccount"} {
				expanded := subject
				expanded.Kind = kind
				if kind == "ServiceAccount" && expanded.Namespace == "" {
					expanded.Namespace = "*"
				}
				out = append(out, expanded)
			}
			continue
		}
		out = append(out, subject)
	}
	return out
}

func normalizeResources(in []model.AuthzResource) []model.AuthzResource {
	out := make([]model.AuthzResource, 0, len(in))
	for _, resource := range in {
		resource.Kind = strings.TrimSpace(resource.Kind)
		resource.Names = splitOptionalList(resource.Names)
		out = append(out, resource)
	}
	return out
}

// splitOptionalList trims entries and keeps order, dropping empties. It exists
// because a client that sends `names: []` means "I did not fill this", not
// "match nothing" — validation rejects the latter.
func splitOptionalList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
