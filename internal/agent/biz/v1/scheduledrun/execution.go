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

package scheduledrun

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ExecutionBiz is the scheduled-execution business logic.
type ExecutionBiz interface {
	// List returns the executions of one schedule.
	List(ctx context.Context, req *v1.ListScheduledExecutionQuery) (*ExecutionsResponse, error)
	// Trigger admits a manual run.
	Trigger(ctx context.Context, req *v1.TriggerScheduledRunRequest) (*ExecutionResponse, error)
	// Cancel stops one execution.
	Cancel(ctx context.Context, req *v1.CancelScheduledExecutionRequest) error
}

// ExecutionsResponse is a page of executions.
type ExecutionsResponse struct {
	Executions []*model.ScheduledExecutionM `json:"executions"`
}

// ExecutionResponse is one execution.
type ExecutionResponse struct {
	Execution *model.ScheduledExecutionM `json:"execution"`
}

type executionBiz struct {
	executions agentstore.ScheduledExecutionStore
}

var _ ExecutionBiz = (*executionBiz)(nil)

// NewExecution creates an ExecutionBiz. A manual trigger now creates a child
// Job; the batch CronJob controller owns the timing, so no scheduler is held.
func NewExecution(executions agentstore.ScheduledExecutionStore) *executionBiz {
	return &executionBiz{executions: executions}
}

// List implements ExecutionBiz.
func (b *executionBiz) List(
	ctx context.Context, req *v1.ListScheduledExecutionQuery,
) (*ExecutionsResponse, error) {
	if b.executions == nil {
		return nil, errno.ErrNotFound.WithMessage("scheduled execution store is not configured")
	}
	executions, err := b.executions.ListExecution(ctx, req.Namespace, req.Name, req.Limit)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &ExecutionsResponse{Executions: executions}, nil
}

// Trigger implements ExecutionBiz by cloning the CronJob's JobTemplate into a
// one-off Job. The handler answers 202; the Job controller executes it.
func (b *executionBiz) Trigger(
	ctx context.Context, req *v1.TriggerScheduledRunRequest,
) (*ExecutionResponse, error) {
	if b.executions == nil {
		return nil, errno.ErrNotFound.WithMessage("scheduled execution store is not configured")
	}
	execution, err := b.executions.CreateManualExecution(ctx, "", req.GetName())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errno.ErrNotFound.WithMessage("scheduled run not found")
		}
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &ExecutionResponse{Execution: execution}, nil
}

// Cancel implements ExecutionBiz. Deleting the Job is what cancels the run: the
// Job controller's finalizer calls the agent stop before the object goes away.
func (b *executionBiz) Cancel(ctx context.Context, req *v1.CancelScheduledExecutionRequest) error {
	if b.executions == nil {
		return errno.ErrNotFound.WithMessage("scheduled execution store is not configured")
	}
	execution, err := b.executions.GetExecution(ctx, req.GetId())
	if err != nil {
		return errno.ErrNotFound.WithMessage("execution not found")
	}
	if execution.Schedule != req.GetName() {
		return errno.ErrPermissionDenied.WithMessage("the execution belongs to another schedule")
	}
	if err := b.executions.CancelExecution(ctx, execution.ID); err != nil {
		return errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return nil
}
