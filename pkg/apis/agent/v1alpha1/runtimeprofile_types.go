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

// Runtime kind and model access values. They are part of the wire contract: a
// stored profile that carries an unknown value is rejected rather than mapped
// to a default, because silently running an agent on a different runtime is
// worse than refusing to run it.
const (
	// RuntimeKindInproc is the built-in eino runtime running in this process.
	RuntimeKindInproc = "inproc"
	// RuntimeKindA2A is any runtime speaking A2A over HTTP.
	RuntimeKindA2A = "a2a"

	// ModelAccessProxy sends model traffic through the gateway's model data
	// plane. The runtime never sees a provider key.
	ModelAccessProxy = "proxy"
	// ModelAccessCredential issues a short-lived, model/run-scoped credential
	// to the runtime. It is the exception for runtimes that cannot point at a
	// custom endpoint.
	ModelAccessCredential = "credential"

	// HistoryModeGateway means the gateway owns conversation history.
	HistoryModeGateway = "gateway"
	// HistoryModeRuntime means the runtime owns conversation history and the
	// gateway only stores an audit copy.
	HistoryModeRuntime = "runtime"

	// ResumeModeCheckpoint means a paused run resumes from a gateway
	// checkpoint.
	ResumeModeCheckpoint = "checkpoint"
	// ResumeModeTask means a paused run resumes through the A2A task.
	ResumeModeTask = "task"
)

// Condition types reported by RuntimeProfile and RuntimeHosting in addition to
// the shared ones.
const (
	// ConditionCompatible reports that the runtime's Agent Card satisfies the
	// features the profile/agents require.
	ConditionCompatible = "Compatible"
	// ConditionHosted reports that a hosting controller has at least one ready
	// instance.
	ConditionHosted = "Hosted"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeProfile is the contract a runtime offers: how to call it, what it can
// do, and the limits the gateway enforces. It is deliberately separate from
// RuntimeHosting, which describes how the runtime is deployed.
type RuntimeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec RuntimeProfileSpec `json:"spec,omitempty"`
	// +optional
	Status RuntimeProfileStatus `json:"status,omitempty"`
}

// RuntimeProfileSpec defines the desired state of RuntimeProfile.
type RuntimeProfileSpec struct {
	CommonSpec `json:",inline"`

	// Kind selects the adapter: inproc (built-in eino) or a2a.
	// +optional
	// +kubebuilder:validation:Enum=inproc;a2a
	Kind string `json:"kind,omitempty"`

	// Endpoint is a static A2A endpoint. Mutually exclusive with HostingRef.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ClientConfig is the shared Kubernetes-shaped endpoint configuration. When
	// set it replaces Endpoint.
	// +optional
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`

	// HostingRef delegates deployment to a RuntimeHosting in the same
	// namespace. The hosting controller writes the resolved endpoint to status.
	// +optional
	HostingRef *Reference `json:"hostingRef,omitempty"`

	// Auth is how the gateway authenticates to the runtime.
	// +optional
	Auth *RuntimeAuth `json:"auth,omitempty"`

	// ModelAccess is proxy or credential.
	// +optional
	// +kubebuilder:validation:Enum=proxy;credential
	ModelAccess string `json:"modelAccess,omitempty"`

	// Limits are the gateway-side hard bounds.
	// +optional
	Limits RuntimeLimits `json:"limits,omitempty"`
}

// RuntimeAuth is the outbound credential for an A2A endpoint.
type RuntimeAuth struct {
	// BearerTokenRef references a Secret key holding a bearer token.
	// +optional
	BearerTokenRef *corev1.SecretKeySelector `json:"bearerTokenRef,omitempty"`

	// CABundleRef references a Secret key holding a PEM CA bundle.
	// +optional
	CABundleRef *corev1.SecretKeySelector `json:"caBundleRef,omitempty"`

	// InsecureSkipVerify disables TLS verification. It is only for local
	// development and must not be used in production.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// RuntimeLimits are the gateway-enforced bounds for one profile.
type RuntimeLimits struct {
	// RunTimeout bounds one run. Nil means the deployment default.
	// +optional
	RunTimeout *metav1.Duration `json:"runTimeout,omitempty"`

	// MaxConcurrentRuns bounds concurrent runs across the profile. Zero means
	// unlimited.
	// +optional
	MaxConcurrentRuns int32 `json:"maxConcurrentRuns,omitempty"`

	// MaxToolCalls bounds capability tool calls per run. Zero means unlimited.
	// +optional
	MaxToolCalls int32 `json:"maxToolCalls,omitempty"`
}

// RuntimeProfileStatus defines the observed state of RuntimeProfile.
type RuntimeProfileStatus struct {
	CommonStatus `json:",inline"`

	// ResolvedEndpoint is the endpoint the gateway actually dials. It is set by
	// a hosting controller or copied from spec.endpoint, and it is the only
	// endpoint readers should use.
	// +optional
	ResolvedEndpoint string `json:"resolvedEndpoint,omitempty"`

	// Capabilities is the negotiated Agent Card capability set.
	// +optional
	Capabilities RuntimeCapabilities `json:"capabilities,omitempty"`
}

// RuntimeCapabilities is the negotiated feature set of a runtime.
type RuntimeCapabilities struct {
	// +optional
	Streaming bool `json:"streaming,omitempty"`
	// +optional
	HITL bool `json:"hitl,omitempty"`
	// +optional
	Usage bool `json:"usage,omitempty"`
	// +optional
	Images bool `json:"images,omitempty"`
	// +optional
	StructuredOutput bool `json:"structuredOutput,omitempty"`
	// +optional
	HistoryMode string `json:"historyMode,omitempty"`
	// +optional
	ResumeMode string `json:"resumeMode,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeProfileList is a list of RuntimeProfile objects.
type RuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RuntimeProfile `json:"items"`
}
