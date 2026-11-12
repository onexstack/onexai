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

package opa

import (
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// TestBuildRegoInputCarriesTheBareResourceName pins the Go-to-Rego field pair:
// a mismatch here presents as a silent all-NoOpinion denial, not an error.
func TestBuildRegoInputCarriesTheBareResourceName(t *testing.T) {
	tests := []struct {
		name string
		req  authz.AuthorizationRequest
		want string
	}{
		{
			name: "canonical resource reference",
			req: authz.AuthorizationRequest{
				Resource: authz.Resource{Kind: authz.ResourceKindMCP, Name: "helper-public/onexai"},
			},
			want: "onexai",
		},
		{
			name: "bare resource reference",
			req: authz.AuthorizationRequest{
				Resource: authz.Resource{Kind: authz.ResourceKindMCP, Name: "onexai"},
			},
			want: "onexai",
		},
		{
			name: "tool-level request has no qualifier",
			req:  authz.AuthorizationRequest{ToolName: "pod"},
			want: "pod",
		},
		{
			name: "agent-level request has no qualifier",
			req:  authz.AuthorizationRequest{AgentName: "fz-default"},
			want: "fz-default",
		},
		{
			name: "no resource",
			req:  authz.AuthorizationRequest{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := buildRegoInput(tt.req)["resource_name_bare"].(string)
			if !ok {
				t.Fatalf("resource_name_bare missing or not a string: %#v", buildRegoInput(tt.req))
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
