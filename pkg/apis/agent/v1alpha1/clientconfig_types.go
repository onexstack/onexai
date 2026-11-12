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

package v1alpha1

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
)

// ClientConfig is the platform's one way to address an external endpoint.
//
// It embeds Kubernetes admissionregistration.k8s.io/v1 WebhookClientConfig
// verbatim. URL, Service and CABundle therefore keep their exact JSON shape and
// semantics: a value can be copied between a platform resource and a
// ValidatingWebhookConfiguration without translation, and a Kubernetes-native
// component already knows how to read it.
//
// The remaining fields are platform extensions. They describe concerns a
// Kubernetes webhook configuration leaves to its caller (a timeout, a TLS
// opt-out, static headers and a credential) and never change the meaning of the
// embedded fields.
//
// URL and Service are mutually exclusive. Service references resolve through
// the control plane's core/v1 Services; a deployment without that data falls
// back to the in-cluster DNS name (name.namespace.svc).
type ClientConfig struct {
	admissionregistrationv1.WebhookClientConfig `json:",inline"`

	// TimeoutSeconds bounds one request. Zero means the caller's default.
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// InsecureSkipTLSVerify disables certificate verification. It is strongly
	// discouraged and exists for local development only.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// Headers are sent with every request. A header value may be overridden by
	// the credential resolved from SecretRef/HeadersRef.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// SecretRef resolves one credential key from a Secret. The value is sent as
	// a bearer token. It is mutually exclusive with HeadersRef.
	// +optional
	SecretRef *corev1.SecretKeySelector `json:"secretRef,omitempty"`

	// HeadersRef imports every key of a Secret as a request header. It is
	// mutually exclusive with SecretRef.
	// +optional
	HeadersRef *corev1.LocalObjectReference `json:"headersRef,omitempty"`
}
