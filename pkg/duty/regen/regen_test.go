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

package regen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onexstack/onexai/pkg/duty"
)

// fakeServer is a Regen stand-in: it hands out a session cookie on login and
// refuses every other call without the current one, which is the behaviour the
// client's re-login path exists for.
type fakeServer struct {
	mu sync.Mutex
	// logins counts the login exchanges, to prove a 401 triggers exactly one.
	logins int
	// session is the value the server currently accepts; a login bumps it,
	// which invalidates every cookie already handed out — a revoked session.
	session int
	// revokeAfter makes the server revoke the session once this many calls have
	// been served; zero means never. It is how a test forces a 401.
	revokeAfter int
	served      int

	handlers map[string]http.HandlerFunc
}

// cookieValue is the session cookie the server accepts right now.
func (f *fakeServer) cookieValue() string {
	return "session-" + strconv.Itoa(f.session)
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["email"] == "" || body["password"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		f.logins++
		f.session++
		value := f.cookieValue()
		f.mu.Unlock()

		http.SetCookie(w, &http.Cookie{Name: "oi_session", Value: value, Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	for path, h := range f.handlers {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.served++
			cookie, err := r.Cookie("oi_session")
			valid := err == nil && cookie.Value == f.cookieValue()
			// Revoke only after the call has been validated: this call is
			// answered, the next one is the one that gets the 401.
			if valid && f.revokeAfter > 0 && f.served >= f.revokeAfter {
				f.session++
			}
			f.mu.Unlock()

			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized"}}`))
				return
			}
			h(w, r)
		})
	}
	return mux
}

func newTestClient(t *testing.T, f *fakeServer) (*httptest.Server, duty.Provider) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL, "harness@example.com", "secret")
}

func TestClientLoginIsLazyAndShared(t *testing.T) {
	f := &fakeServer{handlers: map[string]http.HandlerFunc{
		"/api/v1/schedules": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"onex","timezone":"Asia/Shanghai"}],"total":1}`))
		},
	}}
	_, cli := newTestClient(t, f)

	if f.logins != 0 {
		t.Fatalf("client logged in before the first call")
	}

	schedules, err := cli.Schedules(context.Background())
	if err != nil {
		t.Fatalf("ListSchedules returned error: %v", err)
	}
	if len(schedules) != 1 || schedules[0].Name != "onex" {
		t.Fatalf("ListSchedules = %+v, want the onex schedule", schedules)
	}

	// The session is reused: a second call must not log in again.
	if _, err := cli.OnCall(context.Background(), "s1"); err == nil {
		t.Fatal("GetOnCall to an unrouted path returned no error")
	}
	if f.logins != 1 {
		t.Errorf("login count = %d, want 1 (the session should be reused)", f.logins)
	}
}

func TestClientReloginsOnceOnUnauthorized(t *testing.T) {
	f := &fakeServer{
		// The first authenticated call succeeds, then the session is revoked.
		revokeAfter: 1,
		handlers: map[string]http.HandlerFunc{
			"/api/v1/schedules": func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"data":[],"total":0}`))
			},
		},
	}
	_, cli := newTestClient(t, f)

	if _, err := cli.Schedules(context.Background()); err != nil {
		t.Fatalf("first ListSchedules returned error: %v", err)
	}
	if _, err := cli.Schedules(context.Background()); err != nil {
		t.Fatalf("ListSchedules after session expiry returned error: %v", err)
	}
	if f.logins != 2 {
		t.Errorf("login count = %d, want 2 (one initial, one after the 401)", f.logins)
	}
}

// dropConn answers a request by closing the connection instead of replying. It
// is what a client sees as a transport error: no status, no body, nothing in
// the server's access log.
func dropConn(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("test server does not support hijacking")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	_ = conn.Close()
}

// A dropped connection on the way out is the failure the duty refresh hits: the
// request never reaches the server, so no status comes back to react to, and
// the refresh fails.
//
// The login answers with Connection: close so the next request is sent on a
// fresh connection. That matters: Go's transport silently re-sends a request
// whose *reused* connection died before a response, so a dropped connection on
// a pooled one would be retried by the transport and this test would pass
// without the client's own retry ever running. On a fresh connection it is the
// client's retry or nothing.
func TestClientRetriesADroppedRead(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "oi_session", Value: "session-1", Path: "/"})
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusOK)
		case "/api/v1/schedules":
			attempts++
			if attempts == 1 {
				dropConn(t, w)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"onex"}],"total":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	cli := NewClient(srv.URL, "harness@example.com", "secret")
	start := time.Now()
	schedules, err := cli.Schedules(context.Background())
	if err != nil {
		t.Fatalf("ListSchedules after one dropped connection = %v, want the retry to answer", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (the first dropped, the second answered)", attempts)
	}
	if len(schedules) != 1 || schedules[0].Name != "onex" {
		t.Fatalf("ListSchedules = %+v, want the onex schedule", schedules)
	}
	// The retry waits between attempts rather than hammering the path that just
	// failed; without the wait the transport would be the one that answered.
	if elapsed := time.Since(start); elapsed < transportRetryDelay {
		t.Errorf("ListSchedules took %v, want at least the %v backoff", elapsed, transportRetryDelay)
	}
}

