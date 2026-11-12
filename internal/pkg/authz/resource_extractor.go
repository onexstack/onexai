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

package authz

import "context"

// ResourceExtractor turns a Tool call's raw JSON arguments into a Resource +
// Verb, using a per-(mcpName, toolName) mapping configuration (see the
// mcpResourceExtractor implementation). Returning Resource{Kind:
// ResourceKindNone} with a nil error is how an implementation signals "this
// Tool has no configured resource mapping" — the absence of a mapping IS the
// "no resource, skip resource-level authorization" signal, not an error.
type ResourceExtractor interface {
	Extract(ctx context.Context, mcpName, toolName, argumentsInJSON string) (Resource, Verb, error)
}

// NoopResourceExtractor always reports "no resource", i.e. resource-level
// authorization is skipped for every Tool call.
type NoopResourceExtractor struct{}

func (NoopResourceExtractor) Extract(_ context.Context, _, _, _ string) (Resource, Verb, error) {
	return Resource{Kind: ResourceKindNone}, "", nil
}
