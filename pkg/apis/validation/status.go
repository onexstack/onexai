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

// Package validation holds validation shared by every onexai API group.
// Group-specific rules live in pkg/apis/<group>/validation.
package validation

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateCommonStatus validates the fields every onexai resource status
// carries. It is called from the status subresource strategy, which is the only
// writer of these fields, so the checks guard against a buggy controller
// persisting nonsense rather than against user input.
func ValidateCommonStatus(
	conditions []metav1.Condition,
	observedGeneration int64,
	fldPath *field.Path,
) field.ErrorList {

	allErrs := metav1validation.ValidateConditions(conditions, fldPath.Child("conditions"))

	// ObservedGeneration is a count, so it can never be negative; a controller
	// that subtracts instead of assigning would otherwise store -1 forever.
	if observedGeneration < 0 {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("observedGeneration"), observedGeneration, "must be greater than or equal to 0",
		))
	}

	return allErrs
}
