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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
)

// TestGetRunReadsTheEnvelope pins the shape the server actually writes.
//
// GET /v1/agent/runs/:id returns `{run: {...}, events: [...]}`, and the run
// row is snake_case (it is serialized from the durable model, not from the
// camelCase create response). Decoding that envelope into the flat Run type
// left every field empty, so the Job provider could never observe a terminal
// state and scheduled executions stayed "running" forever.
func TestGetRunReadsTheEnvelope(t *testing.T) {
	const body = `{
		"run": {
			"id": "run-1",
			"session_id": "sess-1",
			"state": "succeeded",
			"error": ""
		},
		"events": []
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	client := NewForConfig(&genericoptions.RestyOptions{Endpoint: srv.URL})
	run, err := client.GetRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.ID != "run-1" || run.SessionID != "sess-1" || run.State != "succeeded" {
		t.Fatalf("decoded run = %+v, want run-1/sess-1/succeeded", run)
	}
}
