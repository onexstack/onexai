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

package validation

import (
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// ValidateReferenceGrant checks a grant before it is persisted.
//
// The checks are fail-closed: a grant that names no referrer or no target is
// rejected rather than accepted as a no-op, because an accepted-but-inert
// grant reads like an authorization to whoever wrote it.
func ValidateReferenceGrant(spec *agent.ReferenceGrantSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if len(spec.From) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("from"),
			"a grant must name at least one referrer"))
	}
	for i, from := range spec.From {
		p := fldPath.Child("from").Index(i)
		if from.Group == "" {
			allErrs = append(allErrs, field.Required(p.Child("group"), "the referrer group is required"))
		} else if from.Group != sharing.GroupName {
			allErrs = append(allErrs, field.NotSupported(p.Child("group"), from.Group, []string{sharing.GroupName}))
		}
		if from.Kind == "" {
			allErrs = append(allErrs, field.Required(p.Child("kind"), "the referrer kind is required"))
		} else if from.Kind != sharing.KindAgent {
			// A grant answers "may this tenant's agents reach my object?".
			// Any other referrer kind would be persisted and never match.
			allErrs = append(allErrs, field.NotSupported(p.Child("kind"), from.Kind, []string{sharing.KindAgent}))
		}
		if from.Namespace == "" {
			allErrs = append(allErrs, field.Required(p.Child("namespace"), "the referrer namespace is required"))
		} else {
			for _, msg := range validation.IsDNS1123Label(from.Namespace) {
				allErrs = append(allErrs, field.Invalid(p.Child("namespace"), from.Namespace, msg))
			}
		}
	}

	if len(spec.To) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("to"),
			"a grant must name at least one target"))
	}
	for i, to := range spec.To {
		p := fldPath.Child("to").Index(i)
		if to.Group == "" {
			allErrs = append(allErrs, field.Required(p.Child("group"), "the target group is required"))
		} else if to.Group != sharing.GroupName {
			allErrs = append(allErrs, field.NotSupported(p.Child("group"), to.Group, []string{sharing.GroupName}))
		}
		if to.Kind == "" {
			allErrs = append(allErrs, field.Required(p.Child("kind"), "the target kind is required"))
		} else if !sharing.Shareable(to.Kind) {
			// A grant for a same-namespace-only kind reads like an
			// authorization but can never admit anything.
			allErrs = append(allErrs, field.Invalid(p.Child("kind"), to.Kind,
				"this kind is not shareable across namespaces"))
		}
		if to.Name != "" {
			for _, msg := range validation.IsDNS1123Subdomain(to.Name) {
				allErrs = append(allErrs, field.Invalid(p.Child("name"), to.Name, msg))
			}
		}
	}

	return allErrs
}
