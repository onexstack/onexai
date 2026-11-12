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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	genericmw "github.com/onexstack/onexstack/pkg/middleware/gin"

	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
)

// TestDetachConnDeadlinesOutlivesTheServerWriteTimeout is the regression test for
// the severed agent stream.
//
// A server's WriteTimeout is one absolute deadline for the whole request, and
// net/http closes the connection outright at the first write past it. A run is
// bounded by agent.run-timeout instead, which is minutes — so any run longer than
// the server timeout had its SSE response cut mid-stream, and the chat read
// a truncated body: io.ErrUnexpectedEOF, reported to the user as a red card
// reading "unexpected EOF".
//
// This runs against a real net/http connection on purpose, and it runs the whole
// writer chain, not just gin's. The fix depends on http.ResponseController
// reaching the connection through that chain; a writer it cannot unwrap returns
// ErrNotSupported, the handler logs that and carries on, and the bug stays in
// place while a bare-engine test still passes. That is exactly how the first
// attempt at this fix shipped inert, so both assertions matter: the controller
// has to reach the socket, not merely be called.
func TestDetachConnDeadlinesOutlivesTheServerWriteTimeout(t *testing.T) {
	const (
		writeTimeout = 200 * time.Millisecond
		writes       = 6
		interval     = 80 * time.Millisecond
	)

	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		// chain installs the middleware the handler runs behind.
		chain func(*gin.Engine)
		// wantWrapped pins whether that chain hides the connection behind a
		// writer of its own. A variant that stops wrapping would silently
		// become a duplicate of the bare one and protect nothing.
		wantWrapped bool
	}{
		{
			name:  "bare engine",
			chain: func(*gin.Engine) {},
		},
		{
			// The chain the live server runs: at debug level Observability
			// replaces c.Writer with a body-capturing writer that has no
			// Unwrap, so a handler reaching for the connection through c.Writer
			// gets ErrNotSupported and the deadline survives.
			name: "behind the body-capturing observability middleware",
			chain: func(e *gin.Engine) {
				debug := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
				e.Use(mw.CaptureConnWriter(), genericmw.Observability(genericmw.WithLogger(debug)))
			},
			wantWrapped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := gin.New()
			tt.chain(engine)

			// Buffered, so a handler that runs to completion never blocks on it.
			deadlineErr := make(chan error, 1)
			wrapped := make(chan bool, 1)
			engine.GET("/sse", func(c *gin.Context) {
				c.Writer.Header().Set("Content-Type", "text/event-stream")
				wrapped <- mw.ConnWriter(c) != http.ResponseWriter(c.Writer)

				// The expression the streaming handlers use.
				deadlineErr <- detachConnDeadlines(mw.ConnWriter(c))
				for i := range writes {
					fmt.Fprintf(c.Writer, "data: %d\n\n", i)
					c.Writer.Flush()
					time.Sleep(interval)
				}
			})

			srv := httptest.NewUnstartedServer(engine)
			srv.Config.WriteTimeout = writeTimeout
			srv.Start()
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/sse")
			if err != nil {
				t.Fatalf("GET /sse: %v", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading the stream: %v (received %q)", err, body)
			}
			if got := strings.Count(string(body), "data: "); got != writes {
				t.Fatalf("got %d of %d events before the connection was cut: %q", got, writes, body)
			}
			if got := <-wrapped; got != tt.wantWrapped {
				t.Fatalf("this chain hid the connection = %v, want %v", got, tt.wantWrapped)
			}
			if err := <-deadlineErr; err != nil {
				t.Fatalf("detachConnDeadlines could not reach the connection, so the fix is inert: %v", err)
			}
		})
	}
}
