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

// Package observability implements the cluster observability tool calls.
package observability

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/pkg/k8sgpt"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The k8sgpt tool names these endpoints invoke. They are the fleet's own tool
// names, passed through rather than renamed, so an operator can correlate a
// request with the server log that served it.
const (
	getLogsTool    = "get-logs"
	listEventsTool = "list-events"
)

// ObservabilityBiz is the cluster observability call logic.
type ObservabilityBiz interface {
	// Configured reports whether a cluster fleet is wired up.
	Configured() bool
	// GetLogs asks one pod for its logs.
	GetLogs(ctx context.Context, req *v1.GetLogsParams) (*Result, error)
	// ListEvent asks for cluster events.
	ListEvent(ctx context.Context, req *v1.ListK8sEventParams) (*Result, error)
}

// Result carries the tool's output verbatim.
type Result struct {
	Result string `json:"result"`
}

type observabilityBiz struct {
	router *k8sgpt.Router
}

var _ ObservabilityBiz = (*observabilityBiz)(nil)

// New creates an ObservabilityBiz.
func New(router *k8sgpt.Router) *observabilityBiz { return &observabilityBiz{router: router} }

// Configured implements ObservabilityBiz.
func (b *observabilityBiz) Configured() bool { return b.router != nil }

// GetLogs implements ObservabilityBiz.
//
// It routes through the same Router the agent's tools use, so a request made
// over HTTP and the same request made by the model reach the identical cluster
// by the identical path.
func (b *observabilityBiz) GetLogs(
	ctx context.Context, req *v1.GetLogsParams,
) (*Result, error) {
	if b.router == nil {
		return nil, errno.ErrNotFound.WithMessage("observability is not configured")
	}
	args := map[string]any{"namespace": req.Namespace, "podName": req.PodName}
	if req.Container != "" {
		args["container"] = req.Container
	}
	if req.Previous != nil {
		args["previous"] = *req.Previous
	}
	if req.SinceSeconds != nil {
		args["sinceSeconds"] = *req.SinceSeconds
	}
	if req.TailLines != nil {
		args["tailLines"] = *req.TailLines
	}

	result, err := b.router.CallTool(ctx, req.AccountID, req.ClusterName, getLogsTool, args)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &Result{Result: result}, nil
}

// ListEvent implements ObservabilityBiz.
func (b *observabilityBiz) ListEvent(
	ctx context.Context, req *v1.ListK8sEventParams,
) (*Result, error) {
	if b.router == nil {
		return nil, errno.ErrNotFound.WithMessage("observability is not configured")
	}
	args := map[string]any{"namespace": req.Namespace}
	if req.InvolvedObjectKind != "" {
		args["involvedObjectKind"] = req.InvolvedObjectKind
	}
	if req.InvolvedObjectName != "" {
		args["involvedObjectName"] = req.InvolvedObjectName
	}
	if req.Limit != nil {
		args["limit"] = *req.Limit
	}

	result, err := b.router.CallTool(ctx, req.AccountID, req.ClusterName, listEventsTool, args)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &Result{Result: result}, nil
}
