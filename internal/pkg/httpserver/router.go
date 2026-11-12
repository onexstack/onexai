// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package httpserver builds the gin engine every onexai HTTP service serves:
// one middleware chain, one set of infrastructure routes.
//
// The chain used to be copied into each service, and copies drift. chat was
// already missing CaptureConnWriter, which is invisible until a response
// outlives the server's write deadline — at which point a stream is severed
// mid-flight and the symptom looks like a handler bug. A drift in middleware
// order is the same class of problem: nothing fails until something does.
package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	genericmw "github.com/onexstack/onexstack/pkg/middleware/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/onexstack/onexai/internal/pkg/errno"
	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// probePaths are never wrapped by the request-observability middleware: a probe
// answering every few seconds would otherwise fill the request log with noise
// that says nothing about traffic.
var probePaths = []string{"/healthz", "/readyz"}

// Options configures the engine.
type Options struct {
	// ServiceName names the OpenTelemetry service and appears on every span.
	ServiceName string
	// FeatureOptions gates the observability-skip behavior. Nil means the
	// switch is off.
	FeatureOptions *customoptions.FeatureOptions
	// ObservabilitySkips lists this service's own long-lived or sensitive
	// routes. They are only honored when the switch is on; the probe paths
	// above are always skipped.
	ObservabilitySkips []string
}

// NewRouter builds an engine with the standard middleware chain and the
// infrastructure routes (/metrics, /debug/pprof, JSON 404).
//
// # Why the order is what it is
//
// Recovery first, so a panic in any later middleware is a 500 rather than a
// dropped connection. Security headers before anything can write a body.
// Tracing before the body-capturing observability writer, so the span sees the
// request rather than a wrapper. CaptureConnWriter before Observability,
// because that writer cannot be unwrapped back to the connection, and a
// streaming handler that cannot clear the request's write deadline dies at the
// server timeout. Request context last, so it sees the finished request.
func NewRouter(opts Options) *gin.Engine {
	engine := gin.New()

	engine.Use(
		recovery(),
		mw.NoCache,
		mw.Cors(mw.DefaultCorsConfig()),
		mw.Secure,
		otelgin.Middleware(opts.ServiceName, otelgin.WithFilter(recordTelemetry)),
		mw.CaptureConnWriter(),
		observability(opts),
		mw.Context(),
	)

	installInfrastructureRoutes(engine)

	return engine
}

// recovery turns a panic into the same JSON error every other failure returns.
//
// gin.Recovery writes a bare 500 with no body, which is the one response a
// client cannot parse: the console shows "请求失败（HTTP 500）" and an operator
// has only the log line to go on. The panic value and stack stay in the log;
// the caller gets errno.ErrInternal through the shared response writer.
func recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(c.Request.Context(), "panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
					"path", c.Request.URL.Path,
				)
				c.Abort()
				core.WriteResponse(c, nil, errno.ErrInternal)
			}
		}()

		c.Next()
	}
}

// observability builds the request-observability middleware.
//
// With the switch off it is the plain middleware. With it on, the streaming and
// long-lived routes are excluded and body logging is turned off for the rest:
// the bodies onexai handles are user prompts, tool payloads and platform
// webhook envelopes, none of which belong in a log line.
func observability(opts Options) gin.HandlerFunc {
	if opts.FeatureOptions == nil || !opts.FeatureOptions.ObservabilitySkip {
		return genericmw.Observability()
	}

	skips := make([]string, 0, len(probePaths)+len(opts.ObservabilitySkips))
	skips = append(skips, probePaths...)
	skips = append(skips, opts.ObservabilitySkips...)

	return genericmw.Observability(
		genericmw.WithSkipPaths(skips...),
		genericmw.WithDisableBodyLog(),
	)
}

// recordTelemetry filters out paths that should not generate traces.
func recordTelemetry(r *http.Request) bool {
	return r.URL.Path != "/metrics"
}

// installInfrastructureRoutes registers the routes every service has: the
// Prometheus scrape, the profiling endpoints, and a JSON 404 so a mistyped path
// answers like any other API error.
func installInfrastructureRoutes(engine *gin.Engine) {
	pprof.Register(engine)

	engine.GET("/metrics", gin.WrapH(promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{ //nolint:exhaustruct_v5 // only the opt-in switches are set
			EnableOpenMetrics: true,
		},
	)))

	// WriteResponse(c, data, err) writes the error only when it arrives as err.
	// Both services used to pass it as data, which serialized the error struct
	// with a 200 status: an unknown route answered success.
	engine.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, nil, errno.ErrPageNotFound)
	})
}
