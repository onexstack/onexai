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

package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/pkg/readiness"
)

// TestInstallRESTAPIMountsTheAgentCardAtTheOriginRoot pins the published
// location: the card is public at the protocol's origin-root well-known path,
// and the former nested path is gone rather than aliased.
func TestInstallRESTAPIMountsTheAgentCardAtTheOriginRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &ServerConfig{
		Config: &Config{},
		Handler: handler.NewHandler(handler.Deps{
			A2A: &handler.A2AHandlers{
				JSONRPC: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				REST:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				Card: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			},
		}),
		Readiness: readiness.New(),
	}

	engine := gin.New()
	cfg.InstallRESTAPI(engine)

	call := func(method, path string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call(http.MethodGet, a2a.PathCard),
		"the card must be public at the origin-root well-known path")
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/chat/a2a/.well-known/agent-card.json"),
		"the old nested path must be gone, with no alias or redirect")
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1"+a2a.PathCard),
		"the card must not be reachable under /v1")
}
