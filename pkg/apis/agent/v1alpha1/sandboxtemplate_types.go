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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SandboxTemplate declares what a sandbox container is made of.
//
// It exists so an operator can add or move an image without a redeploy: before
// it, every image was a key in the deployment's config map, which meant a new
// language runtime needed a release. The template names an image by digest, so
// "which image ran" is answerable after the tag has moved.
//
// It is a declaration, not a deployment: the container is still created by the
// agent service on the machine it runs on, and the isolation policy (network,
// limits, non-root, read-only root) stays a deployment decision.
type SandboxTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SandboxTemplateSpec `json:"spec,omitempty"`
	// +optional
	Status SandboxTemplateStatus `json:"status,omitempty"`
}

// SandboxTemplateSpec defines one template.
type SandboxTemplateSpec struct {
	CommonSpec `json:",inline"`

	// Image is the container image, pinned by digest (name@sha256:...).
	Image string `json:"image"`

	// Command overrides the image's entrypoint. Empty runs the deployment's
	// default command, which keeps the container alive for exec.
	// +optional
	Command []string `json:"command,omitempty"`

	// Env is literal environment for the container. Secret-backed values are
	// deliberately not supported: the sandbox is given no credential.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// TTL is how long a sandbox created from this template may live. Nil uses
	// the deployment default.
	// +optional
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

// SandboxTemplateStatus is the observed state of the template.
type SandboxTemplateStatus struct {
	CommonStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SandboxTemplateList is a list of SandboxTemplate objects.
type SandboxTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SandboxTemplate `json:"items"`
}
