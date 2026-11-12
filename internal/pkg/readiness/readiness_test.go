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

package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNoChecksIsReady(t *testing.T) {
	ready, results := New().Ready(context.Background())
	if !ready {
		t.Error("a registry with no checks must report ready")
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %v", results)
	}
}

func TestAnUnwiredRegistryIsReady(t *testing.T) {
	var registry *Registry
	if ready, _ := registry.Ready(context.Background()); !ready {
		t.Error("a nil registry must report ready rather than panic")
	}

	// Handler on a nil registry must not dereference it either.
	rec := httptest.NewRecorder()
	registry.Handler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAFailingCheckMakesTheInstanceNotReady(t *testing.T) {
	registry := New(
		Check{Name: "ok", Probe: func(context.Context) error { return nil }},
		Check{Name: "down", Probe: func(context.Context) error { return errors.New("connection refused") }},
	)

	ready, results := registry.Ready(context.Background())
	if ready {
		t.Fatal("one failing check must make the aggregate not ready")
	}
	if len(results) != 2 {
		t.Fatalf("expected both checks to run, got %v", results)
	}
	if results[0].Name != "ok" || !results[0].Ready {
		t.Errorf("unexpected first result %+v", results[0])
	}
	if results[1].Ready || results[1].Detail != "error" {
		t.Errorf("unexpected second result %+v", results[1])
	}
}

// /readyz is unauthenticated, and a driver error routinely contains the host and
// port it could not reach. The response must classify, not quote.
func TestAFailureDetailDoesNotLeakTheDependencyError(t *testing.T) {
	const secret = "dial tcp 10.0.0.7:5432: connect: connection refused"
	registry := New(Check{
		Name:  "database",
		Probe: func(context.Context) error { return errors.New(secret) },
	})

	rec := httptest.NewRecorder()
	registry.Handler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "5432") {
		t.Errorf("the probe response must not repeat the dependency's address: %s", rec.Body.String())
	}
}

func TestASlowCheckIsReportedAsATimeout(t *testing.T) {
	registry := New(Check{
		Name: "slow",
		Probe: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	registry.timeout = 10 * time.Millisecond

	ready, results := registry.Ready(context.Background())
	if ready {
		t.Fatal("a check that never answers must make the instance not ready")
	}
	if results[0].Detail != "timeout" {
		t.Errorf("expected a timeout detail, got %q", results[0].Detail)
	}
}

func TestTheHandlerReportsStatusAndBreakdown(t *testing.T) {
	healthy := New(Check{Name: "database", Probe: func(context.Context) error { return nil }})
	rec := httptest.NewRecorder()
	healthy.Handler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		Ready  bool     `json:"ready"`
		Checks []Result `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Ready || len(body.Checks) != 1 || body.Checks[0].Name != "database" {
		t.Errorf("unexpected body %s", rec.Body.String())
	}

	broken := New(Check{Name: "database", Probe: func(context.Context) error { return errors.New("x") }})
	rec = httptest.NewRecorder()
	broken.Handler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when a check fails, got %d", rec.Code)
	}
}
