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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// errServer answers every request with one status and body, which is all a
// refusal test needs: the interesting behaviour is how the client reads what
// came back, not what the server did to decide it.
func errServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewForConfig(&genericoptions.RestyOptions{Endpoint: srv.URL})
}

func noEvent(*v1.AgentStreamEvent) error { return nil }

// TestAgentAskStreamRefusalCarriesStatusAndReason pins the whole point of
// HTTPError: a refusal reaches the caller with the status and the server's own
// explanation, instead of the status-only string that used to make a 401, a 403
// and a 500 indistinguishable.
func TestAgentAskStreamRefusalCarriesStatusAndReason(t *testing.T) {
	const envelope = `{"reason":"PermissionDenied","message":"agent \"default\" access denied for subject ou_684c"}`

	err := errServer(t, http.StatusForbidden, envelope).
		AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want an *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", httpErr.StatusCode)
	}
	if httpErr.Reason != "PermissionDenied" {
		t.Errorf("Reason = %q, want PermissionDenied", httpErr.Reason)
	}
	if !strings.Contains(httpErr.Message, "access denied") {
		t.Errorf("Message = %q, want the server's explanation", httpErr.Message)
	}
	if !strings.Contains(err.Error(), "PermissionDenied") {
		t.Errorf("Error() = %q, want it to name the reason so the log says why", err)
	}
}

// TestPermissionClassification pins the pair the card keys on, and that the
// neighbouring statuses are not mistaken for one of them: a busy conversation
// is a retry, not a refusal.
func TestPermissionClassification(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		body           string
		wantUnauth     bool
		wantForbidden  bool
		wantPermission bool
	}{
		{"unauthorized", http.StatusUnauthorized, `{"reason":"Unauthenticated"}`, true, false, true},
		{"forbidden", http.StatusForbidden, `{"reason":"PermissionDenied"}`, false, true, true},
		{"conflict is not a refusal", http.StatusConflict, `{"reason":"OperationFailed"}`, false, false, false},
		{"server fault is not a refusal", http.StatusInternalServerError, `{"message":"boom"}`, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := errServer(t, tc.status, tc.body).
				AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)
			if got := IsUnauthenticated(err); got != tc.wantUnauth {
				t.Errorf("IsUnauthenticated = %v, want %v", got, tc.wantUnauth)
			}
			if got := IsForbidden(err); got != tc.wantForbidden {
				t.Errorf("IsForbidden = %v, want %v", got, tc.wantForbidden)
			}
			if got := IsPermissionError(err); got != tc.wantPermission {
				t.Errorf("IsPermissionError = %v, want %v", got, tc.wantPermission)
			}
		})
	}
}

// TestRefusalSentinelsSurviveTheRicherError pins that naming the refusals did
// not cost the callers that match 409/410 their sentinel: both errors have to
// stay matchable at once.
func TestRefusalSentinelsSurviveTheRicherError(t *testing.T) {
	t.Run("409 stays ErrBusy", func(t *testing.T) {
		err := errServer(t, http.StatusConflict, `{"reason":"OperationFailed","message":"busy"}`).
			AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("error = %v, want ErrBusy", err)
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
			t.Errorf("the status is no longer inspectable through the sentinel: %v", err)
		}
	})

	t.Run("410 stays ErrNotPending", func(t *testing.T) {
		err := errServer(t, http.StatusGone, `{"reason":"NotFound","message":"gone"}`).
			AgentApproveStream(context.Background(), &v1.AgentApproveRequest{RunID: "r"}, noEvent)
		if !errors.Is(err, ErrNotPending) {
			t.Fatalf("error = %v, want ErrNotPending", err)
		}
	})

	t.Run("a refused approval keeps its status", func(t *testing.T) {
		err := errServer(t, http.StatusForbidden, `{"reason":"PermissionDenied","message":"no"}`).
			AgentApproveStream(context.Background(), &v1.AgentApproveRequest{RunID: "r"}, noEvent)
		if !IsForbidden(err) {
			t.Fatalf("error = %v, want a 403 refusal", err)
		}
	})
}

