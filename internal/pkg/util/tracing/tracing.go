// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

// Package tracing wires OpenTelemetry tracing into controller processes.
package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
)

// Setup initializes the global OpenTelemetry tracer provider, exporting spans to
// the given OTLP endpoint. It returns an http.RoundTripper wrapper that
// instruments client-go requests with spans. When endpoint is empty, tracing is
// disabled and the returned wrapper is nil.
func Setup(ctx context.Context, serviceName, endpoint string) (func(http.RoundTripper) http.RoundTripper, error) {
	if endpoint == "" {
		return nil, nil
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)))
	if err != nil {
		return nil, err
	}

	tp := tracesdk.NewTracerProvider(
		tracesdk.WithSampler(tracesdk.ParentBased(tracesdk.TraceIDRatioBased(1.0))),
		tracesdk.WithSpanProcessor(tracesdk.NewBatchSpanProcessor(exporter)),
		tracesdk.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return func(rt http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(rt)
	}, nil
}
