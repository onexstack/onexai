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

// Package telemetry carries the run's identity onto every span it emits.
//
// A trace is read by asking "which run is this", and answering that from the
// root span alone means opening every trace to find out. The attributes here are
// attached to each span at start, so a filter on run id, session or tenant works
// on any span of the trace, including the ones a tool or a remote runtime
// produced.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
)

// AttributeKeys are the request-identity attributes this service sets.
const (
	RunIDKey     = attribute.Key("onexai.run.id")
	SessionKey   = attribute.Key("onexai.session.key")
	AgentKey     = attribute.Key("gen_ai.agent.name")
	TenantKey    = attribute.Key("onexai.tenant")
	OperationKey = attribute.Key("gen_ai.operation.name")
)

type attributesKey struct{}

// WithRequestAttributes attaches the run's identity to ctx.
func WithRequestAttributes(ctx context.Context, attrs ...attribute.KeyValue) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	existing, _ := ctx.Value(attributesKey{}).([]attribute.KeyValue)
	merged := make([]attribute.KeyValue, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, attributesKey{}, merged)
}

// RequestAttributes returns the identity attached to ctx, if any.
func RequestAttributes(ctx context.Context) []attribute.KeyValue {
	attrs, _ := ctx.Value(attributesKey{}).([]attribute.KeyValue)
	return attrs
}

// SpanAttributes is the start-time option every span in a run uses.
func SpanAttributes(ctx context.Context, extra ...attribute.KeyValue) []attribute.KeyValue {
	base := RequestAttributes(ctx)
	if len(extra) == 0 {
		return base
	}
	out := make([]attribute.KeyValue, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// maxBaggageBytes bounds the propagated baggage. W3C baggage travels on every
// outbound request; an unbounded map is a header a proxy will reject, and the
// rejection looks like a broken upstream rather than a large config.
const maxBaggageBytes = 8 << 10

// WithBaggage replaces the given baggage members, dropping empty values.
//
// Replacing rather than adding is what makes it safe to call on a context that
// already carries a caller's baggage: this service states its own keys and
// leaves the rest alone.
func WithBaggage(ctx context.Context, members map[string]string) context.Context {
	bag := baggage.FromContext(ctx)
	for key, value := range members {
		if value == "" {
			continue
		}
		member, err := baggage.NewMember(key, value)
		if err != nil {
			// A key that is not a valid baggage token is dropped rather than
			// failing the run: it is observability, not correctness.
			continue
		}
		updated, err := bag.SetMember(member)
		if err != nil {
			continue
		}
		if len(updated.String()) > maxBaggageBytes {
			break
		}
		bag = updated
	}
	return baggage.ContextWithBaggage(ctx, bag)
}
