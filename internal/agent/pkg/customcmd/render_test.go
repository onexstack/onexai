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

package customcmd

import (
	"strings"
	"testing"
)

func TestRenderTemplateExplainsSingularParam(t *testing.T) {
	data := TemplateData{Params: map[string]any{"name": "世界"}}
	_, err := RenderTemplate("prompt", "你好 {{.Param.name}}", data)
	if err == nil {
		t.Fatal("a singular .Param was rendered")
	}
	for _, want := range []string{".Params", "已声明参数：name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

func TestRenderTemplateReportsUndeclaredParameter(t *testing.T) {
	data := TemplateData{Params: map[string]any{"name": "世界"}}
	_, err := RenderTemplate("prompt", "{{.Params.city}}", data)
	if err == nil {
		t.Fatal("an undeclared parameter was rendered")
	}
	if !strings.Contains(err.Error(), "未声明的参数") {
		t.Errorf("error = %q, want the undeclared-parameter hint", err)
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error = %q, want the declared parameter list", err)
	}
}

func TestRenderTemplateKeepsNonFieldErrors(t *testing.T) {
	// User is nil: this is an execution error, not a field typo. The hint must
	// not pretend otherwise.
	data := TemplateData{Params: map[string]any{"name": "世界"}}
	_, err := RenderTemplate("prompt", "{{.User.Username}}", data)
	if err == nil {
		t.Fatal("a nil user was rendered")
	}
	if strings.Contains(err.Error(), "可用字段") {
		t.Errorf("error = %q, want the original execution error", err)
	}
}

func TestRenderWithUsesOutputScope(t *testing.T) {
	data := OutputTemplateData{Params: map[string]any{"name": "世界"}}
	_, err := RenderWith("output", "{{.Command}}", data)
	if err == nil {
		t.Fatal("an output template referencing .Command was rendered")
	}
	if !strings.Contains(err.Error(), "可用字段：Status、Header、Body、JSON、Params") {
		t.Errorf("error = %q, want the output field list", err)
	}
}

func TestRenderTemplateAcceptsDeclaredFields(t *testing.T) {
	data := TemplateData{
		Params:       map[string]any{"name": "世界"},
		Command:      "hello",
		Agent:        "helper",
		Conversation: map[string]string{"conversationId": "oc_1"},
	}
	got, err := RenderTemplate("prompt",
		`{{.Command}} {{.Agent}} {{.Conversation.conversationId}} {{.Params.name}}`, data)
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if got != "hello helper oc_1 世界" {
		t.Errorf("rendered %q", got)
	}
}
