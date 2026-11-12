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
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// interruptErr builds a genuine eino interrupt error the way a gated tool
// does, so the tests exercise the real detection path rather than a stand-in.
func interruptErr(t *testing.T) error {
	t.Helper()
	err := tool.Interrupt(context.Background(), "please confirm")
	if err == nil {
		t.Fatal("tool.Interrupt returned a nil error; the test harness is wrong")
	}
	if !IsInterrupt(err) {
		t.Fatalf("IsInterrupt did not recognise an interrupt error: %v", err)
	}
	return err
}

func TestIsInterrupt(t *testing.T) {
	if IsInterrupt(nil) {
		t.Error("nil is not an interrupt")
	}
	if IsInterrupt(errors.New("boom")) {
		t.Error("a plain error is not an interrupt")
	}
	if !IsInterrupt(interruptErr(t)) {
		t.Error("a tool interrupt must be recognised")
	}
}

func TestToolErrorHandler_Invokable(t *testing.T) {
	h := NewToolErrorHandler()
	ctx := context.Background()

	t.Run("success passes through", func(t *testing.T) {
		wrapped, err := h.WrapInvokableToolCall(ctx, func(context.Context, string, ...tool.Option) (string, error) {
			return "ok", nil
		}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := wrapped(ctx, "{}")
		if err != nil || out != "ok" {
			t.Fatalf("got (%q, %v), want (\"ok\", nil)", out, err)
		}
	})

	t.Run("failure becomes model-visible text", func(t *testing.T) {
		wrapped, err := h.WrapInvokableToolCall(ctx, func(context.Context, string, ...tool.Option) (string, error) {
			return "", errors.New("connection refused")
		}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := wrapped(ctx, "{}")
		if err != nil {
			t.Fatalf("a tool failure must not surface as an error, got: %v", err)
		}
		if !strings.Contains(out, "connection refused") {
			t.Errorf("the model must see the cause, got: %q", out)
		}
	})

	t.Run("interrupt is never swallowed", func(t *testing.T) {
		want := interruptErr(t)
		wrapped, err := h.WrapInvokableToolCall(ctx, func(context.Context, string, ...tool.Option) (string, error) {
			return "", want
		}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		out, gotErr := wrapped(ctx, "{}")
		if !IsInterrupt(gotErr) {
			t.Fatalf("an interrupt must pass through unchanged, got (%q, %v)", out, gotErr)
		}
	})
}

func TestToolErrorHandler_Streamable(t *testing.T) {
	h := NewToolErrorHandler()
	ctx := context.Background()

	t.Run("failure becomes a one-chunk stream", func(t *testing.T) {
		wrapped, err := h.WrapStreamableToolCall(ctx,
			func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
				return nil, errors.New("upstream 503")
			}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		sr, err := wrapped(ctx, "{}")
		if err != nil {
			t.Fatalf("a streamable failure must not surface as an error, got: %v", err)
		}
		defer sr.Close()
		got, err := sr.Recv()
		if err != nil {
			t.Fatalf("expected one chunk, got error: %v", err)
		}
		if !strings.Contains(got, "upstream 503") {
			t.Errorf("the model must see the cause, got: %q", got)
		}
	})

	t.Run("interrupt is never swallowed", func(t *testing.T) {
		want := interruptErr(t)
		wrapped, err := h.WrapStreamableToolCall(ctx,
			func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
				return nil, want
			}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if _, gotErr := wrapped(ctx, "{}"); !IsInterrupt(gotErr) {
			t.Fatalf("an interrupt must pass through unchanged, got: %v", gotErr)
		}
	})

	t.Run("mid-stream failure is appended", func(t *testing.T) {
		broken := func() *schema.StreamReader[string] {
			r, w := schema.Pipe[string](4)
			go func() {
				_ = w.Send("partial", nil)
				w.Send("", errors.New("stream broke"))
				w.Close()
			}()
			return r
		}
		wrapped, err := h.WrapStreamableToolCall(ctx,
			func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
				return broken(), nil
			}, &adk.ToolContext{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		sr, err := wrapped(ctx, "{}")
		if err != nil {
			t.Fatal(err)
		}
		defer sr.Close()

		var sb strings.Builder
		for {
			chunk, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("the wrapper must not surface a stream error, got: %v", err)
			}
			sb.WriteString(chunk)
		}
		if !strings.Contains(sb.String(), "partial") || !strings.Contains(sb.String(), "stream broke") {
			t.Errorf("expected the partial output and the failure cause, got: %q", sb.String())
		}
	})
}
