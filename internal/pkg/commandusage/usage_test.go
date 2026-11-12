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

package commandusage

import "testing"

func TestArguments(t *testing.T) {
	params := []Parameter{
		{Name: "service", In: "positional", Required: true},
		{Name: "env", Type: "string"},
	}
	if got, want := Arguments(params), "<service> [--env=<env>]"; got != want {
		t.Errorf("Arguments = %q, want %q", got, want)
	}
	if got := Arguments(nil); got != "" {
		t.Errorf("Arguments(nil) = %q, want empty", got)
	}
}

func TestLine(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		params []Parameter
		want   string
	}{
		{
			name: "no parameters",
			cmd:  "hello",
			want: "/hello",
		},
		{
			name: "required positional",
			cmd:  "hello",
			params: []Parameter{
				{Name: "name", In: "positional", Type: "string", Required: true},
			},
			want: "/hello <name>",
		},
		{
			name: "optional positional",
			cmd:  "hello",
			params: []Parameter{
				{Name: "name", In: "positional", Type: "string"},
			},
			want: "/hello [name]",
		},
		{
			name: "positionals precede flags",
			cmd:  "deploy",
			params: []Parameter{
				{Name: "env", Type: "string"},
				{Name: "service", In: "positional", Type: "string", Required: true},
				{Name: "dry-run", Type: "boolean"},
				{Name: "replicas", Type: "integer", Required: true},
			},
			want: "/deploy <service> [--env=<env>] [--dry-run] --replicas=<replicas>",
		},
		{
			name: "array and boolean flags",
			cmd:  "tag",
			params: []Parameter{
				{Name: "labels", Type: "array"},
				{Name: "force", Type: "boolean"},
			},
			want: "/tag [--labels=<labels>,...] [--force]",
		},
		{
			name: "default type is string",
			cmd:  "greet",
			params: []Parameter{
				{Name: "name", Required: true},
			},
			want: "/greet --name=<name>",
		},
		{
			name: "empty parameter names are skipped",
			cmd:  "x",
			params: []Parameter{
				{Name: "", In: "positional", Required: true},
			},
			want: "/x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Line(tt.cmd, tt.params); got != tt.want {
				t.Errorf("Line(%q, %v) = %q, want %q", tt.cmd, tt.params, got, tt.want)
			}
		})
	}
}
