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

// Package observe wires eino's callback mechanism to the service's metrics.
//
// It exists because the only latency the service could see before was the run
// and the tool: a slow provider and a slow tool were the same line in a
// dashboard. Eino injects a callback around every chat-model call the agent
// makes, so one handler here yields per-model duration, streaming first-chunk
// latency and failure counts without touching the model implementation or the
// agent's middleware order.
//
// It deliberately records nothing else. Token usage and tool timing already
// have exactly one writer each (reportRunUsage and the tool tracer); counting
// them here as well would double every number an operator reads.
package observe

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/telemetry"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

const (
	// statusOK and statusError are the two outcomes a model call can have.
	statusOK    = "ok"
	statusError = "error"
	// modelLabelDefault labels a run that resolved no endpoint name.
	modelLabelDefault = "default"
	// tracerName is the instrumentation scope, named after the service so a
	// span is traceable back to what produced it.
	tracerName = "github.com/onexstack/onexai/internal/agent"
	// spanNamePrefix names one model call's span. The GenAI semantic convention
	// is "{operation} {model}", so the model is appended when the span opens.
	spanNamePrefix = "chat "
	// toolSpanNamePrefix names one tool call's span, by the same convention.
	toolSpanNamePrefix = "execute_tool "
)

// tracer is package-level so every call shares one instrumentation scope.
var tracer = otel.Tracer(tracerName)

// registerOnce makes Register idempotent: the callback list is process-wide and
// appending the same handler twice would observably double every metric.
var registerOnce sync.Once

// Register installs the model-call observer on eino's global callback list.
//
// It is a no-op for a nil sink, which is what a deployment that failed to
// register its collectors has. Registration must happen before the first run
// begins; callers do it while building the server, which is before anything can
// query.
func Register(m *metrics.Metrics) {
	if m == nil {
		return
	}
	registerOnce.Do(func() {
		callbacks.AppendGlobalHandlers(&modelObserver{m: m}, &toolObserver{})
	})
}

// callState is one model call in flight. It is stored in the context the
// handler returns from OnStart, which eino threads to the matching terminal
// callback, so concurrent calls never share state.
type callState struct {
	start time.Time
	span  trace.Span
	// usage and finishReason are read off the response so the span carries the
	// same numbers the ledger does. Nil usage means the provider reported none,
	// which is recorded by omission rather than as a zero.
	usage        *tokenUsage
	finishReason string
	// done guards against a late OnError recording a second outcome for a call
	// that already ended: the agent above the model can fail after the model
	// succeeded, and that failure is not the model's.
	done bool
}

// stateKey is the context key for callState. An empty struct type cannot
// collide with another package's key.
type stateKey struct{}

// modelObserver records model calls as metrics and spans.
type modelObserver struct {
	m *metrics.Metrics
}

// Needed reports the timings this handler implements, so eino skips the stream
// copies and goroutine work for the ones it does not.
func (o *modelObserver) Needed(_ context.Context, _ *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	switch timing {
	case callbacks.TimingOnStart, callbacks.TimingOnEnd,
		callbacks.TimingOnError, callbacks.TimingOnEndWithStreamOutput:
		return true
	default:
		return false
	}
}

// OnStart begins a call when the input is a chat-model input. Every other
// component's start is ignored: this handler observes models and nothing else.
func (o *modelObserver) OnStart(
	ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput,
) context.Context {
	if model.ConvCallbackInput(input) == nil {
		return ctx
	}

	label := modelLabel(ctx)
	// The run's identity first, then this call's own attributes: every span of a
	// run carries the same identifiers, so a filter on run id or session works
	// on any of them.
	attrs := telemetry.SpanAttributes(ctx,
		// The semantic-convention names first: a dashboard built for any other
		// GenAI service can read these without a onexai-specific mapping. The
		// endpoint name is carried separately because it is what an operator
		// configured, and one endpoint can serve one model id.
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", label),
		attribute.String("onexai.model.endpoint", label),
	)
	ctx, span := tracer.Start(ctx, spanNamePrefix+label, trace.WithAttributes(attrs...))
	return context.WithValue(ctx, stateKey{}, &callState{start: time.Now(), span: span})
}

