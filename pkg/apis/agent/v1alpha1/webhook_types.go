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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Webhook is an HTTP hook called around an agent run. Agents name hooks rather
// than carrying their configuration, so moving an endpoint is one edit and no
// credential is ever stored on an Agent.
type Webhook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec WebhookSpec `json:"spec,omitempty"`
	// +optional
	Status WebhookStatus `json:"status,omitempty"`
}

// WebhookSpec defines the desired state of Webhook.
type WebhookSpec struct {
	CommonSpec `json:",inline"`

	// Type selects what this Webhook is used for: run, authentication or
	// authorization. Empty means run, which keeps objects written before the
	// field existed working unchanged.
	//
	// A run hook is called around an Agent run (onStart/onEnd). An
	// authentication hook receives a Kubernetes TokenReview and answers "who is
	// this token". An authorization hook receives a SubjectAccessReview and
	// answers "may this subject use this Agent". Agents name the two latter
	// kinds in authnWebhooks/authzWebhooks; when they do, the platform's own
	// authentication/authorization is not consulted for that Agent.
	// +optional
	Type string `json:"type,omitempty"`

	// Events are the run-lifecycle moments this hook fires on: onStart, onEnd.
	// They are required for a run hook and must be empty for the authentication
	// and authorization types, which are called per request rather than per run.
	// +optional
	Events []string `json:"events,omitempty"`

	// Audiences are sent in TokenReview.spec.audiences for an authentication
	// hook. Empty sends none, leaving the audience decision to the receiver.
	// +optional
	Audiences []string `json:"audiences,omitempty"`

	// Endpoint is the URL the runtime posts to.
	//
	// Deprecated: use ClientConfig. It is kept so objects written before
	// ClientConfig existed keep working; a write that supplies ClientConfig
	// leaves this empty.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ClientConfig addresses the endpoint with the Kubernetes
	// admissionregistration.k8s.io/v1 WebhookClientConfig shape: a URL or an
	// in-cluster Service, an optional CA bundle, and the platform's timeout /
	// headers / credential extensions.
	//
	// Exactly one of ClientConfig and Endpoint must be set. ClientConfig wins
	// when both are present.
	// +optional
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`

	// SecretRef names the Secret whose value is the bearer token sent with the
	// call. It is a reference rather than a value because this object is shown in
	// every console page and dump; a credential beside a URL is a credential
	// everywhere. Empty means the endpoint is called unauthenticated, which is a
	// real configuration on a trusted network.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// TimeoutSeconds bounds one call. Zero means the deployment default.
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// FailureMode decides what an unreachable endpoint means: "ignore" lets the
	// run through, "fail" refuses it. Empty means ignore.
	// +optional
	FailureMode string `json:"failureMode,omitempty"`

	// Headers are sent with every delivery, for an endpoint that needs more than
	// the shared token — a tenant label, a routing key. They cannot override the
	// delivery headers the contract defines.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// Template, when set, is rendered and sent as the request body instead of the
	// default one, so an endpoint can name the fields it wants in its own
	// vocabulary.
	// +optional
	Template string `json:"template,omitempty"`
}

// WebhookStatus defines the observed state of Webhook.
type WebhookStatus struct {
	CommonStatus `json:",inline"`

	// ConsecutiveFailures is what the runtime observed. The runtime disables the
	// hook locally after too many failures; publishing the count here is what
	// turns "the hook stopped firing" into something an operator can see.
	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`
}

// Webhook condition types beyond the shared ones.
const (
	// ConditionEndpointHealthy reports that the endpoint answered the last probe.
	ConditionEndpointHealthy = "EndpointHealthy"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WebhookList is a list of Webhook objects.
type WebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Webhook `json:"items"`
}
