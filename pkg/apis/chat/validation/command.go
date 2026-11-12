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
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	chat "github.com/onexstack/onexai/pkg/apis/chat"
)

const (
	// maxCommandAliases caps how many extra names one command may answer to.
	maxCommandAliases = 16
	// maxCommandUsage bounds the human-facing usage text.
	maxCommandUsage = 256
)

// ValidateCommand checks a catalogue entry before it is persisted.
func ValidateCommand(spec *chat.CommandSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Group == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("group"),
			"a command must belong to a catalogue group"))
	} else {
		for _, msg := range validation.IsDNS1123Label(spec.Group) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("group"), spec.Group, msg))
		}
	}

	if len(spec.Aliases) > maxCommandAliases {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("aliases"), len(spec.Aliases), maxCommandAliases))
	}
	for i, alias := range spec.Aliases {
		aliasPath := fldPath.Child("aliases").Index(i)
		if alias == "" {
			allErrs = append(allErrs, field.Required(aliasPath, "an alias must not be empty"))
			continue
		}
		for _, msg := range validation.IsDNS1123Label(alias) {
			allErrs = append(allErrs, field.Invalid(aliasPath, alias, msg))
		}
	}

	if len(spec.Usage) > maxCommandUsage {
		allErrs = append(allErrs, field.TooLong(fldPath.Child("usage"), "", maxCommandUsage))
	}

	return allErrs
}
