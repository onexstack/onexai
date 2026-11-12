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

func TestParseResourceMappingsTreatsEmptyStringAsNoMappings(t *testing.T) {
	mappings, err := ParseResourceMappings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mappings != nil {
		t.Fatalf("got %v, want nil for empty input", mappings)
	}
}

func TestParseResourceMappingsRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseResourceMappings("{not valid json"); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestParseResourceMappingsReadsTheArray(t *testing.T) {
	raw := `[
		{"tool_name":"get-resource","resource_kind":"namespace","name_path":"namespace","verb":"read"},
		{"tool_name":"cmdb_search","resource_kind":"cmdb_service","name_path":"service_name"}
	]`
	mappings, err := ParseResourceMappings(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("got %d mappings, want 2", len(mappings))
	}
	if mappings[0].ToolName != "get-resource" || mappings[0].Verb != VerbRead {
		t.Errorf("unexpected first mapping: %+v", mappings[0])
	}
}

func TestMCPResourceExtractorSkipsToolsWithoutMapping(t *testing.T) {
	extractor := NewMCPResourceExtractor(nil)
	resource, _, err := extractor.Extract(context.Background(), "k8sgpt", "get-resource", `{"namespace":"prod"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resource.IsEmpty() {
		t.Fatalf("got %+v, want an empty resource when no mapping is configured", resource)
	}
}

func TestMCPResourceExtractorReadsNamePathAndAttributes(t *testing.T) {
	index := map[string]map[string]ToolResourceMapping{
		"k8sgpt": {
			"get-resource": {
				ToolName: "get-resource", ResourceKind: ResourceKindK8sNamespace, NamePath: "namespace",
				AttributePaths: map[string]string{"cluster": "cluster_name"},
			},
		},
	}
	extractor := NewMCPResourceExtractor(index)

	resource, verb, err := extractor.Extract(context.Background(), "k8sgpt", "get-resource",
		`{"namespace":"prod","cluster_name":"eks-prod-1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resource.Kind != ResourceKindK8sNamespace || resource.Name != "prod" {
		t.Fatalf("got %+v, want namespace:prod", resource)
	}
	if resource.Attributes["cluster"] != "eks-prod-1" {
		t.Fatalf("got attributes %+v, want cluster=eks-prod-1", resource.Attributes)
	}
	if verb != VerbRead {
		t.Fatalf("got verb %v, want default VerbRead", verb)
	}
}

func TestMCPResourceExtractorReturnsEmptyWhenNamePathIsAbsent(t *testing.T) {
	index := map[string]map[string]ToolResourceMapping{
		"k8sgpt": {"get-resource": {ToolName: "get-resource", ResourceKind: ResourceKindK8sNamespace, NamePath: "namespace"}},
	}
	extractor := NewMCPResourceExtractor(index)

	resource, _, err := extractor.Extract(context.Background(), "k8sgpt", "get-resource", `{"other_field":"x"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resource.IsEmpty() {
		t.Fatalf("got %+v, want empty resource when the configured name_path is absent from args", resource)
	}
}

func TestMCPResourceExtractorRejectsMalformedArguments(t *testing.T) {
	index := map[string]map[string]ToolResourceMapping{
		"k8sgpt": {"get-resource": {ToolName: "get-resource", ResourceKind: ResourceKindK8sNamespace, NamePath: "namespace"}},
	}
	extractor := NewMCPResourceExtractor(index)

	if _, _, err := extractor.Extract(context.Background(), "k8sgpt", "get-resource", `not json`); err == nil {
		t.Fatal("expected an error for malformed tool call arguments")
	}
}

func TestBuildResourceMappingIndexSkipsEmptyAndMalformed(t *testing.T) {
	raw := map[string]string{
		"k8sgpt": `[{"tool_name":"get-resource","resource_kind":"namespace","name_path":"namespace"}]`,
		"cmdb":   `[{"tool_name":"cmdb_search","resource_kind":"service","name_path":"service_name"}]`,
		"noop":   "",
		"broken": "{not json",
	}
	index, errs := BuildResourceMappingIndex(raw)

	if len(index) != 2 {
		t.Fatalf("got %d indexed MCPs, want 2 (noop and broken excluded)", len(index))
	}
	if _, ok := index["k8sgpt"]["get-resource"]; !ok {
		t.Error("expected k8sgpt/get-resource mapping in index")
	}
	if _, ok := index["cmdb"]["cmdb_search"]; !ok {
		t.Error("expected cmdb/cmdb_search mapping in index")
	}
	if _, ok := index["noop"]; ok {
		t.Error("did not expect an entry for an MCP with no mappings configured")
	}
	if _, ok := errs["broken"]; !ok {
		t.Error("expected a parse error recorded for the broken MCP")
	}
}
