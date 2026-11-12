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
	"fmt"
	"net/http"
)

// RunRequest is the body of POST /v1/agent/runs. The caller supplies the
// dedupKey; the server creates at most one run for it, so a retried dispatch is
// safe.
type RunRequest struct {
	DedupKey         string            `json:"dedupKey"`
	Agent            string            `json:"agent"`
	Prompt           string            `json:"prompt"`
	SubjectID        string            `json:"subjectID"`
	SubjectName      string            `json:"subjectName,omitempty"`
	Metering         string            `json:"metering,omitempty"`
	ApprovalFallback string            `json:"approvalFallback,omitempty"`
	SessionPolicy    string            `json:"sessionPolicy,omitempty"`
	Source           string            `json:"source,omitempty"`
	Trigger          string            `json:"trigger,omitempty"`
	ScheduleRef      string            `json:"scheduleRef,omitempty"`
	ScheduledTime    int64             `json:"scheduledTime,omitempty"`
	Attempt          int32             `json:"attempt,omitempty"`
	TimeoutSeconds   int64             `json:"timeoutSeconds,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// Run is the server's representation of one durable agent run.
type Run struct {
	ID            string `json:"id"`
	SessionID     string `json:"sessionID"`
	State         string `json:"state"`
	Message       string `json:"message,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

// CreateRun submits a run and returns the durable record. A duplicate dedupKey
// returns the existing run rather than an error.
func (c *Client) CreateRun(ctx context.Context, req *RunRequest) (*Run, error) {
	const path = "/v1/agent/runs"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, false)
	if err != nil {
		return nil, err
	}
	var out Run
	r, err := rq.SetResult(&out).Post(path)
	if err != nil {
		return nil, fmt.Errorf("agent create run: %w", err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent create run returned %d: %s", r.StatusCode(), r.String())
	}
	return &out, nil
}

// getRunResponse is the server's envelope for GET /v1/agent/runs/:id. The run
// row uses snake_case in the durable store, so it cannot be decoded straight
// into Run (which is the camelCase create response shape).
type getRunResponse struct {
	Run struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		State     string `json:"state"`
		Error     string `json:"error"`
	} `json:"run"`
}

// GetRun returns one run by ID.
func (c *Client) GetRun(ctx context.Context, id string) (*Run, error) {
	var out getRunResponse
	if err := c.get(ctx, "/v1/agent/runs/"+id, &out); err != nil {
		return nil, err
	}
	return &Run{
		ID:            out.Run.ID,
		SessionID:     out.Run.SessionID,
		State:         out.Run.State,
		FailureReason: out.Run.Error,
	}, nil
}

// CancelRun cancels a run that has not reached a terminal state.
func (c *Client) CancelRun(ctx context.Context, id string) error {
	path := "/v1/agent/runs/" + id + "/cancel"
	rq, err := c.prepare(ctx, http.MethodPost, path, nil, false)
	if err != nil {
		return err
	}
	r, err := rq.Post(path)
	if err != nil {
		return fmt.Errorf("agent cancel run: %w", err)
	}
	if r.IsStatusFailure() {
		return fmt.Errorf("agent cancel run: %w", newHTTPError(r))
	}
	return nil
}
