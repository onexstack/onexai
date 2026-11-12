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

package run

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

const (
	defaultAdminRunLimit = 20
	maxAdminRunLimit     = 200
	defaultAdminEvents   = 200
	maxAdminEvents       = 500
	promptPreviewRunes   = 200
)

// AdminRun is one row of the management async-task list. It is intentionally
// narrower than RunM: the list does not need lease, dedup key or the request
// body, and the prompt is reduced to a preview.
type AdminRun struct {
	ID            string `json:"id"`
	SessionID     string `json:"sessionId"`
	Agent         string `json:"agent"`
	Subject       string `json:"subject"`
	Source        string `json:"source"`
	ScheduleRef   string `json:"scheduleRef"`
	Trigger       string `json:"trigger"`
	State         string `json:"state"`
	Error         string `json:"error"`
	Attempt       int32  `json:"attempt"`
	StartedAt     int64  `json:"startedAt"`
	FinishedAt    int64  `json:"finishedAt"`
	CreatedAt     int64  `json:"createdAt"`
	PromptPreview string `json:"promptPreview"`
}

// AdminRunDetail carries the fields only the detail page needs.
type AdminRunDetail struct {
	AdminRun
	// RunState is the raw run.state. The list state is the admission state,
	// which is the task lifecycle; this field is for troubleshooting only.
	RunState    string            `json:"runState"`
	Prompt      string            `json:"prompt"`
	UsageJSON   string            `json:"usageJson"`
	TaskJSON    string            `json:"taskJson"`
	Actor       string            `json:"actor"`
	Runtime     string            `json:"runtime"`
	RequestID   string            `json:"requestId"`
	RequestJSON string            `json:"requestJson"`
	Metadata    map[string]string `json:"metadata"`
}

// AdminAdmissionView is the admission half of a detail response.
type AdminAdmissionView struct {
	State      string `json:"state"`
	Message    string `json:"message"`
	ClaimCount int32  `json:"claimCount"`
	CreatedAt  int64  `json:"createdAt"`
}

// ListAdminRunsResponse is one page of asynchronous tasks.
type ListAdminRunsResponse struct {
	Runs  []AdminRun `json:"runs"`
	Count int64      `json:"count"`
}

// GetAdminRunResponse is one asynchronous task with its admission.
type GetAdminRunResponse struct {
	Run       AdminRunDetail     `json:"run"`
	Admission AdminAdmissionView `json:"admission"`
}

// AdminRunEvent is the camelCase view the console consumes. The durable
// RunEventM carries snake_case JSON because it is also the signed API's shape;
// the management surface spells its own fields.
type AdminRunEvent struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	CreatedAt int64  `json:"createdAt"`
}

// AdminRunEventsResponse is one page of events on the management surface.
type AdminRunEventsResponse struct {
	Events []AdminRunEvent `json:"events"`
}

// ListAdminRuns implements RunBiz.
func (b *runBiz) ListAdminRuns(
	ctx context.Context, req *v1.ListAdminRunQuery,
) (*ListAdminRunsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultAdminRunLimit
	}
	if limit > maxAdminRunLimit {
		limit = maxAdminRunLimit
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	total, admissions, err := b.store.ListAdmissionPage(ctx, runstore.AdmissionFilter{
		Offset:      req.Offset,
		Limit:       limit,
		State:       req.State,
		Agent:       req.Agent,
		Trigger:     req.Trigger,
		Source:      req.Source,
		ScheduleRef: req.ScheduleRef,
		SessionID:   req.SessionID,
		Keyword:     req.Keyword,
		From:        req.From,
		To:          req.To,
	})
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}

	ids := make([]string, 0, len(admissions))
	for _, admission := range admissions {
		ids = append(ids, admission.ID)
	}
	runs, err := b.store.ListRunsByIDs(ctx, ids)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	runByID := make(map[string]*model.RunM, len(runs))
	for _, row := range runs {
		runByID[row.ID] = row
	}

	items := make([]AdminRun, 0, len(admissions))
	for _, admission := range admissions {
		payload := decodeAdmitRequest(admission.RequestJSON)
		items = append(items, adminRunOf(admission, runByID[admission.ID], payload))
	}
	return &ListAdminRunsResponse{Runs: items, Count: total}, nil
}

