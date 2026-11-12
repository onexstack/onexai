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

package model

import "encoding/json"

// The types below are the canonical JSON shape of an agent's A2A execution and
// status. Both the proto conversion and the control-plane projection marshal
// into them, so the two directions cannot disagree about how a card, a time or
// a credential is represented.

type AgentExecutionM struct {
	Mode string         `json:"mode,omitempty"`
	A2A  *A2AExecutionM `json:"a2a,omitempty"`
}

type A2AExecutionM struct {
	RuntimeRef               string          `json:"runtimeRef,omitempty"`
	Endpoint                 string          `json:"endpoint,omitempty"`
	ClientConfig             json.RawMessage `json:"clientConfig,omitempty"`
	Source                   string          `json:"source,omitempty"`
	CardURL                  string          `json:"cardUrl,omitempty"`
	Card                     string          `json:"card,omitempty"`
	ProtocolVersion          string          `json:"protocolVersion,omitempty"`
	Auth                     *A2AAuthM       `json:"auth,omitempty"`
	TimeoutSeconds           int32           `json:"timeoutSeconds,omitempty"`
	MaxConcurrentInvocations int32           `json:"maxConcurrentInvocations,omitempty"`
	MetadataJSON             string          `json:"metadataJson,omitempty"`
}

type A2AAuthM struct {
	Type               string   `json:"type,omitempty"`
	APIKeyHeader       string   `json:"apiKeyHeader,omitempty"`
	HeaderNames        []string `json:"headerNames,omitempty"`
	TokenURL           string   `json:"tokenUrl,omitempty"`
	ClientID           string   `json:"clientId,omitempty"`
	Scopes             []string `json:"scopes,omitempty"`
	Audience           string   `json:"audience,omitempty"`
	ClientAuth         string   `json:"clientAuth,omitempty"`
	InsecureSkipVerify bool     `json:"insecureSkipVerify,omitempty"`
	HasCredentials     bool     `json:"hasCredentials,omitempty"`

	// Write-only credential material. The store consumes it when writing the
	// Secret and never projects it back from the control plane, so a read row
	// cannot leak it.
	Credential       string `json:"credential,omitempty"`
	ClientSecret     string `json:"clientSecret,omitempty"`
	ClearCredentials bool   `json:"clearCredentials,omitempty"`
}

type AgentContractM struct {
	AllowedSkills []string `json:"allowedSkills,omitempty"`
	InputModes    []string `json:"inputModes,omitempty"`
	OutputModes   []string `json:"outputModes,omitempty"`
}

type AgentSharingM struct {
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
}

type A2ASkillM struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

type A2ACapabilitiesM struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	ExtendedAgentCard      bool `json:"extendedAgentCard,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

type A2AExtensionM struct {
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type A2ACardSnapshotM struct {
	Version        string `json:"version,omitempty"`
	Digest         string `json:"digest,omitempty"`
	FetchedAt      int64  `json:"fetchedAt,omitempty"`
	SkillCount     int32  `json:"skillCount,omitempty"`
	InterfaceCount int32  `json:"interfaceCount,omitempty"`
}

type AgentA2AStatusM struct {
	Phase                string             `json:"phase,omitempty"`
	ProtocolVersion      string             `json:"protocolVersion,omitempty"`
	Transport            string             `json:"transport,omitempty"`
	ResolvedEndpoint     string             `json:"resolvedEndpoint,omitempty"`
	CardVersion          string             `json:"cardVersion,omitempty"`
	CardDigest           string             `json:"cardDigest,omitempty"`
	CardFetchedAt        int64              `json:"cardFetchedAt,omitempty"`
	LastSyncError        string             `json:"lastSyncError,omitempty"`
	AuthReady            bool               `json:"authReady,omitempty"`
	Provider             string             `json:"provider,omitempty"`
	Capabilities         A2ACapabilitiesM   `json:"capabilities,omitempty"`
	InputModes           []string           `json:"inputModes,omitempty"`
	OutputModes          []string           `json:"outputModes,omitempty"`
	Skills               []A2ASkillM        `json:"skills,omitempty"`
	SecuritySchemes      string             `json:"securitySchemes,omitempty"`
	Extensions           []A2AExtensionM    `json:"extensions,omitempty"`
	Card                 string             `json:"card,omitempty"`
	CardHistory          []A2ACardSnapshotM `json:"cardHistory,omitempty"`
	ObservedRefreshToken string             `json:"observedRefreshToken,omitempty"`
}
