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

package agent

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SandboxTemplate declares what a sandbox container is made of.
type SandboxTemplate struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	// +optional
	Spec SandboxTemplateSpec
	// +optional
	Status SandboxTemplateStatus
}

// SandboxTemplateSpec defines one template.
type SandboxTemplateSpec struct {
	CommonSpec

	// Image is the container image, pinned by digest.
	Image string
	// Command overrides the image entrypoint.
	Command []string
	// Env is literal environment for the container.
	Env []corev1.EnvVar
	// TTL is how long a sandbox from this template may live.
	TTL *metav1.Duration
}

// SandboxTemplateStatus is the observed state of the template.
type SandboxTemplateStatus struct {
	CommonStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SandboxTemplateList is a list of SandboxTemplate objects.
type SandboxTemplateList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []SandboxTemplate
}
