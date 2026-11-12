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

package customcommand

import (
	"testing"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// Warnings are for configuration that is legal but risky. An in-cluster
// service target is neither: it is the supported way to reach a private
// endpoint, and warning about it would train people to ignore warnings.
func TestWarningsForHTTPSettings(t *testing.T) {
	tests := []struct {
		name string
		spec agent.CustomCommandSpec
		want int
	}{
		{
			name: "prompt command",
			spec: agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
			},
			want: 0,
		},
		{
			name: "https with verification",
			spec: agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{URL: "https://example.com/deploy"},
			},
			want: 0,
		},
		{
			name: "verification disabled",
			spec: agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{URL: "https://example.com/deploy", InsecureSkipTLSVerify: true},
			},
			want: 1,
		},
		{
			name: "plain http to an external host",
			spec: agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{URL: "http://example.com/deploy"},
			},
			want: 1,
		},
		{
			name: "in-cluster service",
			spec: agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{Service: &agent.ServiceReference{Name: "api"}},
			},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &agent.CustomCommand{Spec: tc.spec}
			if got := warningsFor(obj); len(got) != tc.want {
				t.Errorf("warnings = %v, want %d", got, tc.want)
			}
		})
	}
}
