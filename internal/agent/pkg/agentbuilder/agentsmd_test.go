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
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	adkfilesystem "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

// readableBackend is enough of a sandbox to answer a read: the AGENTS.md loader
// uses Read and nothing else.
type readableBackend struct {
	adkfilesystem.Backend
	files map[string]string
}

func (b readableBackend) Read(_ context.Context, req *adkfilesystem.ReadRequest) (*adkfilesystem.FileContent, error) {
	content, ok := b.files[req.FilePath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &adkfilesystem.FileContent{Content: content}, nil
}

// TestAgentsMDInjectsOnceBeforeTheFirstUserTurn drives the middleware, not just
// its constructor: the file's content must reach the model, and the injection
// must be idempotent so a tool loop does not carry the same conventions once per
// model call.
func TestAgentsMDInjectsOnceBeforeTheFirstUserTurn(t *testing.T) {
	backend := readableBackend{files: map[string]string{"AGENTS.md": "# 仓库约定\n- 用中文回答\n"}}
	b := NewBuilder(Deps{}, Options{AgentsMD: AgentsMDOptions{Enabled: true, MaxBytes: 4096}})
	mw := b.agentsMDHandler(context.Background(), backend)
	if mw == nil {
		t.Fatal("the middleware was not installed")
	}

	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("怎么部署？")}}
	_, got, err := mw.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want the question plus the injected conventions", len(got.Messages))
	}
	if !strings.Contains(got.Messages[0].Content, "仓库约定") {
		t.Fatalf("the first message is %q, want the conventions", got.Messages[0].Content)
	}

	// A second pass over the rewritten state adds nothing.
	_, again, err := mw.BeforeModelRewriteState(context.Background(), got, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState (second): %v", err)
	}
	if len(again.Messages) != 2 {
		t.Fatalf("the conventions were injected again: %d messages", len(again.Messages))
	}
}

func TestAgentsMDHandler(t *testing.T) {
	backend := readableBackend{files: map[string]string{"AGENTS.md": "# 约定\n"}}

	cases := []struct {
		name    string
		opts    AgentsMDOptions
		backend adkfilesystem.Backend
		want    bool
	}{
		{name: "off", opts: AgentsMDOptions{}, backend: backend, want: false},
		{name: "on without a sandbox", opts: AgentsMDOptions{Enabled: true}, backend: nil, want: false},
		{name: "on with a sandbox", opts: AgentsMDOptions{Enabled: true}, backend: backend, want: true},
		{
			name:    "an explicit file list is accepted",
			opts:    AgentsMDOptions{Enabled: true, Files: []string{"AGENTS.md", "docs/CONVENTIONS.md"}, MaxBytes: 4096},
			backend: backend,
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder(Deps{}, Options{AgentsMD: tc.opts})
			got := b.agentsMDHandler(context.Background(), tc.backend)
			if (got != nil) != tc.want {
				t.Fatalf("agentsMDHandler = %v, want installed=%v", got, tc.want)
			}
		})
	}
}
