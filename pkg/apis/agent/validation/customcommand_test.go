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

package validation

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func raw(v string) *runtime.RawExtension { return &runtime.RawExtension{Raw: []byte(v)} }

func TestValidateCustomCommandAcceptsValidDefinitions(t *testing.T) {
	tests := []struct {
		name string
		spec *agent.CustomCommandSpec
	}{
		{
			name: "prompt",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "请处理 {{ .Params.service }}"},
				Parameters: []agent.CommandParameter{
					{Name: "service", Type: "string", Required: true},
				},
			},
		},
		{
			name: "http",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{
					Method:      "POST",
					URL:         "https://example.com/deploy",
					Body:        `{"service":"{{ .Params.service }}"}`,
					SideEffects: "None",
				},
				Parameters: []agent.CommandParameter{
					{Name: "service", Type: "string", Required: true},
				},
				Output: &agent.CommandOutputSpec{Format: "json"},
			},
		},
		{
			name: "tool",
			spec: &agent.CustomCommandSpec{
				Type: "tool",
				Tool: &agent.ToolCommandSpec{
					MCPServer:         agent.Reference{Name: "k8s"},
					Tool:              "get_pod",
					ArgumentsTemplate: `{"name":"{{ .Params.pod }}"}`,
				},
				Parameters: []agent.CommandParameter{
					{Name: "pod", Type: "string", Required: true},
				},
			},
		},
		{
			name: "http against an in-cluster service",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{
					Method:  "POST",
					Service: &agent.ServiceReference{Name: "api", Namespace: "ops", Port: 8080, Path: "/deploy"},
				},
			},
		},
		{
			name: "defaults and enums",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "env", Type: "string", Enum: []string{"dev", "prod"}, Default: raw(`"dev"`)},
					{Name: "count", Type: "integer", Default: raw(`3`)},
					{Name: "obj", Type: "object", Schema: raw(`{"type":"object"}`)},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errs := ValidateCustomCommand(tt.spec, field.NewPath("spec")); len(errs) > 0 {
				t.Fatalf("valid definition was rejected: %v", errs)
			}
		})
	}
}

func TestValidateCustomCommandRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name string
		spec *agent.CustomCommandSpec
	}{
		{
			name: "missing type",
			spec: &agent.CustomCommandSpec{Prompt: &agent.PromptCommandSpec{Template: "x"}},
		},
		{
			name: "two bodies",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Tool:   &agent.ToolCommandSpec{MCPServer: agent.Reference{Name: "m"}, Tool: "t"},
			},
		},
		{
			name: "type and body disagree",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				Tool: &agent.ToolCommandSpec{MCPServer: agent.Reference{Name: "m"}, Tool: "t"},
			},
		},
		{
			name: "prompt with output",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Output: &agent.CommandOutputSpec{Format: "json"},
			},
		},
		{
			name: "bad template",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "{{ .Params"},
			},
		},
		{
			name: "templated host",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{URL: "https://{{ .Params.host }}/x"},
			},
		},
		{
			name: "get with body",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{Method: "GET", URL: "https://example.com", Body: "x"},
			},
		},
		{
			name: "url and service together",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{
					URL:     "https://example.com",
					Service: &agent.ServiceReference{Name: "svc"},
				},
			},
		},
		{
			name: "service name is an address",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{
					Service: &agent.ServiceReference{Name: "169.254.169.254", Port: 80},
				},
			},
		},
		{
			name: "service namespace is not a label",
			spec: &agent.CustomCommandSpec{
				Type: "http",
				HTTP: &agent.HTTPCommandSpec{
					Service: &agent.ServiceReference{Name: "api", Namespace: "evil.example.com"},
				},
			},
		},
		{
			name: "required with default",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "env", Required: true, Default: raw(`"dev"`)},
				},
			},
		},
		{
			name: "duplicate parameter",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "env"}, {Name: "env"},
				},
			},
		},
		{
			name: "enum on object",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "obj", Type: "object", Enum: []string{"a"}},
				},
			},
		},
		{
			name: "default of the wrong type",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "count", Type: "integer", Default: raw(`"three"`)},
				},
			},
		},
		{
			name: "required positional after optional",
			spec: &agent.CustomCommandSpec{
				Type:   "prompt",
				Prompt: &agent.PromptCommandSpec{Template: "x"},
				Parameters: []agent.CommandParameter{
					{Name: "a", In: "positional"},
					{Name: "b", In: "positional", Required: true},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errs := ValidateCustomCommand(tt.spec, field.NewPath("spec")); len(errs) == 0 {
				t.Fatalf("invalid definition was accepted")
			}
		})
	}
}

func TestValidateCustomCommandName(t *testing.T) {
	if errs := ValidateCustomCommandName("deploy", field.NewPath("metadata", "name")); len(errs) > 0 {
		t.Fatalf("valid name rejected: %v", errs)
	}
	for _, name := range []string{"Deploy", "de_ploy", "-deploy", "", "a2345678901234567890123456789012345"} {
		if errs := ValidateCustomCommandName(name, field.NewPath("metadata", "name")); len(errs) == 0 {
			t.Errorf("invalid name %q was accepted", name)
		}
	}
}
