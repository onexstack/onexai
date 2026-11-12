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

package agentrun

import (
	"context"

	"github.com/onexstack/onexai/internal/controller/job/job/providers/registry"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// httpClient adapts the signed agent client to the narrow interface the
// registry exposes, so the provider never depends on the full client surface.
type httpClient struct {
	client *agentclient.Client
}

// NewHTTPClient returns a registry.AgentRunClient backed by the agent client.
func NewHTTPClient(c *agentclient.Client) registry.AgentRunClient {
	return &httpClient{client: c}
}

var _ registry.AgentRunClient = (*httpClient)(nil)

func (h *httpClient) CreateRun(ctx context.Context, req *registry.AgentRunRequest) (*registry.AgentRunResult, error) {
	out, err := h.client.CreateRun(ctx, &agentclient.RunRequest{
		DedupKey:         req.DedupKey,
		Agent:            req.Agent,
		Prompt:           req.Prompt,
		SubjectID:        req.SubjectID,
		SubjectName:      req.SubjectName,
		Metering:         req.Metering,
		ApprovalFallback: req.ApprovalFallback,
		SessionPolicy:    req.SessionPolicy,
		Source:           req.Source,
		Trigger:          req.Trigger,
		ScheduleRef:      req.ScheduleRef,
		ScheduledTime:    req.ScheduledTime,
		Attempt:          req.Attempt,
		TimeoutSeconds:   req.TimeoutSeconds,
		Metadata:         req.Metadata,
	})
	if err != nil {
		return nil, err
	}
	return result(out), nil
}

func (h *httpClient) GetRun(ctx context.Context, id string) (*registry.AgentRunResult, error) {
	out, err := h.client.GetRun(ctx, id)
	if err != nil {
		// Translate only the one refusal the provider must act on: a run the
		// service no longer knows is terminal, not a retryable failure.
		if agentclient.IsNotFound(err) {
			return nil, registry.ErrRunNotFound
		}
		return nil, err
	}
	return result(out), nil
}

func (h *httpClient) CancelRun(ctx context.Context, id string) error {
	err := h.client.CancelRun(ctx, id)
	if agentclient.IsNotFound(err) {
		// Already gone: the cancel achieved what it wanted.
		return nil
	}
	return err
}

func result(out *agentclient.Run) *registry.AgentRunResult {
	message := out.Message
	if message == "" {
		message = out.FailureReason
	}
	return &registry.AgentRunResult{
		ID:        out.ID,
		SessionID: out.SessionID,
		State:     out.State,
		Message:   message,
	}
}
