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

package mcpclient

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// recordedTool mimics the eino MCP adapter: it owns a wire name that only its
// own InvokableRun uses, exactly as einomcp's toolHelper does. It records every
// name it is asked to call, so a test can prove the wire name — not the
// sanitised one — is what reaches the server.
type recordedTool struct {
	wire  string
	out   string
	err   error
	calls *[]string
}

var _ tool.InvokableTool = (*recordedTool)(nil)

func (t *recordedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.wire, Desc: "desc of " + t.wire}, nil
}

func (t *recordedTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	if t.calls != nil {
		*t.calls = append(*t.calls, t.wire)
	}
	return t.out, t.err
}

func TestWireToolInfoIsSanitized(t *testing.T) {
	inner := &recordedTool{wire: "kubernetes_resource.list"}
	got, err := newWireTool(context.Background(), inner, "kubernetes_resource_list")
	if err != nil {
		t.Fatalf("newWireTool: %v", err)
	}

	info, err := got.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "kubernetes_resource_list" {
		t.Fatalf("model-facing name = %q, want it sanitised", info.Name)
	}
	if info.Desc != "desc of kubernetes_resource.list" {
		t.Fatalf("desc = %q, want the server's description preserved", info.Desc)
	}
}

// TestWireToolCallsWireName is the guard for the one detail that would break
// every tool call silently: the model must see a sanitised name while the
// server is still addressed by its original, dotted name.
func TestWireToolCallsWireName(t *testing.T) {
	var calls []string
	inner := &recordedTool{wire: "kubernetes_pod.logs", out: "ok", calls: &calls}

	got, err := newWireTool(context.Background(), inner, "kubernetes_pod_logs")
	if err != nil {
		t.Fatalf("newWireTool: %v", err)
	}
	if _, err := got.InvokableRun(context.Background(), `{"namespace":"default"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	if len(calls) != 1 || calls[0] != "kubernetes_pod.logs" {
		t.Fatalf("inner tool was called as %v, want the original wire name", calls)
	}
}

func TestWireToolPropagatesError(t *testing.T) {
	// The wrapper must not swallow errors: converting them into model-visible
	// text is the error handler's job, installed separately by the manager.
	inner := &recordedTool{wire: "scan.run", err: errFake}
	got, err := newWireTool(context.Background(), inner, "scan_run")
	if err != nil {
		t.Fatalf("newWireTool: %v", err)
	}
	if _, err := got.InvokableRun(context.Background(), "{}"); err == nil {
		t.Fatal("want the inner error to propagate")
	}
}

func TestFlattenCallToolResult(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single text block",
			in:   `{"content":[{"type":"text","text":"hello"}]}`,
			want: "hello",
		},
		{
			name: "multiple text blocks are joined",
			in:   `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`,
			want: "a\nb",
		},
		{
			name: "non text blocks are ignored when text is present",
			in:   `{"content":[{"type":"image","data":"zzz"},{"type":"text","text":"a"}]}`,
			want: "a",
		},
		{
			name: "non text only keeps the envelope so nothing is lost",
			in:   `{"content":[{"type":"image","data":"zzz"}]}`,
			want: `{"content":[{"type":"image","data":"zzz"}]}`,
		},
		{
			name: "not an envelope",
			in:   "plain string output",
			want: "plain string output",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flattenCallToolResult(tt.in); got != tt.want {
				t.Fatalf("flattenCallToolResult(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
