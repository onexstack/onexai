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

package agentbuilder

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/onexstack/onexai/pkg/options"
)

func TestUnknownToolResult(t *testing.T) {
	handler := unknownToolResult([]string{"read_file", "search_knowledge_base"})

	got, err := handler(context.Background(), "delete_everything", `{"path":"/"}`)
	if err != nil {
		t.Fatalf("handler returned an error, which would fail the run: %v", err)
	}
	for _, want := range []string{"delete_everything", "read_file", "search_knowledge_base"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply does not mention %q: %s", want, got)
		}
	}
}

func TestUnknownToolResultWithoutTools(t *testing.T) {
	handler := unknownToolResult(nil)

	got, err := handler(context.Background(), "anything", "{}")
	if err != nil {
		t.Fatalf("handler returned an error, which would fail the run: %v", err)
	}
	if !strings.Contains(got, "no tools are available") {
		t.Errorf("reply does not say no tools are available: %s", got)
	}
}

func TestUnknownToolResultCapsTheNameList(t *testing.T) {
	names := make([]string, maxUnknownToolNamesList+10)
	for i := range names {
		names[i] = fmt.Sprintf("tool_%02d", i)
	}
	handler := unknownToolResult(names)

	got, err := handler(context.Background(), "invented", "{}")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(got, "tool_00") || !strings.Contains(got, "tool_59") {
		t.Errorf("reply drops names inside the cap: %s", got)
	}
	if strings.Contains(got, "tool_60") {
		t.Errorf("reply lists a name past the cap: %s", got)
	}
}

func TestDanglingToolResult(t *testing.T) {
	got, err := danglingToolResult(context.Background(), "read_file", "call-1", nil)
	if err != nil {
		t.Fatalf("danglingToolResult: %v", err)
	}
	if got == nil || got.Content == "" {
		t.Fatal("danglingToolResult returned no content")
	}
	if !strings.Contains(got.Content, "read_file") || !strings.Contains(got.Content, "not executed") {
		t.Errorf("content does not say the call did not run: %s", got.Content)
	}
}

// TestSystemPrompt_IsReadPerBuild pins that the deployment-wide system prompt is
// resolved when the run is assembled, not when the builder is created: an
// operator's edit then reaches the next run instead of the next restart.
func TestSystemPrompt_IsReadPerBuild(t *testing.T) {
	live := "on call"
	b := testBuilder(t, Deps{}, Options{SystemPrompt: func() string { return live }})

	appended := func() string {
		t.Helper()
		handlers, err := b.assembleHandlers(context.Background(), Request{}, "agent", "", nil, nil, nil)
		if err != nil {
			t.Fatalf("assembleHandlers: %v", err)
		}
		for _, handler := range handlers {
			if h, ok := handler.(*instructionAppendHandler); ok {
				return h.text
			}
		}
		return ""
	}

	if got := appended(); got != "on call" {
		t.Fatalf("the appended prompt = %q, want %q", got, "on call")
	}

	live = "off call"
	if got := appended(); got != "off call" {
		t.Errorf("an edit is not visible to the next build: appended %q", got)
	}
}

// TestSystemPrompt_ConfigurationWins pins the precedence: a deployment that
// pinned the prompt in its configuration is never overridden by the runtime
// source, which is why the provider is not consulted at all.
func TestSystemPrompt_ConfigurationWins(t *testing.T) {
	called := false
	b := testBuilder(t, Deps{}, Options{
		Prompts:      options.PromptsConfig{System: "pinned"},
		SystemPrompt: func() string { called = true; return "runtime" },
	})

	if got := b.systemPrompt(); got != "pinned" {
		t.Errorf("systemPrompt = %q, want the configured prompt", got)
	}
	if called {
		t.Error("the runtime source must not be read when the configuration pinned a value")
	}
	if got := testBuilder(t, Deps{}, Options{}).systemPrompt(); got != "" {
		t.Errorf("a builder with no prompt source returned %q", got)
	}
}