// TestRefusalBodyFallbacks pins that a response which is not an envelope is
// still described rather than dropped, and that a huge one cannot make the
// client allocate all of it.
func TestRefusalBodyFallbacks(t *testing.T) {
	t.Run("a non-envelope body is kept", func(t *testing.T) {
		err := errServer(t, http.StatusBadGateway, "connection reset by peer").
			AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("error = %v, want an *HTTPError", err)
		}
		if httpErr.Body != "connection reset by peer" {
			t.Errorf("Body = %q, want the raw body", httpErr.Body)
		}
		if HTTPErrorMessage(err) != "connection reset by peer" {
			t.Errorf("HTTPErrorMessage = %q, want the raw body as the fallback", HTTPErrorMessage(err))
		}
	})

	t.Run("an oversized body is truncated", func(t *testing.T) {
		err := errServer(t, http.StatusBadGateway, strings.Repeat("a", maxErrorBodyBytes*2)).
			AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("error = %v, want an *HTTPError", err)
		}
		if len(httpErr.Body) != maxErrorBodyBytes {
			t.Errorf("Body is %d bytes, want it capped at %d", len(httpErr.Body), maxErrorBodyBytes)
		}
	})
}

// closeTrackingBody records whether it was closed, which is how a test tells a
// released connection from a leaked one.
type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRefusalBodiesAreClosed pins that every refusal branch reads and closes
// the body. A streamed request is opened with SetDoNotParseResponse, so nothing
// downstream consumes it: a branch that returns without closing leaks the
// connection, which is what the 409/410 branches used to do.
func TestRefusalBodiesAreClosed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		ask    bool
	}{
		{"forbidden ask", http.StatusForbidden, `{"reason":"PermissionDenied"}`, true},
		{"busy ask", http.StatusConflict, `{"reason":"OperationFailed"}`, true},
		{"server fault ask", http.StatusInternalServerError, `{"message":"boom"}`, true},
		{"gone approve", http.StatusGone, `{"reason":"NotFound"}`, false},
		{"busy approve", http.StatusConflict, `{"reason":"OperationFailed"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(tc.body)}
			client := NewForConfig(&genericoptions.RestyOptions{Endpoint: "http://agent.invalid"})
			client.client.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    tc.status,
					Status:        fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)),
					Header:        http.Header{"Content-Type": []string{"application/json"}},
					Body:          body,
					ContentLength: int64(len(tc.body)),
					Request:       req,
				}, nil
			}))

			var err error
			if tc.ask {
				err = client.AgentAskStream(context.Background(), &v1.AgentAskRequest{Input: &v1.Input{Text: "q"}}, noEvent)
			} else {
				err = client.AgentApproveStream(context.Background(), &v1.AgentApproveRequest{RunID: "r"}, noEvent)
			}
			if err == nil {
				t.Fatal("the refusal was reported as success")
			}
			if !body.closed {
				t.Error("the refusal body was not closed, which leaks the connection")
			}
		})
	}
}

// TestGetRunNotFoundCarriesStatus pins the one refusal the job controller keys
// on: a 404 has to reach it as a typed status, so it can settle the Job instead
// of retrying the poll forever.
func TestGetRunNotFoundCarriesStatus(t *testing.T) {
	_, err := errServer(t, http.StatusNotFound, `{"reason":"NotFound","message":"session not found"}`).
		GetRun(context.Background(), "run-1")
	if err == nil {
		t.Fatal("a 404 was reported as success")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false, want true", err)
	}
	if IsPermissionError(err) || IsTooManyRequests(err) {
		t.Fatalf("a missing run was classified as a permission or rate-limit refusal: %v", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %v, want an *HTTPError with status 404", err)
	}
}

// TestHTTPErrorMessageIgnoresOtherErrors keeps the log helper from inventing a
// message for an error that never came from a response.
func TestHTTPErrorMessageIgnoresOtherErrors(t *testing.T) {
	if got := HTTPErrorMessage(errors.New("dial tcp: refused")); got != "" {
		t.Errorf("HTTPErrorMessage = %q, want empty", got)
	}
}
