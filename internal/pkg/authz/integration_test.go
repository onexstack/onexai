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

import (
	"context"
	"testing"
)

// TestEndToEndResourceMappingDrivenAuthorization exercises the full chain
// end-to-end: Coordinator.AuthorizeToolCall plus a real mcpResourceExtractor
// built the same way the engine assembles one from MCPM.ResourceMappings,
// verifying:
//   - a Tool with no configured resource mapping is never resource-checked;
//   - a Tool with a configured mapping correctly extracts the Resource from
//     its call arguments and the resource-level Authorizer sees it;
//   - a resource-level Deny overrides a tool-level Allow.
func TestEndToEndResourceMappingDrivenAuthorization(t *testing.T) {
	ctx := context.Background()

	mcpResourceMappingsRaw := map[string]string{
		"k8sgpt": `[{"tool_name":"get-resource","resource_kind":"namespace","name_path":"namespace"}]`,
		"cmdb":   "", // no resource mapping configured for this MCP
	}
	index, parseErrs := BuildResourceMappingIndex(mcpResourceMappingsRaw)
	if len(parseErrs) != 0 {
		t.Fatalf("unexpected parse errors: %v", parseErrs)
	}
	extractor := NewMCPResourceExtractor(index)

	var resourceCheckedWith Resource
	resourceCalled := false
	resourceAuthz := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		resourceCalled = true
		resourceCheckedWith = req.Resource
		if req.Resource.Name == "restricted-ns" {
			return DecisionDeny, "namespace is restricted", nil
		}
		return DecisionAllow, "", nil
	})

	toolAllow := AuthorizerFunc(func(context.Context, AuthorizationRequest) (Decision, string, error) {
		return DecisionAllow, "", nil
	})
	coordinator := NewCoordinator(toolAllow, resourceAuthz, true)
	subj := Subject{Kind: SubjectKindUser, UserID: "u_alice"}

	// Case 1: cmdb_search has NO resource mapping configured for the "cmdb"
	// MCP -> resource-level authz must be skipped entirely.
	decision, _, err := coordinator.AuthorizeToolCall(ctx, subj, []string{"some-agent"}, "cmdb_search", "cmdb", `{"query":"anything"}`, extractor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow for a tool with no resource mapping", decision)
	}
	if resourceCalled {
		t.Fatal("resource authorizer must not be consulted for a tool with no resource mapping configured")
	}

	// Case 2: get-resource HAS a mapping for "k8sgpt" -> namespace should be
	// extracted from the call args and passed to the resource Authorizer.
	decision, _, err = coordinator.AuthorizeToolCall(ctx, subj, []string{"some-agent"}, "get-resource", "k8sgpt", `{"namespace":"prod","name":"my-pod"}`, extractor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow for an unrestricted namespace", decision)
	}
	if !resourceCalled {
		t.Fatal("resource authorizer should have been consulted for a tool with a configured resource mapping")
	}
	if resourceCheckedWith.Kind != ResourceKindK8sNamespace || resourceCheckedWith.Name != "prod" {
		t.Fatalf("got resource %+v, want namespace:prod extracted from the call args", resourceCheckedWith)
	}

	// Case 3: same Tool, but the extracted namespace is one the resource
	// Authorizer denies -> the whole call must be denied even though
	// Tool-level allowed it.
	decision, reason, err := coordinator.AuthorizeToolCall(ctx, subj, []string{"some-agent"}, "get-resource", "k8sgpt", `{"namespace":"restricted-ns","name":"secret-pod"}`, extractor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny for a namespace the resource authorizer restricts", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty denial reason")
	}
}

// TestEndToEndSREBypassSkipsBothLevels pins the SRE-member bypass: a caller
// flagged IsSRE on a bypass-listed tool skips tool- and resource-level checks
// even when every backend would deny.
