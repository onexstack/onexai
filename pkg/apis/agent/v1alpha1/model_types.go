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

// Model describes one upstream endpoint: where it is, which model to ask for,
// and how to authenticate. The credential lives in a Secret and is referenced,
// never stored here.
type Model struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ModelSpec `json:"spec,omitempty"`
	// +optional
	Status ModelStatus `json:"status,omitempty"`
}

// ModelSpec defines the desired state of Model.
type ModelSpec struct {
	CommonSpec `json:",inline"`

	// Provider names the protocol family: openai, deepseek, anthropic, ollama…
	// Empty lets the endpoint and model name be inferred.
	// +optional
	Provider string `json:"provider,omitempty"`

	// Endpoint is the base URL of the OpenAI-compatible API.
	Endpoint string `json:"endpoint"`

	// Model is the upstream model name, e.g. "deepseek-chat".
	Model string `json:"model"`

	// CredentialRef names the Secret key holding the API key.
	// +optional
	CredentialRef *corev1.SecretKeySelector `json:"credentialRef,omitempty"`

	// Capabilities declares what the endpoint can do beyond text: vision,
	// tool_call, … Empty means "not declared", and readers fall back to
	// inference from the provider and model name.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// Params are sampling defaults for this endpoint.
	// +optional
	Params ModelParams `json:"params,omitempty"`
}

// ModelParams are the sampling defaults applied to a run unless the request
// overrides them.
type ModelParams struct {
	// +optional
	Temperature *float64 `json:"temperature,omitempty"`
	// +optional
	TopP *float64 `json:"topP,omitempty"`
	// +optional
	MaxTokens *int32 `json:"maxTokens,omitempty"`
}

// ModelStatus defines the observed state of Model.
type ModelStatus struct {
	CommonStatus `json:",inline"`

	// LastProbeTime is when the endpoint was last reachable.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// LatencyMillis is the round-trip of the last successful probe.
	// +optional
	LatencyMillis int32 `json:"latencyMillis,omitempty"`
}

// Model condition types beyond the shared ones.
const (
	// ConditionReachable reports that the endpoint answered a minimal request.
	ConditionReachable = "Reachable"
	// ConditionAuthOK reports that the credential was accepted.
	ConditionAuthOK = "AuthOK"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelList is a list of Model objects.
type ModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Model `json:"items"`
}