// A request with side effects is never replayed: the first attempt may have
// reached the server, and an incident created twice is worse than an error the
// caller can retry itself.
func TestClientDoesNotReplayASideEffectingRequest(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "oi_session", Value: "session-1", Path: "/"})
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			dropConn(t, w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	cli := NewClient(srv.URL, "harness@example.com", "secret")
	if _, err := cli.CreateIncident(context.Background(), duty.CreateIncidentRequest{
		Title:    "DB is down",
		Severity: duty.SeverityCritical,
	}); err == nil {
		t.Fatal("CreateIncident over a dropped connection returned no error")
	}
	if got := posts.Load(); got != 1 {
		t.Errorf("POST attempts = %d, want 1", got)
	}
}

func TestClientIncidents(t *testing.T) {
	f := &fakeServer{handlers: map[string]http.HandlerFunc{
		"/api/v1/incidents": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var req wireCreateIncident
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &req)
			if req.Title != "DB is down" || req.Severity != string(duty.SeverityCritical) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"inc-1","incident_number":1,"title":"DB is down","status":"triggered","severity":"critical"}`))
		},
		"/api/v1/incidents/inc-1": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`{"id":"inc-1","status":"resolved","severity":"high"}`))
			case http.MethodPatch:
				var req wireUpdateIncident
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &req)
				if req.Status != string(duty.StatusAcknowledged) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"id":"inc-1","status":"acknowledged","severity":"critical"}`))
			}
		},
	}}
	_, cli := newTestClient(t, f)
	ctx := context.Background()

	created, err := cli.CreateIncident(ctx, duty.CreateIncidentRequest{Title: "DB is down", Severity: duty.SeverityCritical})
	if err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	if created.ID != "inc-1" || created.Status != duty.StatusTriggered {
		t.Fatalf("CreateIncident = %+v, want inc-1 triggered", created)
	}

	updated, err := cli.UpdateIncident(ctx, "inc-1", duty.UpdateIncidentRequest{Status: duty.StatusAcknowledged})
	if err != nil {
		t.Fatalf("UpdateIncident returned error: %v", err)
	}
	if updated.Status != duty.StatusAcknowledged {
		t.Fatalf("UpdateIncident status = %q, want acknowledged", updated.Status)
	}

	got, err := cli.GetIncident(ctx, "inc-1")
	if err != nil {
		t.Fatalf("GetIncident returned error: %v", err)
	}
	if got.Status != duty.StatusResolved {
		t.Fatalf("GetIncident status = %q, want resolved", got.Status)
	}
}

func TestClientRejectsNonSuccessStatus(t *testing.T) {
	f := &fakeServer{handlers: map[string]http.HandlerFunc{
		"/api/v1/schedules": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		},
	}}
	_, cli := newTestClient(t, f)

	if _, err := cli.Schedules(context.Background()); err == nil {
		t.Fatal("ListSchedules returned no error for a 500")
	}
}

func TestNoopClientWhenEndpointMissing(t *testing.T) {
	cli := NewClient("  ", "user", "pass")
	if cli.Configured() {
		t.Fatal("Configured() = true for an empty endpoint")
	}
	if _, err := cli.Schedules(context.Background()); err == nil {
		t.Fatal("noop ListSchedules returned no error")
	}
	if _, err := cli.CreateIncident(context.Background(), duty.CreateIncidentRequest{Title: "x"}); err == nil {
		t.Fatal("noop CreateIncident returned no error")
	}
}

func TestClientRequiresUsername(t *testing.T) {
	f := &fakeServer{handlers: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	cli := NewClient(srv.URL, "", "pass")
	if _, err := cli.Schedules(context.Background()); err == nil {
		t.Fatal("ListSchedules with no username returned no error")
	}
}

// TestReloginKeepsTheCookieJar pins the concurrency fix: clearing the session
// must clear the jar in place. Assigning a new *Jar to the client while
// requests are in flight is a data race, and the jar's own API is safe for
// concurrent use.
func TestReloginKeepsTheCookieJar(t *testing.T) {
	f := &fakeServer{handlers: map[string]http.HandlerFunc{
		"/api/v1/schedules": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[],"total":0}`))
		},
	}}
	_, provider := newTestClient(t, f)
	cli, ok := provider.(*Client)
	if !ok {
		t.Fatalf("NewClient returned %T, want *Client", provider)
	}

	jar := cli.client.CookieJar()
	if err := cli.ensureSession(context.Background()); err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if err := cli.relogin(context.Background()); err != nil {
		t.Fatalf("relogin: %v", err)
	}
	if cli.client.CookieJar() != jar {
		t.Fatal("relogin replaced the cookie jar; it must be cleared in place")
	}
}
