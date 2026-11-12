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

// Package llmhttp provides retry-with-model-fallback around an
// OpenAI-compatible HTTP call. It retries transient failures (network errors
// and 429/5xx) with exponential backoff, and on exhaustion tries the next
// model in the list, so a primary-model outage degrades to a fallback model.
package llmhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ReqFunc builds a fresh *http.Request for the given model. It must produce a
// new request on each call (bodies are consumed by Do).
type ReqFunc func(model string) (*http.Request, error)

// Config tunes retry/fallback behavior.
type Config struct {
	// Models is the ordered list to try (primary first, then fallbacks). Empty
	// entries are skipped.
	Models []string
	// MaxRetries is the number of retries per model on transient failures.
	MaxRetries int
	// Backoff is the base backoff; attempt n waits Backoff*2^n.
	Backoff time.Duration
}

// Do issues the request with retry and model fallback, returning the first
// successful (2xx) response. The caller owns closing the returned body.
func Do(ctx context.Context, client *http.Client, cfg Config, build ReqFunc) (*http.Response, error) {
	if cfg.Backoff <= 0 {
		cfg.Backoff = 500 * time.Millisecond
	}
	models := cfg.Models
	if len(models) == 0 {
		models = []string{""}
	}

	var lastErr error
	for _, model := range models {
		if model == "" && len(cfg.Models) > 0 {
			continue
		}
		for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
			if attempt > 0 {
				if err := sleep(ctx, cfg.Backoff<<(attempt-1)); err != nil {
					return nil, err
				}
			}
			req, err := build(model)
			if err != nil {
				return nil, err
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue // transient network error → retry
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return resp, nil
			}
			if isTransient(resp.StatusCode) {
				lastErr = statusError(resp)
				continue // retry same model
			}
			// Non-transient (e.g. 400/401) — fallback won't help.
			return nil, statusError(resp)
		}
		// Retries exhausted for this model → try next fallback model.
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("llm request failed")
	}
	return nil, lastErr
}

func isTransient(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// HTTPError is a non-2xx response from the model endpoint.
//
// The status code is exposed because a caller above this layer has to decide
// whether another model is worth trying: a 401 on a wrong key fails the same
// way everywhere, while a 503 is exactly what a fallback is for.
type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("LLM API error %d: %s", e.Code, e.Body)
}

// Retryable reports whether the same endpoint may succeed on another attempt.
func (e *HTTPError) Retryable() bool {
	return isTransient(e.Code)
}

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	return &HTTPError{Code: resp.StatusCode, Body: string(b)}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
