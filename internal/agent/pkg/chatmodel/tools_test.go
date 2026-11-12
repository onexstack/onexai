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

package chatmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/onexstack/onexstack/pkg/ptr"
)

// TestBuildRequestSendsToolParameters guards the wire format the model needs to
// call a tool with arguments at all: without the parameters schema every tool
// reaches the model as a zero-argument function.
func TestBuildRequestSendsToolParameters(t *testing.T) {
	props := jsonschema.NewProperties()
	props.Set("namespace", &jsonschema.Schema{Type: "string", Description: "the namespace to read"})
	params := &jsonschema.Schema{
		Type:       "object",
		Properties: props,
		Required:   []string{"namespace"},
	}
	info := &schema.ToolInfo{
		Name:        "kubernetes_pod_logs",
		Desc:        "Read pod logs.",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(params),
	}

	m := &OpenAIChatModel{tools: []*schema.ToolInfo{info}}
	req, err := m.buildRequest([]*schema.Message{schema.UserMessage("list pods")}, false, "test-model", m.tools, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("got %d tool specs, want 1", len(req.Tools))
	}

	spec := req.Tools[0]
	if spec.Type != "function" || spec.Function.Name != "kubernetes_pod_logs" {
		t.Fatalf("unexpected tool spec: %+v", spec)
	}
	if len(spec.Function.Parameters) == 0 {
		t.Fatal("parameters are missing: the model would see a zero-argument function")
	}

	var got struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &got); err != nil {
		t.Fatalf("parameters are not valid JSON (%v): %s", err, spec.Function.Parameters)
	}
	if got.Type != "object" {
		t.Fatalf("parameters type = %q, want object", got.Type)
	}
	if _, ok := got.Properties["namespace"]; !ok {
		t.Fatalf("parameters lost the namespace property: %s", spec.Function.Parameters)
	}
	if len(got.Required) != 1 || got.Required[0] != "namespace" {
		t.Fatalf("parameters lost the required list: %s", spec.Function.Parameters)
	}

	// The whole request must still serialise, since that is what goes on the wire.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(raw), `"parameters"`) {
		t.Fatalf("marshalled request has no parameters: %s", raw)
	}
}

// TestBuildRequestOmitsEmptyParameters pins the other half: a tool that declares
// no parameters keeps the field out of the payload rather than sending null.
func TestBuildRequestOmitsEmptyParameters(t *testing.T) {
	m := &OpenAIChatModel{tools: []*schema.ToolInfo{{Name: "current_time", Desc: "now"}}}
	req, err := m.buildRequest(nil, false, "test-model", m.tools, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	spec := req.Tools[0]
	if spec.Function.Parameters != nil {
		t.Fatalf("parameters = %s, want omitted", spec.Function.Parameters)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"parameters"`) {
		t.Fatalf("parameters should be omitted from the payload: %s", raw)
	}
}

// TestCommonOptionsHonoursCallTools covers how eino actually binds tools: the
// adk passes them as a call option instead of calling WithTools, so a model that
// drops its options reaches the LLM with no tools at all and the model answers
// that it cannot do the job. That failure is indistinguishable from a refusal in
// the transcript, hence this test.
func TestCommonOptionsHonoursCallTools(t *testing.T) {
	m := &OpenAIChatModel{tools: []*schema.ToolInfo{{Name: "base_tool"}}}

	// With no options the model keeps what it was constructed with.
	if got := m.commonOptions(nil).Tools; len(got) != 1 || got[0].Name != "base_tool" {
		t.Fatalf("without options the base tools were lost: %+v", got)
	}

	// A call option replaces them (this is the adk path).
	call := []*schema.ToolInfo{{Name: "kubernetes_resource_list"}}
	got := m.commonOptions([]model.Option{model.WithTools(call)}).Tools
	if len(got) != 1 || got[0].Name != "kubernetes_resource_list" {
		t.Fatalf("the call's tools did not win: %+v", got)
	}

	// An empty tool list clears them rather than inheriting the base set.
	if got := m.commonOptions([]model.Option{model.WithTools(nil)}).Tools; len(got) != 0 {
		t.Fatalf("an explicit empty tool list should clear the base tools, got %+v", got)
	}
}

// TestWithToolsIsImmutable pins that binding tools does not mutate the receiver,
// which the manager relies on when handing the same model to concurrent runs.
func TestWithToolsIsImmutable(t *testing.T) {
	base := &OpenAIChatModel{model: "m"}
	bound, err := base.WithTools([]*schema.ToolInfo{{Name: "t"}})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if len(base.tools) != 0 {
		t.Fatal("WithTools mutated the receiver")
	}
	if got := len(bound.(*OpenAIChatModel).tools); got != 1 {
		t.Fatalf("the bound copy has %d tools, want 1", got)
	}
}

// TestToolCallAccumulatorRebuildsFragments pins the streaming reassembly: the
// provider delivers one call across deltas that share an index, and eino reads
// the id/type/name atomically per chunk while only arguments accumulate.
func TestToolCallAccumulatorRebuildsFragments(t *testing.T) {
	idx := func(i int) *int { return &i }
	frag := func(index int, id, name, args string) wireToolCall {
		tc := wireToolCall{Index: idx(index), ID: id, Type: "function"}
		tc.Function.Name = name
		tc.Function.Arguments = args
		return tc
	}

	var a toolCallAccumulator
	a.add(frag(0, "call_1", "kubernetes_resource_list", ""))
	a.add(frag(0, "", "", `{"kind":`))
	a.add(frag(0, "", "", `"namespaces"}`))
	a.add(frag(1, "call_2", "current_time", "{}"))

	message := a.message()
	if message == nil {
		t.Fatal("no message assembled")
	}
	if len(message.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(message.ToolCalls))
	}
	first := message.ToolCalls[0]
	if first.ID != "call_1" || first.Function.Name != "kubernetes_resource_list" {
		t.Fatalf("unexpected first call: %+v", first)
	}
	if first.Function.Arguments != `{"kind":"namespaces"}` {
		t.Fatalf("arguments were not concatenated: %q", first.Function.Arguments)
	}
	if !ptr.EqualTo(first.Index, 0) {
		t.Fatalf("index was lost: %+v", first.Index)
	}
	if message.Content != "" {
		t.Fatalf("the assembled message must carry no content (it was already streamed): %q", message.Content)
	}

	// No tool calls at all must yield no trailing message.
	var empty toolCallAccumulator
	if empty.message() != nil {
		t.Fatal("an empty accumulator should not produce a message")
	}
}
