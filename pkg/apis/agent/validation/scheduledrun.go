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
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// ValidateScheduledRun checks one cron schedule.
func ValidateScheduledRun(spec *agent.ScheduledRunSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Agent.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("agent", "name"), "a schedule must name an agent"))
	}
	if spec.Agent.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("agent", "namespace"), spec.Agent.Namespace,
			"a schedule runs an agent in its own namespace"))
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("prompt"), "a schedule must carry a prompt"))
	}
	if len(spec.Prompt) > 32*1024 {
		allErrs = append(allErrs, field.TooLong(fldPath.Child("prompt"), "", 32*1024))
	}

	fields := strings.Fields(spec.Schedule)
	if len(fields) != 5 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("schedule"), spec.Schedule,
			"must be a five-field cron expression"))
	} else {
		tz := spec.TimeZone
		if tz == "" {
			tz = "UTC"
		}
		if _, err := time.LoadLocation(tz); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("timeZone"), spec.TimeZone, err.Error()))
		} else if _, err := cron.ParseStandard("CRON_TZ=" + tz + " " + strings.Join(fields, " ")); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("schedule"), spec.Schedule, err.Error()))
		}
	}

	if spec.ExecutionTimeout != nil && spec.ExecutionTimeout.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("executionTimeout"),
			spec.ExecutionTimeout.Duration, "must be positive"))
	}

	return allErrs
}