// OnEnd closes a non-streaming call.
func (o *modelObserver) OnEnd(
	ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput,
) context.Context {
	st := stateFrom(ctx)
	if st == nil || st.done {
		return ctx
	}
	st.done = true
	st.record(output)
	o.finish(ctx, st, statusOK)
	return ctx
}

// OnError closes a call that failed before producing its output.
func (o *modelObserver) OnError(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
	st := stateFrom(ctx)
	if st == nil || st.done {
		return ctx
	}
	st.done = true
	if st.span != nil {
		st.span.RecordError(err)
	}
	o.finish(ctx, st, statusError)
	return ctx
}

// OnEndWithStreamOutput closes a streaming call.
//
// The latency that matters for a stream is the first chunk, and the only way to
// see it is to read the handler's private copy. Reading it blocks until the
// stream ends, so it happens in a goroutine: doing it inline would hold the
// model's Stream call open until the answer had finished streaming.
func (o *modelObserver) OnEndWithStreamOutput(
	ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput],
) context.Context {
	st := stateFrom(ctx)
	if st == nil || st.done {
		return ctx
	}
	st.done = true

	label := modelLabel(ctx)
	go func() {
		// The copy is ours to close: eino only frees the original once every
		// handler has released its own.
		defer output.Close()

		first := true
		for {
			chunk, err := output.Recv()
			if err == nil {
				st.record(chunk)
			}
			if err != nil {
				o.finish(ctx, st, streamStatus(err))
				return
			}
			if first {
				first = false
				o.m.ObserveModelFirstToken(label, time.Since(st.start))
			}
		}
	}()

	return ctx
}

// OnStartWithStreamInput is unused: chat models receive whole messages, not
// streams. It is required by the interface.
func (o *modelObserver) OnStartWithStreamInput(
	ctx context.Context, _ *callbacks.RunInfo, _ *schema.StreamReader[callbacks.CallbackInput],
) context.Context {
	return ctx
}

// finish records the terminal outcome once.
func (o *modelObserver) finish(ctx context.Context, st *callState, status string) {
	o.m.ObserveModelCall(modelLabel(ctx), status, time.Since(st.start))
	if st.span != nil {
		st.span.SetAttributes(st.responseAttributes()...)
		st.span.End()
	}
}

// tokenUsage is the two counters a model span reports. It is this package's
// own type because eino exposes two vocabularies for them: the callback output
// carries its own TokenUsage, and the message it wraps carries schema's.
type tokenUsage struct {
	input  int
	output int
}

// record reads the response facts a span reports. A chunk without usage (every
// chunk but the last, in a stream) leaves what was already recorded alone.
func (st *callState) record(output callbacks.CallbackOutput) {
	converted := model.ConvCallbackOutput(output)
	if converted == nil {
		return
	}
	if converted.TokenUsage != nil {
		st.usage = &tokenUsage{
			input:  converted.TokenUsage.PromptTokens,
			output: converted.TokenUsage.CompletionTokens,
		}
	}
	if converted.Message == nil || converted.Message.ResponseMeta == nil {
		return
	}
	if reason := converted.Message.ResponseMeta.FinishReason; reason != "" {
		st.finishReason = reason
	}
	if schemaUsage := converted.Message.ResponseMeta.Usage; schemaUsage != nil {
		st.usage = &tokenUsage{input: schemaUsage.PromptTokens, output: schemaUsage.CompletionTokens}
	}
}

// responseAttributes are the GenAI attributes only the response can supply.
func (st *callState) responseAttributes() []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if st.usage != nil {
		attrs = append(attrs,
			attribute.Int("gen_ai.usage.input_tokens", st.usage.input),
			attribute.Int("gen_ai.usage.output_tokens", st.usage.output),
		)
	}
	if st.finishReason != "" {
		attrs = append(attrs, attribute.StringSlice("gen_ai.response.finish_reasons", []string{st.finishReason}))
	}
	return attrs
}

