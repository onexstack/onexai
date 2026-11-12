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

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/options"
)

// identityRecorder is a handler that reports the caller identity it finds in
// the request context, which is where the command tree reads it from.
func identityRecorder(got *runtime.DynamicRuntime) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		*got = *runtime.From(r.Context())
	})
}

// TestAttributeMCPCallerTakesTheHeader is the identity half of the ownership
// contract: job and cronjob tools scope every query by the caller, so the
// header has to arrive in the context the command runs with. Getting this wrong
// does not fail loudly — it silently attributes records to the wrong owner, or
// to nobody.
func TestAttributeMCPCallerTakesTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		header     string
		configured string
		wantUserID string
	}{
		{name: "header wins", header: "ou_abc123", configured: "ou_fallback", wantUserID: "ou_abc123"},
		{name: "falls back to the configured user", configured: "ou_fallback", wantUserID: "ou_fallback"},
		{name: "no identity anywhere", wantUserID: ""},
		{name: "blank header is not an identity", header: "   ", configured: "ou_fallback", wantUserID: "ou_fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTransport(&options.MCPOptions{UserID: tc.configured}, roleRuntime())

			var dr runtime.DynamicRuntime
			handler := tr.attributeMCPCaller(identityRecorder(&dr))

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("x-user-id", tc.header)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if dr.Identity() != tc.wantUserID {
				t.Errorf("Identity() = %q, want %q", dr.Identity(), tc.wantUserID)
			}
			if dr.Source != runtime.RequestSourceMCP {
				t.Errorf("Source = %q, want %q", dr.Source, runtime.RequestSourceMCP)
			}
		})
	}
}

// TestRequireToken covers the optional bearer. An empty token is the documented
// default — this listener is meant to sit on a loopback or cluster-internal
// address, and a deployment adds a token when it does not.
func TestRequireToken(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range []struct {
		name       string
		configured string
		auth       string
		wantStatus int
		wantPass   bool
	}{
		{name: "no token configured is open", auth: "", wantStatus: http.StatusOK, wantPass: true},
		{name: "correct token passes", configured: "s3cret", auth: "Bearer s3cret", wantStatus: http.StatusOK, wantPass: true},
		{name: "wrong token is rejected", configured: "s3cret", auth: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "missing token is rejected", configured: "s3cret", wantStatus: http.StatusUnauthorized},
		{name: "token without the scheme is rejected", configured: "s3cret", auth: "s3cret", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			tr := newTransport(&options.MCPOptions{Token: tc.configured}, roleRuntime())

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			tr.requireToken(next).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if reached != tc.wantPass {
				t.Errorf("handler reached = %v, want %v", reached, tc.wantPass)
			}
		})
	}
}

// TestAttributeMCPCallerKeepsRequestValues checks that the identity middleware
// layers onto the request context rather than replacing it, so whatever the
// server put there (a deadline, a trace, the request id) survives into the
// command.
func TestAttributeMCPCallerKeepsRequestValues(t *testing.T) {
	type key struct{}
	tr := newTransport(&options.MCPOptions{}, roleRuntime())

	var seen any
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Context().Value(key{})
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), key{}, "kept"))
	tr.attributeMCPCaller(next).ServeHTTP(httptest.NewRecorder(), req)

	if seen != "kept" {
		t.Errorf("an existing context value was dropped: got %v", seen)
	}
}

// roleRuntime is a static runtime that lists ou_sre as both SRE and admin, which
// is the identity these tests send. The gate itself is decided by the configured
// lists (see StaticRuntime.IsSREUser), so a test about the transport configures
// the list it needs rather than restating the policy; an empty list would now
// grant nothing and make every x-sre-only tool unreachable.
func roleRuntime() *runtime.StaticRuntime {
	return &runtime.StaticRuntime{
		Roles: roleset.NewStatic(map[string]struct{}{"ou_sre": {}}, nil),
	}
}

// TestAttributeMCPCallerResolvesRoles is the role half of the same contract the
// identity test covers, and it exists because its absence was silent in the
// direction that matters least visibly: an unset role is read by ValidateCommands
// as "not that role", so every x-sre-only tool was refused over MCP — the model
// was told "requires SRE role" and reported exactly that to the user, with no
// build ever triggered.
//
// Both directions are pinned. A caller the configured list names resolves to
// both roles, and a runtime that cannot answer grants nothing: the MCP listener
// has no claims of its own to fall back on, and a default of "allowed" is how a
// gate stops being one.
func TestAttributeMCPCallerResolvesRoles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sr          *runtime.StaticRuntime
		wantSRE     bool
		wantIsAdmin bool
	}{
		{name: "the deployment decides", sr: roleRuntime(), wantSRE: true, wantIsAdmin: true},
		{name: "no runtime grants nothing", sr: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTransport(&options.MCPOptions{}, tc.sr)

			var dr runtime.DynamicRuntime
			handler := tr.attributeMCPCaller(identityRecorder(&dr))

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("x-user-id", "ou_sre")
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if dr.IsSRE != tc.wantSRE {
				t.Errorf("IsSRE = %v, want %v", dr.IsSRE, tc.wantSRE)
			}
			if dr.IsAdmin != tc.wantIsAdmin {
				t.Errorf("IsAdmin = %v, want %v", dr.IsAdmin, tc.wantIsAdmin)
			}
		})
	}
}