// GetAdminRun implements RunBiz.
func (b *runBiz) GetAdminRun(
	ctx context.Context, req *v1.GetAdminRunRequest,
) (*GetAdminRunResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}

	admission, err := b.adminAdmission(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	payload := decodeAdmitRequest(admission.RequestJSON)

	row, err := b.store.GetRun(ctx, admission.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}

	detail := AdminRunDetail{
		AdminRun:    adminRunOf(admission, row, payload),
		Prompt:      payload.Prompt,
		RequestJSON: admission.RequestJSON,
		Metadata:    payload.Metadata,
	}
	if row != nil {
		detail.RunState = row.State
		detail.UsageJSON = row.UsageJSON
		detail.TaskJSON = row.TaskJSON
		detail.Actor = row.Actor
		detail.Runtime = row.Runtime
		detail.RequestID = row.RequestID
	}

	return &GetAdminRunResponse{
		Run: detail,
		Admission: AdminAdmissionView{
			State:      admission.State,
			Message:    admission.Message,
			ClaimCount: admission.ClaimCount,
			CreatedAt:  admission.CreatedAt,
		},
	}, nil
}

// ListAdminRunEvents implements RunBiz.
func (b *runBiz) ListAdminRunEvents(
	ctx context.Context, req *v1.ListAdminRunEventQuery,
) (*AdminRunEventsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	if _, err := b.adminAdmission(ctx, req.ID); err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultAdminEvents
	}
	if limit > maxAdminEvents {
		limit = maxAdminEvents
	}
	events, err := b.store.ListEvent(ctx, req.ID, req.After, limit)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}

	out := make([]AdminRunEvent, 0, len(events))
	for _, event := range events {
		out = append(out, AdminRunEvent{
			Seq:       event.Seq,
			Type:      event.Type,
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	return &AdminRunEventsResponse{Events: out}, nil
}

// CancelAdminRun implements RunBiz. It is idempotent: a terminal admission is
// already in the requested condition.
func (b *runBiz) CancelAdminRun(ctx context.Context, req *v1.CancelAdminRunRequest) error {
	if b.store == nil {
		return errno.ErrNotFound.WithMessage("run store is not configured")
	}

	admission, err := b.adminAdmission(ctx, req.ID)
	if err != nil {
		return err
	}
	if isTerminalRunState(admission.State) {
		return nil
	}

	// A pending admission has no engine run yet; only a run that has started
	// has a process to stop.
	if row, err := b.store.GetRun(ctx, admission.ID); err == nil && row != nil {
		if b.agent == nil {
			return errno.ErrOperationFailed.WithMessage("the agent engine is not configured")
		}
		if row.State == model.RunStateRunning {
			if err := b.agent.Stop(ctx, admission.ID); err != nil {
				return err
			}
		}
	}
	return b.store.CancelAdmission(ctx, admission.ID, "canceled")
}

// RetryAdminRun implements RunBiz. It replays the original admission's request
// as a new task; the original job (if any) is left untouched.
func (b *runBiz) RetryAdminRun(
	ctx context.Context, req *v1.RetryAdminRunRequest,
) (*model.RunAdmissionM, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}

	admission, err := b.adminAdmission(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	switch admission.State {
	case model.RunStateFailed, model.RunStateCanceled:
		// Retryable.
	case model.RunStatePending, model.RunStateRunning:
		return nil, errno.ErrRunNotRetryable.WithMessage("the run is still active")
	default:
		return nil, errno.ErrRunNotRetryable.WithMessage("only a failed or canceled run can be retried")
	}

	payload := decodeAdmitRequest(admission.RequestJSON)
	// Rows written before the payload was denormalized may still be retryable:
	// the admission itself carries the agent and subject.
	if payload.Agent == "" {
		payload.Agent = admission.Agent
	}
	if payload.SubjectID == "" {
		payload.SubjectID = admission.Subject
	}
	if strings.TrimSpace(payload.Agent) == "" || strings.TrimSpace(payload.Prompt) == "" {
		return nil, errno.ErrRunNotRetryable.WithMessage("the original request is unavailable")
	}

	metadata := make(map[string]string, len(payload.Metadata)+1)
	for key, value := range payload.Metadata {
		metadata[key] = value
	}
	metadata["retryOf"] = admission.ID

	payload.DedupKey = ""
	payload.Trigger = "retry"
	payload.Attempt = admission.Attempt + 1
	payload.ScheduledTime = 0
	payload.Metadata = metadata
	if payload.Source == "" {
		payload.Source = admission.Source
	}
	if payload.ScheduleRef == "" {
		payload.ScheduleRef = admission.ScheduleRef
	}

	retried, _, err := b.Admit(ctx, payload)
	if err != nil {
		return nil, err
	}
	return retried, nil
}

func (b *runBiz) adminAdmission(ctx context.Context, id string) (*model.RunAdmissionM, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("run id is required")
	}
	admission, err := b.store.GetAdmission(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errno.ErrNotFound.WithMessage("run not found")
		}
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return admission, nil
}

func adminRunOf(
	admission *model.RunAdmissionM, row *model.RunM, payload *AdmitRequest,
) AdminRun {
	state := normalizeRunState(admission.State)
	if state == "" && row != nil {
		state = normalizeRunState(row.State)
	}
	errMessage := admission.Message
	if row != nil && row.Error != "" {
		errMessage = row.Error
	}

	var startedAt, finishedAt int64
	if row != nil {
		startedAt = row.StartedAt
		finishedAt = row.FinishedAt
	}

	scheduleRef := cmp.Or(admission.ScheduleRef, payload.ScheduleRef)
	return AdminRun{
		ID:            admission.ID,
		SessionID:     admission.SessionID,
		Agent:         cmp.Or(admission.Agent, payload.Agent),
		Subject:       cmp.Or(admission.Subject, payload.SubjectID),
		Source:        cmp.Or(admission.Source, payload.Source),
		ScheduleRef:   scheduleRef,
		Trigger:       deriveTrigger(admission, payload),
		State:         state,
		Error:         errMessage,
		Attempt:       admission.Attempt,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		CreatedAt:     admission.CreatedAt,
		PromptPreview: truncateRunes(payload.Prompt, promptPreviewRunes),
	}
}

func deriveTrigger(admission *model.RunAdmissionM, payload *AdmitRequest) string {
	if admission.Trigger != "" {
		return admission.Trigger
	}
	if payload.Trigger != "" {
		return payload.Trigger
	}
	if payload.Metadata != nil && payload.Metadata["trigger"] != "" {
		return payload.Metadata["trigger"]
	}
	if admission.ScheduleRef != "" || payload.ScheduleRef != "" {
		return "scheduled"
	}
	return "unknown"
}

func decodeAdmitRequest(raw string) *AdmitRequest {
	payload := &AdmitRequest{}
	if strings.TrimSpace(raw) == "" {
		return payload
	}
	if err := json.Unmarshal([]byte(raw), payload); err != nil {
		// A payload written by an older build may not match today's struct.
		// The task itself is still listable; only the derived fields are empty.
		return &AdmitRequest{}
	}
	return payload
}

// normalizeRunState maps the run vocabulary to the admission vocabulary the
// management API exposes. `completed` is the run's success spelling.
func normalizeRunState(state string) string {
	if state == model.RunStateCompleted {
		return model.RunStateSucceeded
	}
	return state
}

func isTerminalRunState(state string) bool {
	switch state {
	case model.RunStateSucceeded, model.RunStateFailed, model.RunStateCanceled:
		return true
	default:
		return false
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
