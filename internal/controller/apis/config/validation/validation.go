// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package validation

import (
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	componentbasevalidation "k8s.io/component-base/config/validation"

	"github.com/onexstack/onexai/internal/controller/apis/config"
	genericvalidation "github.com/onexstack/onexai/pkg/config/validation"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// Validate ensures validation of the MinerControllerConfiguration struct.
func Validate(cc *config.OneXControllerManagerConfiguration) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, componentbasevalidation.ValidateLeaderElectionConfiguration(&cc.Generic.LeaderElection, field.NewPath("generic", "leaderElection"))...)
	// CoreDBConfiguration's contract is that an empty address disables the block;
	// a controller-manager that does not use a database must not be required to
	// invent credentials for it.
	allErrs = append(allErrs, genericvalidation.ValidateCoreDBConfiguration(&cc.CoreDB, field.NewPath("coredb"))...)
	allErrs = append(allErrs, genericvalidation.ValidateGenericControllerManagerConfiguration(&cc.Generic, field.NewPath("generic"))...)
	allErrs = append(allErrs, validatePromptAggregation(&cc.PromptAggregation, field.NewPath("promptAggregation"))...)
	allErrs = append(allErrs, validateMirror(&cc.Mirror, field.NewPath("mirror"))...)
	if cc.Mirror.Enabled && cc.CoreDB.Addr == "" {
		allErrs = append(allErrs, field.Required(field.NewPath("coredb", "addr"),
			"the resource mirror is enabled and needs a database"))
	}

	return allErrs
}

// validateMirror checks the projection section.
//
// An enabled projection with no database is rejected rather than silently
// skipped: the operator declared that reads may depend on those rows, and
// starting without them would serve an empty picture.
func validateMirror(cfg *config.MirrorConfiguration, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if cfg.BatchSize < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("batchSize"), cfg.BatchSize, "must not be negative"))
	}
	if cfg.FlushInterval.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("flushInterval"), cfg.FlushInterval, "must not be negative"))
	}
	if cfg.SweepInterval.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("sweepInterval"), cfg.SweepInterval, "must not be negative"))
	}
	// Same rule as the writer, which is the only place the name reaches SQL: the
	// default schema is `mirror`, so an RFC 1123 label here would reject
	// the value the component ships with.
	if err := readmodel.ValidateSchema(cfg.Schema); err != nil && cfg.Schema != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("schema"), cfg.Schema, err.Error()))
	}
	return allErrs
}

// ValidatePromptAggregation validates the prompt aggregation section on its
// own, so the options path can check it without pulling in every other
// component's validation.
func ValidatePromptAggregation(cfg *config.PromptAggregationConfiguration, fldPath *field.Path) field.ErrorList {
	return validatePromptAggregation(cfg, fldPath)
}

func validatePromptAggregation(cfg *config.PromptAggregationConfiguration, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if cfg.MaxTokens < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxTokens"), cfg.MaxTokens, "must not be negative"))
	}
	if cfg.MaxInputTokens < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxInputTokens"), cfg.MaxInputTokens, "must not be negative"))
	}
	if cfg.Timeout.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("timeout"), cfg.Timeout, "must not be negative"))
	}
	if cfg.Model != "" {
		for _, msg := range validation.IsDNS1123Subdomain(cfg.Model) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("model"), cfg.Model, msg))
		}
	}
	// AES accepts exactly 16, 24 or 32 bytes. Catching it here names the field
	// instead of surfacing a decryption failure much later, during reconcile.
	switch len(cfg.ModelAPIKeyEncryptionKey) {
	case 0, 16, 24, 32:
	default:
		allErrs = append(allErrs, field.Invalid(fldPath.Child("modelAPIKeyEncryptionKey"),
			"<redacted>", "must be 16, 24 or 32 bytes"))
	}
	return allErrs
}
