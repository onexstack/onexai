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

package observe

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/tool"
)

// TestToolObserverOnlyObservesTools pins the routing rule: the model handler
// and the tool handler share one global callback list, so each must ignore the
// other's calls. A tool span opened for a model call would make every run look
// like it ran a tool.
func TestToolObserverOnlyObservesTools(t *testing.T) {
	o := &toolObserver{}
	ctx := context.Background()

	// A model call's start.
	modelCtx := o.OnStart(ctx, &callbacks.RunInfo{Component: components.ComponentOfChatModel, Name: "m"}, nil)
	if st, _ := modelCtx.Value(toolStateKey{}).(*toolCallState); st != nil {
		t.Fatalf("a model call opened a tool state: %+v", st)
	}

	// A tool call's start.
	toolCtx := o.OnStart(ctx, &callbacks.RunInfo{Component: components.ComponentOfTool, Name: "delete_cluster"},
		&tool.CallbackInput{ArgumentsInJSON: `{"cluster":"test"}`})
	st, _ := toolCtx.Value(toolStateKey{}).(*toolCallState)
	if st == nil {
		t.Fatal("a tool call opened no state, so it would have no span")
	}
	if st.name != "delete_cluster" {
		t.Errorf("state names %q, want delete_cluster", st.name)
	}

	// The end closes it exactly once.
	o.OnEnd(toolCtx, nil, nil)
	if !st.done {
		t.Error("OnEnd left the call open")
	}
	o.OnError(toolCtx, nil, errors.New("late failure"))
	if st.span != nil && !st.done {
		t.Error("a late error reopened a finished call")
	}
}

// TestToolObserverIgnoresAnUnknownInput pins that a callback whose input is not
// a tool input does not open a span, even when the RunInfo claims Tool: eino
// passes nil for components that report no input, and a zero-value span for
// those would be noise.
func TestToolObserverIgnoresAnUnknownInput(t *testing.T) {
	o := &toolObserver{}
	ctx := o.OnStart(context.Background(),
		&callbacks.RunInfo{Component: components.ComponentOfTool, Name: "x"}, nil)
	if st, _ := ctx.Value(toolStateKey{}).(*toolCallState); st != nil {
		t.Fatalf("an inputless call opened a tool state: %+v", st)
	}
}
