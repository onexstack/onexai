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
	"k8s.io/apimachinery/pkg/runtime"
)

// AgentKind says who owns an agent's behavior.
//
// native: the platform defines the prompt, model and capability set.
// external: the behavior is defined by a remote A2A AgentCard; the platform
// only discovers, authenticates and invokes it.
type AgentKind string

const (
	AgentKindNative   AgentKind = "native"
	AgentKindExternal AgentKind = "external"
)

// AgentExecutionMode selects the execution backend.
type AgentExecutionMode string

const (
	ExecutionModeInproc AgentExecutionMode = "inproc"
	ExecutionModeA2A    AgentExecutionMode = "a2a"
)

// A2ASource is how an external agent's card is obtained.
type A2ASource string

const (
	A2ASourceCardURL A2ASource = "card-url"
	A2ASourceJSON    A2ASource = "json"
	A2ASourceManual  A2ASource = "manual"
	A2ASourceRuntime A2ASource = "runtime"
)

// A2A protocol versions the platform understands.
const (
	A2AProtocolVersion03 = "0.3"
	A2AProtocolVersion10 = "1.0"
)

// Agent execution phases.
const (
	AgentPhasePending  = "Pending"
	AgentPhaseReady    = "Ready"
	AgentPhaseDegraded = "Degraded"
	AgentPhaseError    = "Error"
	AgentPhaseDisabled = "Disabled"
)

// AgentExecution selects how an agent runs.
type AgentExecution struct {
	// Mode is inproc or a2a. empty means inproc.
	// +optional
	// +kubebuilder:validation:Enum=inproc;a2a
	Mode AgentExecutionMode `json:"mode,omitempty"`

	// A2A configures a remote A2A execution. Required when Mode=a2a.
	// +optional
	A2A *A2AExecution `json:"a2a,omitempty"`
}

