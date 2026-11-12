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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func promptSpec(template string, params ...agent.CommandParameter) *agent.CustomCommandSpec {
	return &agent.CustomCommandSpec{
		Type:       "prompt",
		Prompt:     &agent.PromptCommandSpec{Template: template},
		Parameters: params,
	}
}

func httpOutputSpec(template string, params ...agent.CommandParameter) *agent.CustomCommandSpec {
	return &agent.CustomCommandSpec{
		Type: "http",
		HTTP: &agent.HTTPCommandSpec{Method: "GET", URL: "https://example.com/x"},
		Output: &agent.CommandOutputSpec{
			Format:   "template",
			Template: template,
		},
		Parameters: params,
	}
}

func TestValidateCustomCommandAcceptsTemplateFields(t *testing.T) {
	name := agent.CommandParameter{Name: "name", Type: "string", Required: true}
	changes := agent.CommandParameter{Name: "changes", Type: "array"}

	tests := []struct {
		name string
		spec *agent.CustomCommandSpec
	}{
		{"declared parameter", promptSpec("你好 {{.Params.name}}", name)},
		{"params map", promptSpec("{{.Params}}", name)},
		{"command", promptSpec("{{.Command}}", name)},
		{"agent", promptSpec("{{.Agent}}", name)},
		{"conversation", promptSpec("{{.Conversation.conversationId}} {{.Conversation.chatType}} {{.Conversation.surface}}", name)},
		{"user", promptSpec("{{.User.Username}} {{.User.UID}} {{.User.Groups}} {{.User.Extra}}", name)},
		{"dry run", promptSpec("{{.DryRun}}", name)},
		{"now method", promptSpec(`{{.Now.Format "2006-01-02"}}`, name)},
		{"dot", promptSpec("{{.}}", name)},
		{"root variable", promptSpec("{{$.Params.name}}", name)},
		{"local variable", promptSpec("{{$p := .Params}}{{$p.anything}}", name)},
		{"dynamic index", promptSpec(`{{index .Params "name"}}`, name)},
		{"range body dot", promptSpec("{{range .Params.changes}}{{.}}{{end}}", name, changes)},
		{"with user", promptSpec("{{with .User}}{{.Username}}{{end}}", name)},
		{"define called with dot", promptSpec(`{{define "x"}}{{.Params.name}}{{end}}{{template "x" .}}`, name)},
		{"define with user scope", promptSpec(`{{define "x"}}{{.Username}}{{end}}{{template "x" .User}}`, name)},
		{
			"output scope",
			httpOutputSpec(`HTTP {{.Status}} {{index .Header "Content-Type"}} {{.Body}} {{.JSON.data.title}} {{.Params.name}}`, name),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errs := ValidateCustomCommand(tt.spec, field.NewPath("spec")); len(errs) > 0 {
				t.Fatalf("valid template was rejected: %v", errs)
			}
		})
	}
}

func TestValidateCustomCommandRejectsUnknownTemplateFields(t *testing.T) {
	name := agent.CommandParameter{Name: "name", Type: "string", Required: true}

	tests := []struct {
		name string
		spec *agent.CustomCommandSpec
		want string
	}{
		{"singular Param", promptSpec("你好 {{.Param.name}}", name), ".Params"},
		{"bare Param", promptSpec("{{.Param}}", name), ".Params"},
		{"undeclared param", promptSpec("{{.Params.city}}", name), "available parameter fields"},
		{"unknown conversation field", promptSpec("{{.Conversation.thread}}", name), "available conversation fields"},
		{"unknown user field", promptSpec("{{.User.Name}}", name), "available user fields"},
		{"unknown root", promptSpec("{{.Nope}}", name), "available fields"},
		{"field on scalar", promptSpec("{{.Command.foo}}", name), "is a scalar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateCustomCommand(tt.spec, field.NewPath("spec"))
			if len(errs) == 0 {
				t.Fatalf("invalid template was accepted")
			}
			var details strings.Builder
			for _, err := range errs {
				details.WriteString(err.Detail)
				details.WriteString(" ")
			}
			if !strings.Contains(details.String(), tt.want) {
				t.Errorf("errors = %q, want %q", details.String(), tt.want)
			}
		})
	}
}

func TestValidateCustomCommandRejectsCommandFieldsInOutput(t *testing.T) {
	errs := ValidateCustomCommand(
		httpOutputSpec("{{.Command}}", agent.CommandParameter{Name: "name", Type: "string"}),
		field.NewPath("spec"),
	)
	if len(errs) == 0 {
		t.Fatal("output template referencing .Command was accepted")
	}
	if !strings.Contains(errs[0].Detail, "available output fields") {
		t.Errorf("error = %q, want the output field list", errs[0].Detail)
	}
}

func TestValidateCustomCommandChecksHTTPWireTemplates(t *testing.T) {
	spec := &agent.CustomCommandSpec{
		Type: "http",
		HTTP: &agent.HTTPCommandSpec{
			Method: "POST",
			URL:    "https://example.com/{{.Params.service}}",
			Headers: []agent.HTTPHeader{
				{Name: "X-Service", Value: "{{.Param.service}}"},
			},
			Body: `{"service":"{{.Params.service}}"}`,
		},
		Parameters: []agent.CommandParameter{{Name: "service", Type: "string", Required: true}},
	}
	errs := ValidateCustomCommand(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("a .Param reference in an http header was accepted")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Detail, ".Params") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("errors = %v, want a .Params suggestion", errs)
	}
}
