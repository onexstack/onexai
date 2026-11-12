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

// Model is the internal (hub) schema for the models API.
type Model struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   ModelSpec
	Status ModelStatus
}

// ModelSpec defines the desired state of Model.
type ModelSpec struct {
	CommonSpec

	Provider      string
	Endpoint      string
	Model         string
	CredentialRef *corev1.SecretKeySelector
	Capabilities  []string
	Params        ModelParams
}

// ModelParams are the sampling defaults for an endpoint.
type ModelParams struct {
	Temperature *float64
	TopP        *float64
	MaxTokens   *int32
}

// ModelStatus defines the observed state of Model.
type ModelStatus struct {
	CommonStatus

	LastProbeTime *metav1.Time
	LatencyMillis int32
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelList is a list of Model objects.
type ModelList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Model
}
