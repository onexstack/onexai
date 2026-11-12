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

package gin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/pkg/idempotent"
)

func newIdempotentEngine(t *testing.T, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.POST("/v1/run", Idempotent(idempotent.New(nil, "")), handler)

	return engine
}

func post(engine *gin.Engine, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/run", nil)
	if key != "" {
		req.Header.Set(IdempotencyHeader, key)
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	return rec
}

// A caller that sends no key keeps the behavior it had before the middleware
// existed; requiring one would break every existing client at once.
func TestNoKeyPassesThrough(t *testing.T) {
	calls := 0
	engine := newIdempotentEngine(t, func(c *gin.Context) { calls++; c.Status(http.StatusOK) })

	post(engine, "")
	post(engine, "")

	if calls != 2 {
		t.Errorf("handler ran %d times, want 2", calls)
	}
}

func TestTheSameKeyRunsOnce(t *testing.T) {
	calls := 0
	engine := newIdempotentEngine(t, func(c *gin.Context) { calls++; c.Status(http.StatusOK) })

	if rec := post(engine, "abc"); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}

	rec := post(engine, "abc")
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate request = %d, want 409", rec.Code)
	}
	if calls != 1 {
		t.Errorf("handler ran %d times, want 1", calls)
	}
}

// A transient failure must not lock the key out for the rest of its TTL.
func TestServerErrorReleasesTheKey(t *testing.T) {
	calls := 0
	engine := newIdempotentEngine(t, func(c *gin.Context) {
		calls++
		if calls == 1 {
			c.Status(http.StatusInternalServerError)

			return
		}
		c.Status(http.StatusOK)
	})

	if rec := post(engine, "abc"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first request = %d, want 500", rec.Code)
	}

	if rec := post(engine, "abc"); rec.Code != http.StatusOK {
		t.Errorf("retry after a server error = %d, want 200", rec.Code)
	}
}

// A rejected request is the client's mistake; the same key retried would fail
// the same way, so the claim stands.
func TestClientErrorKeepsTheClaim(t *testing.T) {
	engine := newIdempotentEngine(t, func(c *gin.Context) { c.Status(http.StatusBadRequest) })

	if rec := post(engine, "abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("first request = %d, want 400", rec.Code)
	}

	if rec := post(engine, "abc"); rec.Code != http.StatusConflict {
		t.Errorf("retry after a client error = %d, want 409", rec.Code)
	}
}

// A read is not a write: it must never consume a key.
func TestReadsAreNotClaimed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	calls := 0
	engine := gin.New()
	engine.GET("/v1/run", Idempotent(idempotent.New(nil, "")), func(c *gin.Context) {
		calls++
		c.Status(http.StatusOK)
	})

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/v1/run", nil)
		req.Header.Set(IdempotencyHeader, "abc")
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}

	if calls != 2 {
		t.Errorf("handler ran %d times, want 2", calls)
	}
}

// One key reused across two endpoints describes two different requests; the
// second must not be refused on behalf of the first.
func TestAKeyIsScopedToItsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	engine := gin.New()
	engine.POST("/v1/a", Idempotent(idempotent.New(nil, "")), ok)
	engine.POST("/v1/b", Idempotent(idempotent.New(nil, "")), ok)

	for _, path := range []string{"/v1/a", "/v1/b"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(IdempotencyHeader, "shared")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}
