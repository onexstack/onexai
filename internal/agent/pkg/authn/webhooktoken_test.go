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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func webhookEngine(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(WebhookBearer(token))
	engine.POST("/review", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return engine
}

func TestWebhookBearerRejectsWhenUnconfigured(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/review", nil)
	req.Header.Set("Authorization", "Bearer anything")
	webhookEngine("").ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503: an unconfigured endpoint must not be open", w.Code)
	}
}

func TestWebhookBearerRejectsMissingOrWrongCredential(t *testing.T) {
	for _, header := range []string{"", "Bearer wrong", "Basic dXNlcjpwYXNz", "Bearer "} {
		t.Run(header, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/review", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			webhookEngine("s3cret").ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", w.Code)
			}
		})
	}
}

func TestWebhookBearerAcceptsTheConfiguredToken(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/review", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	webhookEngine("s3cret").ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", w.Code)
	}
}