// A2AExecution is the outbound contract for an A2A endpoint.
type A2AExecution struct {
	// RuntimeRef names a RuntimeProfile(kind=a2a) in the agent's namespace.
	// +optional
	RuntimeRef *Reference `json:"runtimeRef,omitempty"`

	// Endpoint is the invocation base URL. Empty means the card's first
	// supported interface.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ClientConfig is the shared Kubernetes-shaped endpoint configuration. When
	// set it replaces Endpoint.
	// +optional
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`

	// Source is how the card is obtained. Only external agents set it.
	// +optional
	// +kubebuilder:validation:Enum=card-url;json;manual;runtime
	Source A2ASource `json:"source,omitempty"`

	// CardURL is the explicit AgentCard URL. Empty means discovery from the
	// origin root or the endpoint.
	// +optional
	CardURL string `json:"cardUrl,omitempty"`

	// Card is an imported or manually authored AgentCard.
	// +optional
	Card *runtime.RawExtension `json:"card,omitempty"`

	// ProtocolVersion pins the protocol (0.3 / 1.0). Empty means derive it
	// from the card.
	// +optional
	ProtocolVersion string `json:"protocolVersion,omitempty"`

	// Auth is the outbound credential.
	// +optional
	Auth *A2AAuth `json:"auth,omitempty"`

	// TimeoutSeconds bounds one invocation. Zero uses the deployment default.
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// MaxConcurrentInvocations bounds concurrent calls to this agent. Zero
	// uses the deployment default.
	// +optional
	MaxConcurrentInvocations int32 `json:"maxConcurrentInvocations,omitempty"`

	// MetadataJSON is extra metadata sent with every message. It is JSON so
	// it can carry arrays and objects, unlike a map[string]string.
	// +optional
	MetadataJSON string `json:"metadataJson,omitempty"`
}

// A2AAuth is the outbound credential for an A2A endpoint.
type A2AAuth struct {
	// Type is none, apiKey, bearer, headers, oauth2, runtime or aksk.
	// +optional
	// +kubebuilder:validation:Enum=none;apiKey;bearer;headers;oauth2;runtime;aksk
	Type string `json:"type,omitempty"`

	// APIKeyHeader is the header name for apiKey. Default X-API-KEY.
	// +optional
	APIKeyHeader string `json:"apiKeyHeader,omitempty"`

	// HeaderNames lists the custom header names. Their values live in the
	// credential Secret.
	// +optional
	HeaderNames []string `json:"headerNames,omitempty"`

	// TokenURL is the OAuth2 token endpoint.
	// +optional
	TokenURL string `json:"tokenUrl,omitempty"`

	// ClientID is the OAuth2 client id (not a secret).
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// Scopes are the OAuth2 scopes.
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// Audience is the OAuth2 audience.
	// +optional
	Audience string `json:"audience,omitempty"`

	// ClientAuth is basic or post for OAuth2 token requests.
	// +optional
	ClientAuth string `json:"clientAuth,omitempty"`

	// InsecureSkipVerify disables TLS verification. Local development only.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// SecretRef is server-owned and never accepted from clients.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// AgentContract is the common, externally visible contract.
type AgentContract struct {
	// AllowedSkills restricts which external card skills may be used. Empty
	// means every declared skill.
	// +optional
	AllowedSkills []string `json:"allowedSkills,omitempty"`

	// InputModes / OutputModes override the card defaults.
	// +optional
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// AgentSharing controls cross-namespace use of an external agent.
type AgentSharing struct {
	// AllowedNamespaces lists namespaces whose agents may reference this one.
	// Empty means any namespace. Its own namespace is always allowed.
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
}

// A2ASkill is one skill declared by an AgentCard.
type A2ASkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// A2ACapabilities is the card's optional capability declaration.
type A2ACapabilities struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	ExtendedAgentCard      bool `json:"extendedAgentCard,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

// A2AExtension is one protocol extension declared by the card.
type A2AExtension struct {
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// A2ACardSnapshot is one entry in the card version history.
type A2ACardSnapshot struct {
	Version        string       `json:"version,omitempty"`
	Digest         string       `json:"digest,omitempty"`
	FetchedAt      *metav1.Time `json:"fetchedAt,omitempty"`
	SkillCount     int32        `json:"skillCount,omitempty"`
	InterfaceCount int32        `json:"interfaceCount,omitempty"`
}

// AgentA2AStatus is the observed A2A contract of an agent.
type AgentA2AStatus struct {
	// Phase is Pending, Ready, Degraded, Error or Disabled.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ProtocolVersion is the normalized protocol version (0.3 / 1.0).
	// +optional
	ProtocolVersion string `json:"protocolVersion,omitempty"`

	// Transport is JSONRPC or HTTP+JSON.
	// +optional
	Transport string `json:"transport,omitempty"`

	// ResolvedEndpoint is the endpoint the gateway dials.
	// +optional
	ResolvedEndpoint string `json:"resolvedEndpoint,omitempty"`

	// CardVersion / CardDigest identify the last good card.
	// +optional
	CardVersion string `json:"cardVersion,omitempty"`
	// +optional
	CardDigest string `json:"cardDigest,omitempty"`
	// +optional
	CardFetchedAt *metav1.Time `json:"cardFetchedAt,omitempty"`

	// LastSyncError explains a failed refresh. The last good card is kept.
	// +optional
	LastSyncError string `json:"lastSyncError,omitempty"`

	// AuthReady reports that the credential Secret has every required key.
	// +optional
	AuthReady bool `json:"authReady,omitempty"`

	// Provider is the card's provider name.
	// +optional
	Provider string `json:"provider,omitempty"`

	// Capabilities, skills and modes are normalized from the card.
	// +optional
	Capabilities A2ACapabilities `json:"capabilities,omitempty"`
	// +optional
	InputModes []string `json:"inputModes,omitempty"`
	// +optional
	OutputModes []string `json:"outputModes,omitempty"`
	// +optional
	Skills []A2ASkill `json:"skills,omitempty"`
	// +optional
	SecuritySchemes *runtime.RawExtension `json:"securitySchemes,omitempty"`
	// +optional
	Extensions []A2AExtension `json:"extensions,omitempty"`

	// Card is the last good raw AgentCard. Cards over 256KiB are not written;
	// the detail page fetches them live.
	// +optional
	Card *runtime.RawExtension `json:"card,omitempty"`

	// CardHistory keeps the last 10 summaries.
	// +optional
	CardHistory []A2ACardSnapshot `json:"cardHistory,omitempty"`

	// ObservedRefreshToken is the last refresh annotation acted on.
	// +optional
	ObservedRefreshToken string `json:"observedRefreshToken,omitempty"`
}
