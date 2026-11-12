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

package a2a

import (
	a2atype "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/onexstack/onexai/internal/agent/pkg/a2awire"
)

// Metadata keys and extension URIs this service and its runtimes agree on. They
// live in the leaf a2awire package so a runtime binary can speak the same
// contract without importing the gateway; these aliases keep the names this
// package has always used.
const (
	MetadataPrefix             = a2awire.MetadataPrefix
	PartTypeMetadataKey        = a2awire.PartTypeMetadataKey
	ThoughtMetadataKey         = a2awire.ThoughtMetadataKey
	UsageMetadataKey           = a2awire.UsageMetadataKey
	OutputSchemaSHA256Metadata = a2awire.OutputSchemaSHA256Metadata

	// HITLExtensionURI advertises human approval and user-input pauses.
	HITLExtensionURI = a2awire.HITLExtensionURI
	// UsageExtensionURI advertises task-lifetime token usage.
	UsageExtensionURI = a2awire.UsageExtensionURI
	// StructuredOutputExtensionURI advertises a schema-validated final answer.
	StructuredOutputExtensionURI = a2awire.StructuredOutputExtensionURI
)

// Extension returns a capability declaration for uri. Every extension this
// service emits is optional: a client that does not understand it must still
// be able to read the plain text answer.
func Extension(uri, description string, params map[string]any) a2atype.AgentExtension {
	return a2atype.AgentExtension{
		URI:         uri,
		Description: description,
		Required:    false,
		Params:      params,
	}
}
