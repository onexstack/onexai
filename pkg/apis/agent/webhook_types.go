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

package agent

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Webhook is the internal (hub) schema for the webhooks API.
type Webhook struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   WebhookSpec
	Status WebhookStatus
}

// WebhookSpec defines the desired state of Webhook.
type WebhookSpec struct {
	CommonSpec

	// Type is run, authentication or authorization. Empty means run.
	Type string
	// Events is required for run hooks; auth hooks leave it empty.
	Events []string
	// Audiences is sent in TokenReview.spec.audiences (authentication only).
	Audiences []string
	// Endpoint is the legacy URL. ClientConfig replaces it.
	Endpoint string
	// ClientConfig is the Kubernetes-shaped endpoint configuration.
	ClientConfig   *ClientConfig
	SecretRef      *corev1.LocalObjectReference
	TimeoutSeconds int32
	FailureMode    string
	Headers        map[string]string
	Template       string
}

// WebhookStatus defines the observed state of Webhook.
type WebhookStatus struct {
	CommonStatus

	ConsecutiveFailures int32
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WebhookList is a list of Webhook objects.
type WebhookList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Webhook
}
