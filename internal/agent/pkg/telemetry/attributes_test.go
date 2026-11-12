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

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
)

// TestRequestAttributesAccumulate pins that a nested call (a tool that starts a
// capability call) inherits the run's identity and can add its own.
func TestRequestAttributesAccumulate(t *testing.T) {
	ctx := WithRequestAttributes(context.Background(),
		RunIDKey.String("run-1"), SessionKey.String("s-1"))
	ctx = WithRequestAttributes(ctx, AgentKey.String("planner"))

	attrs := RequestAttributes(ctx)
	require.Len(t, attrs, 3)
	assert.Equal(t, "run-1", attrs[0].Value.AsString())
	assert.Equal(t, "planner", attrs[2].Value.AsString())
}

func TestSpanAttributesLeadWithTheRunIdentity(t *testing.T) {
	ctx := WithRequestAttributes(context.Background(), RunIDKey.String("run-1"))

	attrs := SpanAttributes(ctx, attribute.String("gen_ai.tool.name", "search"))
	require.Len(t, attrs, 2)
	assert.Equal(t, attribute.Key("onexai.run.id"), attrs[0].Key)
	assert.Equal(t, attribute.Key("gen_ai.tool.name"), attrs[1].Key)
}

func TestNoContextMeansNoAttributes(t *testing.T) {
	assert.Empty(t, RequestAttributes(context.Background()))
	assert.Empty(t, SpanAttributes(context.Background()))
}

// TestBaggageDropsEmptyValues pins that an unset attribute does not become an
// empty baggage member that every downstream request then carries.
func TestBaggageDropsEmptyValues(t *testing.T) {
	ctx := WithBaggage(context.Background(), map[string]string{
		"run_id":       "run-1",
		"missing":      "",
		"not a token!": "x",
	})

	bag := baggage.FromContext(ctx)
	assert.Equal(t, "run-1", bag.Member("run_id").Value())
	assert.Empty(t, bag.Member("missing").Value())
	assert.Len(t, bag.Members(), 1, "an invalid key is dropped rather than failing the run")
}
