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

package llmhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
}

func build(model string) ReqFunc {
	return func(m string) (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "http://x/chat/completions", strings.NewReader("{}"))
	}
}

func TestDo_RetriesTransientThenSucceeds(t *testing.T) {
	var calls int
	client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return resp(http.StatusServiceUnavailable), nil
		}
		return resp(http.StatusOK), nil
	})}
	r, err := Do(context.Background(), client, Config{Models: []string{"m1"}, MaxRetries: 3, Backoff: time.Millisecond}, build("m1"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = r.Body.Close()
	if calls != 3 {
		t.Fatalf("want 3 calls, got %d", calls)
	}
}

func TestDo_FallsBackToSecondModel(t *testing.T) {
	var models []string
	client := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		// The model is in the JSON body; we can't read it easily here, so track
		// call order: first model always 503, second returns 200.
		models = append(models, "call")
		if len(models) <= 3 { // primary: 1 + 2 retries
			return resp(http.StatusServiceUnavailable), nil
		}
		return resp(http.StatusOK), nil
	})}
	r, err := Do(context.Background(), client, Config{Models: []string{"primary", "fallback"}, MaxRetries: 2, Backoff: time.Millisecond}, build("x"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = r.Body.Close()
	if len(models) != 4 {
		t.Fatalf("want 4 calls (3 primary + 1 fallback), got %d", len(models))
	}
}

func TestDo_NonTransientNoRetry(t *testing.T) {
	var calls int
	client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return resp(http.StatusBadRequest), nil
	})}
	_, err := Do(context.Background(), client, Config{Models: []string{"m1", "m2"}, MaxRetries: 3, Backoff: time.Millisecond}, build("m1"))
	if err == nil {
		t.Fatal("want error on 400")
	}
	if calls != 1 {
		t.Fatalf("want 1 call (no retry/fallback on 400), got %d", calls)
	}
}
