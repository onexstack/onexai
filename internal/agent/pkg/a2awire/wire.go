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

// Package a2awire holds the metadata keys and extension URIs this service and
// its runtimes agree on.
//
// It is a leaf package on purpose: a runtime binary (the CLI harness, for one)
// has to speak the same contract, and importing the gateway to learn a string
// would pull the whole agent edge into that image. Nothing here may depend on
// anything but the protocol types.
package a2awire

// Metadata keys. They are namespaced so they cannot collide with a
// third-party extension.
const (
	MetadataPrefix             = "onexai.dev/a2a/"
	PartTypeMetadataKey        = MetadataPrefix + "part-type"
	ThoughtMetadataKey         = MetadataPrefix + "thought"
	UsageMetadataKey           = MetadataPrefix + "usage"
	OutputSchemaSHA256Metadata = MetadataPrefix + "output-schema-sha256"
)

// Versioned A2A extension URIs owned by onexai. A URI is a contract: bump the
// version when the payload changes, never reuse one for a different shape.
const (
	// HITLExtensionURI advertises human approval and user-input pauses.
	HITLExtensionURI = "https://onexai.onexstack.io/a2a/extensions/hitl/v1"
	// UsageExtensionURI advertises task-lifetime token usage.
	UsageExtensionURI = "https://onexai.onexstack.io/a2a/extensions/usage/v1"
	// StructuredOutputExtensionURI advertises a schema-validated final answer.
	StructuredOutputExtensionURI = "https://onexai.onexstack.io/a2a/extensions/structured-output/v1"
)

// Usage is the payload carried under UsageExtensionURI on a terminal status.
//
// It is a struct rather than a map so the producer and the consumer cannot
// disagree about a key: the gateway reads this shape off a remote runtime's
// status, and a renamed field would otherwise read as zero tokens.
type Usage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	ReasoningTokens   int `json:"reasoningTokens,omitempty"`
	CachedInputTokens int `json:"cachedInputTokens,omitempty"`
	TotalTokens       int `json:"totalTokens"`
}

// Metadata nests the usage payload the way a terminal status carries it.
//
// The payload is a map rather than the struct because the protocol only admits
// nil, bool, number, string, []any and map[string]any in metadata: a typed value
// is rejected when the task is saved, which turns a finished run into a failed
// one.
func (u Usage) Metadata() map[string]any {
	return map[string]any{UsageExtensionURI: u.Map()}
}

// Map is the payload itself.
func (u Usage) Map() map[string]any {
	fields := map[string]any{
		"inputTokens":  u.InputTokens,
		"outputTokens": u.OutputTokens,
		"totalTokens":  u.TotalTokens,
	}
	if u.ReasoningTokens != 0 {
		fields["reasoningTokens"] = u.ReasoningTokens
	}
	if u.CachedInputTokens != 0 {
		fields["cachedInputTokens"] = u.CachedInputTokens
	}
	return fields
}