// stateFrom returns the call in flight, or nil when this context belongs to a
// component this handler does not observe.
func stateFrom(ctx context.Context) *callState {
	st, _ := ctx.Value(stateKey{}).(*callState)
	return st
}

// streamStatus maps a stream's terminal error to an outcome. io.EOF is how a
// stream ends normally.
func streamStatus(err error) string {
	if errors.Is(err, io.EOF) {
		return statusOK
	}
	return statusError
}

// toolStateKey is the context key for a tool call in flight. It is separate
// from stateKey so the two observers cannot mistake each other's contexts for
// their own: the model handler sees every component's callbacks.
type toolStateKey struct{}

// toolCallState is one tool call in flight.
type toolCallState struct {
	name  string
	start time.Time
	span  trace.Span
	done  bool
}

// toolObserver gives each tool call a span.
//
// It records no metrics on purpose: tool duration and outcome already have one
// writer (pkg/agentbuilder's tracer, which also emits the card's step events),
// and a second counter would double every number an operator reads. What was
// missing was only the trace link between a run's model spans and the tools it
// called, which is what makes a slow tool distinguishable from a slow model in
// a trace rather than only in a dashboard.
type toolObserver struct{}

// Needed reports the timings this handler implements.
func (o *toolObserver) Needed(_ context.Context, _ *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	switch timing {
	case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError:
		return true
	default:
		return false
	}
}

// OnStart begins a span when the call belongs to a tool.
func (o *toolObserver) OnStart(
	ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput,
) context.Context {
	if info == nil || info.Component != components.ComponentOfTool {
		return ctx
	}
	if tool.ConvCallbackInput(input) == nil {
		return ctx
	}
	attrs := telemetry.SpanAttributes(ctx,
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", info.Name),
		// Every tool this service publishes is a function in the GenAI sense:
		// the model calls it by name with JSON arguments. The extension and
		// MCP spellings are what the model sees, not the protocol's category.
		attribute.String("gen_ai.tool.type", "function"),
	)
	ctx, span := tracer.Start(ctx, toolSpanNamePrefix+info.Name, trace.WithAttributes(attrs...))
	return context.WithValue(ctx, toolStateKey{}, &toolCallState{
		name:  info.Name,
		start: time.Now(),
		span:  span,
	})
}

// OnEnd closes a tool call that returned.
func (o *toolObserver) OnEnd(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
	o.finish(ctx, nil)
	return ctx
}

// OnError closes a tool call that failed and records the error on its span.
func (o *toolObserver) OnError(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
	o.finish(ctx, err)
	return ctx
}

// OnEndWithStreamOutput is unused for tools that stream: their span closes on
// OnEnd, which eino still sends. It is required by the interface.
func (o *toolObserver) OnEndWithStreamOutput(
	ctx context.Context, _ *callbacks.RunInfo, _ *schema.StreamReader[callbacks.CallbackOutput],
) context.Context {
	return ctx
}

// OnStartWithStreamInput is unused: tools receive whole arguments.
func (o *toolObserver) OnStartWithStreamInput(
	ctx context.Context, _ *callbacks.RunInfo, _ *schema.StreamReader[callbacks.CallbackInput],
) context.Context {
	return ctx
}

// finish ends the call's span once, whatever the outcome.
func (o *toolObserver) finish(ctx context.Context, err error) {
	st, _ := ctx.Value(toolStateKey{}).(*toolCallState)
	if st == nil || st.done {
		return
	}
	st.done = true
	if st.span == nil {
		return
	}
	if err != nil {
		st.span.RecordError(err)
	}
	st.span.End()
}

// modelLabel is the metric label for a run's model.
//
// It is the endpoint name the run resolved rather than the provider's model id:
// the endpoint is what an operator configured, and the same deployment can serve
// one model id under several endpoints.
func modelLabel(ctx context.Context) string {
	if name := agentapi.ModelNameFromContext(ctx); name != "" {
		return name
	}
	return modelLabelDefault
}
