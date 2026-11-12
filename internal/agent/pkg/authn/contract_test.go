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

package authn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	genericoptions "github.com/onexstack/onexstack/pkg/options"

	"github.com/onexstack/onexai/internal/pkg/authz"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	llmclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// TestTheSigningClientAndThisMiddlewareAgreeOnTheContract closes the loop the
// two unit suites leave open.
//
// Each side is otherwise tested against its own understanding of the algorithm
// — the client's tests recompute the signature here, this package's tests
// recompute it there — which means a shared misreading (a different path, a
// query string included on one side, a body re-encoded on the way out) passes
// both while every real call 401s. So run the actual client against the actual
// middleware.
func TestTheSigningClientAndThisMiddlewareAgreeOnTheContract(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var seenRunID, seenService string
	r := gin.New()
	r.POST("/v1/agent/stop", AKSKAuth(testProvider, false, nil), func(c *gin.Context) {
		var req v1.AgentStopRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			t.Errorf("the server could not read the body the client sent: %v", err)
		}
		seenRunID = req.GetRunID()
		if subj, ok := authz.SubjectFromContext(c.Request.Context()); ok {
			seenService = subj.ServiceName
		}
		c.Status(http.StatusOK)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	opts := genericoptions.NewRestyOptions()
	opts.Endpoint = srv.URL

	client := llmclient.NewWithSigning(opts, testAPIKey, testSecretKey)
	if err := client.Stop(context.Background(), "run-42"); err != nil {
		t.Fatalf("a signed request was refused: %v", err)
	}
	if seenRunID != "run-42" {
		t.Errorf("the server read runID %q, want run-42 — the signed body is not the body delivered", seenRunID)
	}
	if seenService != testAPIKey {
		t.Errorf("the server resolved the caller as %q, want %q", seenService, testAPIKey)
	}
}

// TestTheSigningClientIsRefusedWithTheWrongSecret is the other direction: the
// contract must not verify when the two sides do not share a secret, or the
// check would be decoration.
func TestTheSigningClientIsRefusedWithTheWrongSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.POST("/v1/agent/stop", AKSKAuth(testProvider, false, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
	srv := httptest.NewServer(r)
	defer srv.Close()

	opts := genericoptions.NewRestyOptions()
	opts.Endpoint = srv.URL

	client := llmclient.NewWithSigning(opts, testAPIKey, "the-wrong-secret")
	if err := client.Stop(context.Background(), "run-42"); err == nil {
		t.Fatal("a request signed with the wrong secret was accepted")
	}
}
