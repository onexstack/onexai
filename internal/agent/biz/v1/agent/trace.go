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

package agent

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/onexstack/onexai/internal/agent/pkg/telemetry"
)

const tracerName = "onexai/agent"

// startRunSpan opens the run's root span. The GenAI semantic-convention names
// are used where they exist so an existing dashboard can read the trace without
// a service-specific mapping.
func (b *AgentBiz) startRunSpan(
	ctx context.Context, runID, sessionID, subject string, agents []string,
) context.Context {
	if !b.deps.EnableTracing {
		return ctx
	}
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("enduser.id", subject),
		telemetry.RunIDKey.String(runID),
		telemetry.SessionKey.String(sessionID),
	}
	if len(agents) > 0 && agents[0] != "" {
		attrs = append(attrs, telemetry.AgentKey.String(agents[0]))
	}
	// The identity goes on the context as well as on this span: every span the
	// run produces — model calls, tool calls, capability calls — reads it from
	// here, so one filter finds the whole trace.
	ctx = telemetry.WithRequestAttributes(ctx, attrs...)
	ctx, _ = otel.Tracer(tracerName).Start(ctx, "invoke_agent", trace.WithAttributes(attrs...))
	return ctx
}

// endRunSpan ends the run's span. Ending twice is a no-op, so both the request
// guard and the relay can call it without coordinating ownership.
func endRunSpan(ctx context.Context) {
	trace.SpanFromContext(ctx).End()
}

// injectTrace writes the W3C traceparent into an outbound A2A request, so a
// remote runtime's spans join this run rather than starting a second trace.
func injectTrace(ctx context.Context, send *a2a.SendMessageRequest) {
	if send == nil {
		return
	}
	if send.Metadata == nil {
		send.Metadata = make(map[string]any)
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if traceparent := carrier.Get("traceparent"); traceparent != "" {
		send.Metadata["traceparent"] = traceparent
	}
	if tracestate := carrier.Get("tracestate"); tracestate != "" {
		send.Metadata["tracestate"] = tracestate
	}
}
