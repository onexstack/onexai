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
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// digestPinnedImage matches an image reference pinned by sha256 digest. A tag
// is not a version: it can move under a running deployment.
var digestPinnedImage = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)

// IsDigestPinnedImage reports whether image is pinned by a sha256 digest. The
// REST validator reuses it so the two surfaces cannot drift.
func IsDigestPinnedImage(image string) bool { return digestPinnedImage.MatchString(image) }

// ValidateRuntimeProfile checks a runtime contract before it is persisted.
//
// The endpoint is deliberately exclusive with hostingRef: a profile that both
// names an address and delegates deployment has two answers to "where is the
// runtime", and whichever one the reader picks is a coin flip.
func ValidateRuntimeProfile(spec *agent.RuntimeProfileSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	kind := spec.Kind
	if kind == "" {
		kind = agent.RuntimeKindA2A
	}
	switch kind {
	case agent.RuntimeKindInproc:
		if spec.Endpoint != "" || spec.ClientConfig != nil || spec.HostingRef != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("kind"), spec.Kind,
				"an inproc profile has no endpoint and no hosting: it runs in this process"))
		}
	case agent.RuntimeKindA2A:
		hasEndpoint := spec.Endpoint != "" || spec.ClientConfig != nil
		if !hasEndpoint && spec.HostingRef == nil {
			allErrs = append(allErrs, field.Required(fldPath.Child("endpoint"),
				"an a2a profile needs an endpoint, a clientConfig or a hostingRef"))
		}
		if hasEndpoint && spec.HostingRef != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("hostingRef"), spec.HostingRef.Name,
				"endpoint/clientConfig and hostingRef are mutually exclusive"))
		}
		if spec.Endpoint != "" {
			allErrs = append(allErrs, validateURL(spec.Endpoint, fldPath.Child("endpoint"))...)
		}
		if spec.ClientConfig != nil {
			allErrs = append(allErrs, validateClientConfig(spec.ClientConfig, fldPath.Child("clientConfig"))...)
		}
	default:
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("kind"), spec.Kind,
			[]string{agent.RuntimeKindInproc, agent.RuntimeKindA2A}))
	}

	if spec.HostingRef != nil {
		if spec.HostingRef.Name == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("hostingRef", "name"),
				"hostingRef must name a RuntimeHosting"))
		}
		if spec.HostingRef.Namespace != "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("hostingRef", "namespace"),
				spec.HostingRef.Namespace, "a hosting must be in the profile's namespace"))
		}
	}

	if spec.ModelAccess != "" {
		switch spec.ModelAccess {
		case agent.ModelAccessProxy, agent.ModelAccessCredential:
		default:
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("modelAccess"), spec.ModelAccess,
				[]string{agent.ModelAccessProxy, agent.ModelAccessCredential}))
		}
	}

	if spec.Auth != nil {
		allErrs = append(allErrs, validateSecretKeyRef(spec.Auth.BearerTokenRef,
			fldPath.Child("auth", "bearerTokenRef"))...)
		allErrs = append(allErrs, validateSecretKeyRef(spec.Auth.CABundleRef,
			fldPath.Child("auth", "caBundleRef"))...)
	}

	if spec.Limits.RunTimeout != nil && spec.Limits.RunTimeout.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("limits", "runTimeout"),
			spec.Limits.RunTimeout.Duration, "must be positive"))
	}
	if spec.Limits.MaxConcurrentRuns < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("limits", "maxConcurrentRuns"),
			spec.Limits.MaxConcurrentRuns, "must not be negative"))
	}
	if spec.Limits.MaxToolCalls < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("limits", "maxToolCalls"),
			spec.Limits.MaxToolCalls, "must not be negative"))
	}

	return allErrs
}

// ValidateRuntimeHosting checks a hosting pool before it is persisted.
func ValidateRuntimeHosting(spec *agent.RuntimeHostingSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.ProfileRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("profileRef", "name"),
			"a hosting must name the profile it serves"))
	}
	if spec.ProfileRef.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("profileRef", "namespace"),
			spec.ProfileRef.Namespace, "a hosting must serve a profile in its own namespace"))
	}

	switch spec.Mode {
	case agent.HostingModeDocker:
		if spec.Image == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("image"),
				"docker hosting needs a digest-pinned image"))
		} else if !digestPinnedImage.MatchString(spec.Image) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("image"), spec.Image,
				"must be pinned by @sha256:<64 hex characters>"))
		}
	case agent.HostingModeProcess:
		if len(spec.Command) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("command"),
				"process hosting needs a command"))
		}
	default:
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("mode"), spec.Mode,
			[]string{agent.HostingModeDocker, agent.HostingModeProcess}))
	}

	if spec.MaxInstances < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxInstances"), spec.MaxInstances,
			"must not be negative"))
	}
	if spec.IdleTTL != nil && spec.IdleTTL.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("idleTTL"), spec.IdleTTL.Duration,
			"must be positive"))
	}
	for i, env := range spec.Env {
		if env.Name == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("env").Index(i), "env var name must not be empty"))
			continue
		}
		if strings.HasPrefix(env.Name, "ONEXAI_RUNTIME_") {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("env").Index(i).Child("name"), env.Name,
				"ONEXAI_RUNTIME_* is reserved for the gateway"))
		}
	}

	return allErrs
}

func validateSecretKeyRef(ref *corev1.SecretKeySelector, fldPath *field.Path) field.ErrorList {
	if ref == nil {
		return nil
	}
	var allErrs field.ErrorList
	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "must name a Secret"))
	}
	if ref.Key == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("key"), "must name a key in the Secret"))
	}
	return allErrs
}
