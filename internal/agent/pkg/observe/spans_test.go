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
	"sync"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// spanExporter collects this package's spans. The provider is installed once
// for the whole test binary: otel's global tracer binds to the first provider it
// resolves, so a provider swapped in per test would silently collect nothing
// after the first. Each test instead drains the exporter.
var (
	spanExporter *tracetest.InMemoryExporter
	spanOnce     sync.Once
)

// captureSpans returns the exporter, emptied for this test.
func captureSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()

	spanOnce.Do(func() {
		spanExporter = tracetest.NewInMemoryExporter()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spanExporter)))
	})
	spanExporter.Reset()
	return spanExporter
}

// attributes flattens a recorded span's attributes.
func attributes(span tracetest.SpanStub) map[string]any {
	attrs := make(map[string]any, len(span.Attributes))
	for _, attr := range span.Attributes {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attrs
}

func TestModelSpanFollowsTheGenAIConvention(t *testing.T) {
	exporter := captureSpans(t)
	o, _ := newObserver(t)

	ctx := agentapi.WithModelName(context.Background(), "deepseek")
	ctx = o.OnStart(ctx, nil, []*schema.Message{{Role: schema.User, Content: "hi"}})
	o.OnEnd(ctx, nil, &model.CallbackOutput{
		Message: &schema.Message{
			Role:         schema.Assistant,
			Content:      "hello",
			ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
		},
		TokenUsage: &model.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
	})

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "chat deepseek", spans[0].Name, "the convention names the span {operation} {model}")

	attrs := attributes(spans[0])
	assert.Equal(t, "chat", attrs["gen_ai.operation.name"])
	assert.Equal(t, "deepseek", attrs["gen_ai.request.model"])
	assert.Equal(t, "deepseek", attrs["onexai.model.endpoint"])
	assert.EqualValues(t, 10, attrs["gen_ai.usage.input_tokens"])
	assert.EqualValues(t, 5, attrs["gen_ai.usage.output_tokens"])
	assert.Equal(t, []string{"stop"}, attrs["gen_ai.response.finish_reasons"])
}

// TestModelSpanOmitsUsageItWasNotGiven pins that a provider which reports no
// counters produces a span without usage attributes, not with zeros.
func TestModelSpanOmitsUsageItWasNotGiven(t *testing.T) {
	exporter := captureSpans(t)
	o, _ := newObserver(t)

	ctx := o.OnStart(context.Background(), nil, []*schema.Message{{Role: schema.User, Content: "hi"}})
	o.OnEnd(ctx, nil, &schema.Message{Role: schema.Assistant, Content: "hello"})

	attrs := attributes(exporter.GetSpans()[0])
	assert.NotContains(t, attrs, "gen_ai.usage.input_tokens")
	assert.NotContains(t, attrs, "gen_ai.usage.output_tokens")
}

func TestToolSpanFollowsTheGenAIConvention(t *testing.T) {
	exporter := captureSpans(t)
	o := &toolObserver{}

	ctx := o.OnStart(context.Background(),
		&callbacks.RunInfo{Component: components.ComponentOfTool, Name: "search_knowledge_base"},
		&tool.CallbackInput{ArgumentsInJSON: `{"query":"x"}`})
	o.OnEnd(ctx, nil, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "execute_tool search_knowledge_base", spans[0].Name)

	attrs := attributes(spans[0])
	assert.Equal(t, "execute_tool", attrs["gen_ai.operation.name"])
	assert.Equal(t, "search_knowledge_base", attrs["gen_ai.tool.name"])
	assert.Equal(t, "function", attrs["gen_ai.tool.type"])
}
