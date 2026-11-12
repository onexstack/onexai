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

// Hosting modes. Deployment is an operational concern; it never changes how
// the gateway talks to a runtime.
const (
	// HostingModeDocker runs one container per runtime instance.
	HostingModeDocker = "docker"
	// HostingModeProcess runs a child process per runtime instance.
	HostingModeProcess = "process"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeHosting is a pool of runtime instances for one RuntimeProfile. Each
// instance serves one agent at a time; a session is pinned to the instance it
// started on so A2A context/task state is never lost.
type RuntimeHosting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec RuntimeHostingSpec `json:"spec,omitempty"`
	// +optional
	Status RuntimeHostingStatus `json:"status,omitempty"`
}

// RuntimeHostingSpec defines the desired state of RuntimeHosting.
type RuntimeHostingSpec struct {
	CommonSpec `json:",inline"`

	// ProfileRef names the RuntimeProfile this pool serves.
	ProfileRef Reference `json:"profileRef"`

	// Mode is docker or process.
	// +kubebuilder:validation:Enum=docker;process
	Mode string `json:"mode"`

	// Image is the digest-pinned runtime image.
	// +optional
	Image string `json:"image,omitempty"`

	// Command overrides the image entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Env is literal environment for the runtime process.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// MaxInstances bounds concurrent instances. Zero means the deployment
	// default.
	// +optional
	MaxInstances int32 `json:"maxInstances,omitempty"`

	// IdleTTL is how long an idle instance is kept warm. Nil uses the
	// deployment default.
	// +optional
	IdleTTL *metav1.Duration `json:"idleTTL,omitempty"`

	// Resources are the container resource requests/limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// RuntimeHostingStatus defines the observed state of RuntimeHosting.
type RuntimeHostingStatus struct {
	CommonStatus `json:",inline"`

	// Instances are the runtime instances this pool currently knows about.
	// +optional
	// +listType=map
	// +listMapKey=id
	Instances []RuntimeInstanceStatus `json:"instances,omitempty"`
}

// RuntimeInstanceStatus is one runtime instance in a hosting pool.
type RuntimeInstanceStatus struct {
	// ID is the instance identity (container/process ID).
	ID string `json:"id"`

	// Agent is the agent currently leased to the instance. Empty when idle.
	// +optional
	Agent string `json:"agent,omitempty"`

	// Endpoint is the A2A endpoint the gateway dials.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// State is starting, ready, draining, or stopped.
	// +optional
	State string `json:"state,omitempty"`

	// LeaseUntil is when the current agent lease expires.
	// +optional
	LeaseUntil *metav1.Time `json:"leaseUntil,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeHostingList is a list of RuntimeHosting objects.
type RuntimeHostingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RuntimeHosting `json:"items"`
}
