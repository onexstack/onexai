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
	"context"
	"net/url"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// ValidateRuntimeProfileRules returns the rules for a runtime-profile payload.
func (v *Validator) ValidateRuntimeProfileRules() genericvalidation.Rules { return nameRules }

// ValidateCreateRuntimeProfileRequest validates a create request.
func (v *Validator) ValidateCreateRuntimeProfileRequest(_ context.Context, rq *v1.CreateRuntimeProfileRequest) error {
	if err := validatePayload(rq.GetRuntimeProfile(), "runtimeProfile", v.ValidateRuntimeProfileRules()); err != nil {
		return err
	}

	return validateRuntimeProfile(rq.GetRuntimeProfile())
}

// ValidateUpdateRuntimeProfileRequest validates an update request.
func (v *Validator) ValidateUpdateRuntimeProfileRequest(_ context.Context, rq *v1.UpdateRuntimeProfileRequest) error {
	if err := validatePayload(rq.GetRuntimeProfile(), "runtimeProfile", v.ValidateRuntimeProfileRules()); err != nil {
		return err
	}

	return validateRuntimeProfile(rq.GetRuntimeProfile())
}

// ValidateGetRuntimeProfileRequest validates a get request.
func (v *Validator) ValidateGetRuntimeProfileRequest(_ context.Context, rq *v1.GetRuntimeProfileRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateRuntimeProfileRules())
}

// ValidateDeleteRuntimeProfileRequest validates a delete request.
func (v *Validator) ValidateDeleteRuntimeProfileRequest(_ context.Context, rq *v1.DeleteRuntimeProfileRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateRuntimeProfileRules())
}

// ValidateListRuntimeProfileRequest validates a list request.
func (v *Validator) ValidateListRuntimeProfileRequest(_ context.Context, rq *v1.ListRuntimeProfileRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateRuntimeProfile mirrors the declarative runtime-profile rules. The
// profile is the contract a runtime offers, so a value the adapter cannot use
// is refused before it is stored rather than at the first run.
func validateRuntimeProfile(profile *v1.RuntimeProfile) error {
	if err := validateRuntimeProfilePlacement(profile); err != nil {
		return err
	}

	if err := validateModelAccess(profile.GetModelAccess()); err != nil {
		return err
	}

	if err := validateRuntimeProfileAuth(profile); err != nil {
		return err
	}

	return validateRuntimeProfileLimits(profile)
}

// validateRuntimeProfilePlacement checks the runtime kind and the address it
// needs.
func validateRuntimeProfilePlacement(profile *v1.RuntimeProfile) error {
	kind := profile.GetKind()
	if kind == "" {
		kind = agent.RuntimeKindA2A
	}

	switch kind {
	case agent.RuntimeKindInproc:
		return validateInprocPlacement(profile)
	case agent.RuntimeKindA2A:
		return validateA2APlacement(profile)
	default:
		return errno.ErrInvalidArgument.WithMessage(
			"kind %q is not supported (want %q or %q)", profile.GetKind(), agent.RuntimeKindInproc, agent.RuntimeKindA2A)
	}
}

// validateInprocPlacement checks that an in-process profile names no address.
func validateInprocPlacement(profile *v1.RuntimeProfile) error {
	if profile.GetEndpoint() != "" || profile.GetClientConfig() != nil || profile.GetHostingRef() != "" {
		return errno.ErrInvalidArgument.WithMessage(
			"an inproc profile has no endpoint and no hosting: it runs in this process")
	}

	return nil
}

// validateA2APlacement checks that an A2A profile names exactly one address.
func validateA2APlacement(profile *v1.RuntimeProfile) error {
	endpoint, hosting := profile.GetEndpoint(), profile.GetHostingRef()
	hasClientConfig := profile.GetClientConfig() != nil

	switch {
	case endpoint == "" && hosting == "" && !hasClientConfig:
		return errno.ErrInvalidArgument.WithMessage(
			"an a2a profile needs an endpoint, a clientConfig or a hostingRef")
	case (endpoint != "" || hasClientConfig) && hosting != "":
		return errno.ErrInvalidArgument.WithMessage(
			"endpoint/clientConfig and hostingRef are mutually exclusive")
	case endpoint != "":
		return validateHTTPEndpoint(endpoint)
	case hasClientConfig && profile.GetClientConfig().GetUrl() != "":
		return validateHTTPEndpoint(profile.GetClientConfig().GetUrl())
	default:
		return nil
	}
}

// validateModelAccess checks the model-access mode.
func validateModelAccess(access string) error {
	switch access {
	case "", agent.ModelAccessProxy, agent.ModelAccessCredential:
		return nil
	default:
		return errno.ErrInvalidArgument.WithMessage(
			"modelAccess %q is not supported (want %q or %q)",
			access, agent.ModelAccessProxy, agent.ModelAccessCredential)
	}
}

// validateRuntimeProfileAuth checks the outbound credential pairs.
func validateRuntimeProfileAuth(profile *v1.RuntimeProfile) error {
	if (profile.GetAuthBearerSecret() == "") != (profile.GetAuthBearerKey() == "") {
		return errno.ErrInvalidArgument.WithMessage("authBearerSecret and authBearerKey must be set together")
	}

	if (profile.GetAuthCASecret() == "") != (profile.GetAuthCAKey() == "") {
		return errno.ErrInvalidArgument.WithMessage("authCASecret and authCAKey must be set together")
	}

	return nil
}

// validateRuntimeProfileLimits checks the gateway-enforced bounds.
func validateRuntimeProfileLimits(profile *v1.RuntimeProfile) error {
	if profile.GetRunTimeoutNanos() < 0 {
		return errno.ErrInvalidArgument.WithMessage("runTimeoutNanos must not be negative")
	}

	if profile.GetMaxConcurrentRun() < 0 {
		return errno.ErrInvalidArgument.WithMessage("maxConcurrentRun must not be negative")
	}

	if profile.GetMaxToolCalls() < 0 {
		return errno.ErrInvalidArgument.WithMessage("maxToolCalls must not be negative")
	}

	return nil
}

// validateHTTPEndpoint rejects an endpoint the A2A client cannot dial.
func validateHTTPEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errno.ErrInvalidArgument.WithMessage("endpoint %q must be an absolute http or https URL", endpoint)
	}

	return nil
}
