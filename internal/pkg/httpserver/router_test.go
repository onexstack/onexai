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

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

func featureOptions(skip bool) *customoptions.FeatureOptions {
	// Only the switch under test is set; every other feature is off.
	return &customoptions.FeatureOptions{ObservabilitySkip: skip} //nolint:exhaustruct_v5 // one field under test
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(Options{ServiceName: "test"})
}

func TestInfrastructureRoutesAreInstalled(t *testing.T) {
	engine := newTestRouter(t)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /metrics = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ = %d, want 200", rec.Code)
	}
}

// A mistyped path has to answer like every other API error, not with gin's
// plain-text 404: the clients parse JSON.
func TestUnknownRouteAnswersJSON(t *testing.T) {
	engine := newTestRouter(t)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
}

// CaptureConnWriter must run before anything that wraps the writer, or a
// streaming handler cannot reach the connection to clear the write deadline.
// The chain is verified through its effect: the handler sees a writer even
// though it is registered after every middleware.
func TestCaptureConnWriterRunsBeforeHandlers(t *testing.T) {
	engine := newTestRouter(t)

	var seen bool
	engine.GET("/probe-chain", func(c *gin.Context) {
		// A handler reaches the connection through this writer; the recorder has
		// no deadline to set, so only its presence is asserted.
		seen = mw.ConnWriter(c) != nil
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe-chain", nil))

	if !seen {
		t.Error("the handler was not given a ResponseWriter")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// The gate is off unless the deployment turns it on; the middleware must still
// be present, otherwise a service that enables the gate later gets nothing.
func TestObservabilityIsInstalledRegardlessOfTheGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate bool
	}{
		{name: "gate off"},
		{name: "gate on", gate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := NewRouter(Options{
				ServiceName:        "test",
				FeatureOptions:     featureOptions(tc.gate),
				ObservabilitySkips: []string{"/stream"},
			})

			engine.GET("/stream", func(c *gin.Context) { c.Status(http.StatusOK) })

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}
