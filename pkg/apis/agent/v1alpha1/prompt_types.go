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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Prompt is a reusable system prompt. It exists so several agents can share one
// text without copying it, and so a prompt edit is one object to review.
type Prompt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec PromptSpec `json:"spec,omitempty"`
	// +optional
	Status PromptStatus `json:"status,omitempty"`
}

// PromptSpec defines the desired state of Prompt.
type PromptSpec struct {
	CommonSpec `json:",inline"`

	// Template is the prompt text. It is capped at 256KiB: a prompt large enough
	// to approach the etcd object limit belongs in a ConfigMap, not here.
	Template string `json:"template"`

	// Variables documents the placeholders the template expects.
	// +optional
	Variables []string `json:"variables,omitempty"`

	// DataSources makes the template render. When non-empty, the template is a
	// Go text/template; `include "key"` inserts the named source verbatim.
	// Empty keeps the template literal, so an existing prompt containing "{{"
	// is not reinterpreted.
	// +optional
	DataSources map[string]string `json:"dataSources,omitempty"`

}

// PromptStatus defines the observed state of Prompt.
type PromptStatus struct {
	CommonStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromptList is a list of Prompt objects.
type PromptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Prompt `json:"items"`
}
