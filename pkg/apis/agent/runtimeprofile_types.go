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

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeProfile is the internal (hub) schema for the runtime profiles API. It
// is the contract a runtime offers: how to call it, what it can do, and the
// limits the gateway enforces.
type RuntimeProfile struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   RuntimeProfileSpec
	Status RuntimeProfileStatus
}

// RuntimeProfileSpec defines the desired state of RuntimeProfile.
type RuntimeProfileSpec struct {
	CommonSpec

	// Kind selects the adapter: inproc (built-in eino) or a2a.
	Kind string
	// Endpoint is a static A2A endpoint. Mutually exclusive with HostingRef.
	Endpoint string
	// ClientConfig is the shared Kubernetes-shaped endpoint configuration.
	ClientConfig *ClientConfig
	// HostingRef delegates deployment to a RuntimeHosting in the same
	// namespace. The hosting controller writes the resolved endpoint to status.
	HostingRef *Reference
	// Auth is how the gateway authenticates to the runtime.
	Auth *RuntimeAuth
	// ModelAccess is proxy or credential.
	ModelAccess string
	// Limits are the gateway-side hard bounds.
	Limits RuntimeLimits
}

// RuntimeAuth is the outbound credential for an A2A endpoint.
type RuntimeAuth struct {
	// BearerTokenRef references a Secret key holding a bearer token.
	BearerTokenRef *corev1.SecretKeySelector
	// CABundleRef references a Secret key holding a PEM CA bundle.
	CABundleRef *corev1.SecretKeySelector
	// InsecureSkipVerify disables TLS verification. It is only for local
	// development and is rejected in production configuration.
	InsecureSkipVerify bool
}

// RuntimeLimits are the gateway-enforced bounds for one profile.
type RuntimeLimits struct {
	// RunTimeout bounds one run. Nil means the deployment default.
	RunTimeout *metav1.Duration
	// MaxConcurrentRuns bounds concurrent runs across the profile. Zero means
	// unlimited.
	MaxConcurrentRuns int32
	// MaxToolCalls bounds capability tool calls per run. Zero means unlimited.
	MaxToolCalls int32
}

// RuntimeProfileStatus defines the observed state of RuntimeProfile.
type RuntimeProfileStatus struct {
	CommonStatus

	// ResolvedEndpoint is the endpoint the gateway actually dials. It is set by
	// a hosting controller or copied from spec.endpoint, and it is the only
	// endpoint readers should use.
	ResolvedEndpoint string
	// Capabilities is the negotiated Agent Card capability set.
	Capabilities RuntimeCapabilities
}

// RuntimeCapabilities is the negotiated feature set of a runtime.
type RuntimeCapabilities struct {
	Streaming        bool
	HITL             bool
	Usage            bool
	Images           bool
	StructuredOutput bool
	HistoryMode      string
	ResumeMode       string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RuntimeProfileList is a list of RuntimeProfile objects.
type RuntimeProfileList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []RuntimeProfile
}
