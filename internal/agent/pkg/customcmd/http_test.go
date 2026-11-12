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

package customcmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestHTTPExecutorRendersAndPosts(t *testing.T) {
	var gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Service")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true, AllowPlainHTTP: true})
	spec := &model.HTTPCommandM{
		Method: "POST",
		URL:    srv.URL + "/deploy/{{ .Params.service }}",
		Headers: []model.HTTPHeaderM{
			{Name: "X-Service", Value: "{{ .Params.service }}"},
		},
		Body: `{"service":"{{ .Params.service }}"}`,
	}
	result, err := executor.Execute(context.Background(), spec, nil, nil,
		TemplateData{Params: map[string]any{"service": "api"}}, time.Second, "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", result.Status)
	}
	if gotHeader != "api" {
		t.Errorf("header = %q, want api", gotHeader)
	}
	if gotBody != `{"service":"api"}` {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(result.Rendered, `"ok": true`) {
		t.Errorf("rendered = %q, want pretty JSON", result.Rendered)
	}
}

func TestHTTPExecutorRejectsAHostOutsideTheAllowlist(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowedHosts: []string{"api.example.com"}})
	spec := &model.HTTPCommandM{URL: "https://evil.example.net/x"}
	_, err := executor.Execute(context.Background(), spec, nil, nil, TemplateData{}, time.Second, "")
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("error = %T %v, want *CallError", err, err)
	}
	if ce.Evaluation {
		t.Errorf("a policy refusal should not be an evaluation error")
	}
	if !strings.Contains(ce.Message, "allowed-hosts") {
		t.Errorf("message = %q, want it to name the allowlist", ce.Message)
	}
}

func TestHTTPServiceTargetRendersAnInClusterName(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true})
	raw, inCluster, err := executor.buildURL(&model.HTTPCommandM{
		Service: &model.ServiceReferenceM{Name: "api", Namespace: "ops", Port: 8080},
	}, TemplateData{})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if !inCluster {
		t.Error("a service target must be marked in-cluster")
	}
	if want := "http://api.ops.svc:8080"; raw != want {
		t.Errorf("url = %q, want %q", raw, want)
	}
}

// An address is not a Service name. Without this refusal the cluster fetcher,
// which deliberately skips the private-address policy, would reach a link-local
// address such as the metadata endpoint.
func TestHTTPServiceTargetRejectsAnAddress(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true})
	_, _, err := executor.buildURL(&model.HTTPCommandM{
		Service: &model.ServiceReferenceM{Name: "169.254.169.254", Port: 80},
	}, TemplateData{})
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("error = %T %v, want *CallError", err, err)
	}
	if ce.Evaluation {
		t.Errorf("a policy refusal should not be an evaluation error")
	}
	if !strings.Contains(ce.Message, "invalid service reference") {
		t.Errorf("message = %q, want it to name the service reference", ce.Message)
	}
}

func TestCheckTargetRefusesAnInClusterHostOutsideTheServiceSuffix(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true})
	if err := executor.checkTarget("http://169.254.169.254/latest/meta-data", true); err == nil {
		t.Fatal("an in-cluster target outside .svc was accepted")
	}
}

func TestHTTPExecutorMapsAnUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true, AllowPlainHTTP: true})
	spec := &model.HTTPCommandM{URL: srv.URL}
	result, err := executor.Execute(context.Background(), spec, nil, nil, TemplateData{}, time.Second, "")
	if result == nil || result.Status != http.StatusInternalServerError {
		t.Fatalf("result = %+v, want status 500", result)
	}
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("error = %T %v, want *CallError", err, err)
	}
	if ce.Evaluation || ce.StatusCode != http.StatusInternalServerError {
		t.Errorf("error = %+v, want a non-evaluation 500", ce)
	}
}

func TestHTTPExecutorRefusesAPrivateTarget(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowPlainHTTP: true})
	spec := &model.HTTPCommandM{URL: "http://127.0.0.1:9/x"}
	_, err := executor.Execute(context.Background(), spec, nil, nil, TemplateData{}, time.Second, "")
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("error = %T %v, want *CallError", err, err)
	}
	if !ce.Evaluation {
		t.Errorf("a blocked destination should be an evaluation error")
	}
}

func TestRenderTemplateTreatsAMissingKeyAsAnError(t *testing.T) {
	if _, err := RenderTemplate("x", "{{ .Params.nope }}", TemplateData{Params: map[string]any{}}); err == nil {
		t.Fatalf("a missing key rendered silently")
	}
}

// A command with an explicit TLS configuration must reuse one client: building
// one per call means a new connection pool and a reparsed bundle every time.
func TestClientForCachesTLSClients(t *testing.T) {
	executor := NewHTTPExecutor(HTTPConfig{AllowPrivateHosts: true})
	spec := &model.HTTPCommandM{InsecureSkipTLSVerify: true}

	first, err := executor.clientFor(spec, true)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	second, err := executor.clientFor(spec, true)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if first != second {
		t.Error("the same TLS configuration built two clients")
	}

	// The two fetchers dial under different policies, so a client built for one
	// must never be handed to the other.
	external, err := executor.clientFor(spec, false)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if external == first {
		t.Error("a client built for the cluster fetcher served the public one")
	}
}

// A limit that lands inside a multi-byte rune must back off to the boundary: an
// invalid UTF-8 tail can make a chat card reject the whole message.
func TestTruncateKeepsARuneBoundary(t *testing.T) {
	got := truncate("aaaaé", 5)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("truncate() = %q", got)
	}
}
