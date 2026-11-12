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

package di

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// TestResourceMappingsForResolvesRequestSpellings pins the two properties the
// run path depends on: the returned key is the spelling the caller requested
// (ToolOrigins reports the same string), and a qualified request never borrows
// another namespace's mappings.
func TestResourceMappingsForResolvesRequestSpellings(t *testing.T) {
	const onexaiMappings = `[{"tool_name":"call","resource_kind":"k8s-namespace","name_path":"namespace"}]`

	rows := []*model.MCPM{
		{Name: "onexai", Namespace: "helper-public", ResourceMappings: onexaiMappings},
		{Name: "onexai", Namespace: "other-team", ResourceMappings: `[]`},
		{Name: "incident", Namespace: "helper-public", ResourceMappings: onexaiMappings},
		{Name: "empty", Namespace: "helper-public"},
	}
	lookup := resourceMappingsFor(context.Background(), &fakeAIStore{
		mcps: &fakeMCPStore{rows: rows},
	})

	out := lookup([]string{
		"helper-public/onexai",  // exact canonical hit
		"other-team/onexai",     // a different namespace, same name
		"incident",              // unique bare name
		"onexai",                // ambiguous: helper-public and other-team both hold it
		"helper-public/missing", // no such row; must not fall back to a bare "missing"
		"helper-public/empty",   // row exists but declares no mapping
	})

	if got, want := out["helper-public/onexai"], onexaiMappings; got != want {
		t.Errorf("helper-public/onexai = %q, want the helper-public mappings", got)
	}
	if got, ok := out["other-team/onexai"]; !ok || got != "[]" {
		t.Errorf("other-team/onexai = %q (present=%v), want its own namespace's mappings", got, ok)
	}
	if got, want := out["incident"], onexaiMappings; got != want {
		t.Errorf("incident = %q, want the unique bare-name row's mappings", got)
	}
	if _, ok := out["onexai"]; ok {
		t.Error("an ambiguous bare name must be omitted, not guessed")
	}
	for _, key := range []string{"helper-public/missing", "helper-public/empty"} {
		if _, ok := out[key]; ok {
			t.Errorf("%s must not resolve", key)
		}
	}
}

// TestResourceMappingsForFeedsTheResourceExtractor closes the loop: the map the
// lookup returns must index cleanly under the canonical MCPName ToolOrigins
// reports, or AuthorizeToolCall silently skips the resource-level check.
func TestResourceMappingsForFeedsTheResourceExtractor(t *testing.T) {
	const mappings = `[{"tool_name":"get_pod","resource_kind":"k8s-namespace","name_path":"namespace"}]`
	lookup := resourceMappingsFor(context.Background(), &fakeAIStore{
		mcps: &fakeMCPStore{rows: []*model.MCPM{
			{Name: "onexai", Namespace: "helper-public", ResourceMappings: mappings},
		}},
	})

	const mcpName = "helper-public/onexai"
	raw := lookup([]string{mcpName})
	index, errs := authz.BuildResourceMappingIndex(raw)
	if len(errs) != 0 {
		t.Fatalf("BuildResourceMappingIndex: %v", errs)
	}

	resource, _, err := authz.NewMCPResourceExtractor(index).
		Extract(context.Background(), mcpName, "get_pod", `{"namespace":"prod"}`)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if resource.Kind == authz.ResourceKindNone || resource.Name != "prod" {
		t.Fatalf("got %#v, want the extracted k8s-namespace resource", resource)
	}
}

var _ store.MCPStore = (*fakeMCPStore)(nil)
