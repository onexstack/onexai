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

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestServeLarkWebhookRejectsOversizedBody pins the pre-authentication limit:
// the public webhook must refuse a body larger than the configured bound before
// it reads it into memory, not after verifying it.
func TestServeLarkWebhookRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{}
	router := gin.New()
	router.POST("/lark/event", h.ServeLarkWebhook)

	body := strings.Repeat("x", maxWebhookBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/lark/event", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}
