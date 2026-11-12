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
	"k8s.io/apimachinery/pkg/runtime"
)

type AgentKind string

const (
	AgentKindNative   AgentKind = "native"
	AgentKindExternal AgentKind = "external"
)

type AgentExecutionMode string

const (
	ExecutionModeInproc AgentExecutionMode = "inproc"
	ExecutionModeA2A    AgentExecutionMode = "a2a"
)

type A2ASource string

const (
	A2ASourceCardURL A2ASource = "card-url"
	A2ASourceJSON    A2ASource = "json"
	A2ASourceManual  A2ASource = "manual"
	A2ASourceRuntime A2ASource = "runtime"
)

const (
	A2AProtocolVersion03 = "0.3"
	A2AProtocolVersion10 = "1.0"
)

const (
	AgentPhasePending  = "Pending"
	AgentPhaseReady    = "Ready"
	AgentPhaseDegraded = "Degraded"
	AgentPhaseError    = "Error"
	AgentPhaseDisabled = "Disabled"
)

type AgentExecution struct {
	Mode AgentExecutionMode
	A2A  *A2AExecution
}

type A2AExecution struct {
	RuntimeRef               *Reference
	Endpoint                 string
	ClientConfig             *ClientConfig
	Source                   A2ASource
	CardURL                  string
	Card                     *runtime.RawExtension
	ProtocolVersion          string
	Auth                     *A2AAuth
	TimeoutSeconds           int32
	MaxConcurrentInvocations int32
	MetadataJSON             string
}

type A2AAuth struct {
	Type               string
	APIKeyHeader       string
	HeaderNames        []string
	TokenURL           string
	ClientID           string
	Scopes             []string
	Audience           string
	ClientAuth         string
	InsecureSkipVerify bool
	SecretRef          *corev1.LocalObjectReference
}

type AgentContract struct {
	AllowedSkills []string
	InputModes    []string
	OutputModes   []string
}

type AgentSharing struct {
	AllowedNamespaces []string
}

type A2ASkill struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
	InputModes  []string
	OutputModes []string
}

type A2ACapabilities struct {
	Streaming              bool
	PushNotifications      bool
	ExtendedAgentCard      bool
	StateTransitionHistory bool
}

type A2AExtension struct {
	URI         string
	Description string
	Required    bool
}

type A2ACardSnapshot struct {
	Version        string
	Digest         string
	FetchedAt      *metav1.Time
	SkillCount     int32
	InterfaceCount int32
}

type AgentA2AStatus struct {
	Phase                string
	ProtocolVersion      string
	Transport            string
	ResolvedEndpoint     string
	CardVersion          string
	CardDigest           string
	CardFetchedAt        *metav1.Time
	LastSyncError        string
	AuthReady            bool
	Provider             string
	Capabilities         A2ACapabilities
	InputModes           []string
	OutputModes          []string
	Skills               []A2ASkill
	SecuritySchemes      *runtime.RawExtension
	Extensions           []A2AExtension
	Card                 *runtime.RawExtension
	CardHistory          []A2ACardSnapshot
	ObservedRefreshToken string
}
