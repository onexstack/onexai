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
	"k8s.io/apimachinery/pkg/util/validation/field"
	componentbasevalidation "k8s.io/component-base/config/validation"

	"github.com/onexstack/onexai/pkg/config"
	"github.com/onexstack/onexai/pkg/util/validation"
)

// ValidateCoreDBConfiguration ensures validation of the CoreDBConfiguration
// struct.
//
// A block with no address disables the database entirely, which is the state a
// deployment that does not use it wants; only a half-configured block is an
// error.
func ValidateCoreDBConfiguration(cc *config.CoreDBConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if cc.Addr == "" {
		return allErrs
	}
	if cc.MaxIdleConnections <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxIdleConnections"), cc.MaxIdleConnections, "must be greater than zero"))
	}
	if cc.MaxOpenConnections <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxOpenConnections"), cc.MaxOpenConnections, "must be greater than zero"))
	}
	if cc.MaxConnectionLifeTime.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxConnectionLifeTime"), cc.MaxConnectionLifeTime, "must be greater than zero"))
	}
	if len(cc.Database) == 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("database"), cc.Database, "database is required"))
	}
	if len(cc.Password) == 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("password"), cc.Password, "password is required"))
	}
	return allErrs
}

// ValidateGenericControllerManagerConfiguration ensures validation of the GenericControllerManagerConfiguration struct.
func ValidateGenericControllerManagerConfiguration(cc *config.GenericControllerManagerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if cc.HealthzBindAddress != "" {
		allErrs = append(allErrs, validation.ValidateHostPort(cc.HealthzBindAddress, fldPath.Child("healthzBindAddress"))...)
	}
	if cc.PprofBindAddress != "" {
		allErrs = append(allErrs, validation.ValidateHostPort(cc.PprofBindAddress, fldPath.Child("pprofBindAddress"))...)
	}
	if cc.MetricsBindAddress != "" {
		allErrs = append(allErrs, validation.ValidateHostPort(cc.MetricsBindAddress, fldPath.Child("metricsBindAddress"))...)
	}
	if cc.MinResyncPeriod.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("minResyncPeriod"), cc.MinResyncPeriod, "must be greater than zero"))
	}
	allErrs = append(allErrs, componentbasevalidation.ValidateClientConnectionConfiguration(&cc.ClientConnection, fldPath.Child("clientConnection"))...)

	return allErrs
}
