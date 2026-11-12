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

// Hosting modes. Deployment is an operational concern; it never changes how
// the gateway talks to a runtime.
const (
	// HostingModeDocker runs one container per runtime instance.
	HostingModeDocker = "docker"
	// HostingModeProcess runs a child process per runtime instance.
	HostingModeProcess = "process"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeHosting is the internal (hub) schema for the runtime hostings API. It
// is a pool: at most MaxInstances runtime instances, each serving one agent at
// a time.
type RuntimeHosting struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   RuntimeHostingSpec
	Status RuntimeHostingStatus
}

// RuntimeHostingSpec defines the desired state of RuntimeHosting.
type RuntimeHostingSpec struct {
	CommonSpec

	// ProfileRef names the RuntimeProfile this pool serves.
	ProfileRef Reference
	// Mode is docker or process.
	Mode string
	// Image is the digest-pinned runtime image.
	Image string
	// Command overrides the image entrypoint.
	Command []string
	// Env is literal environment for the runtime process.
	Env []corev1.EnvVar
	// MaxInstances bounds concurrent instances. Zero means the deployment
	// default.
	MaxInstances int32
	// IdleTTL is how long an idle instance is kept warm. Nil uses the
	// deployment default.
	IdleTTL *metav1.Duration
	// Resources are the container resource requests/limits.
	Resources corev1.ResourceRequirements
}

// RuntimeHostingStatus defines the observed state of RuntimeHosting.
type RuntimeHostingStatus struct {
	CommonStatus

	// Instances are the runtime instances this pool currently knows about.
	Instances []RuntimeInstanceStatus
}

// RuntimeInstanceStatus is one runtime instance in a hosting pool.
type RuntimeInstanceStatus struct {
	// ID is the instance identity (container/process ID).
	ID string
	// Agent is the agent currently leased to the instance. Empty when idle.
	Agent string
	// Endpoint is the A2A endpoint the gateway dials.
	Endpoint string
	// State is starting, ready, draining, or stopped.
	State string
	// LeaseUntil is when the current agent lease expires.
	LeaseUntil *metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeHostingList is a list of RuntimeHosting objects.
type RuntimeHostingList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []RuntimeHosting
}
