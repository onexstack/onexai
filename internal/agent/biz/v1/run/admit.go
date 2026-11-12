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
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/safego"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// DefaultAdmissionTimeout bounds an admitted run when the caller does not set
// one. It matches the agent server's historical scheduled-run default.
const DefaultAdmissionTimeout = 15 * time.Minute

// AdmitRequest is the durable payload of an asynchronous run. It is stored as
// JSON on the admission row so a dispatcher can resume after a restart without
// the original HTTP request.
type AdmitRequest struct {
	DedupKey         string            `json:"dedupKey"`
	Agent            string            `json:"agent"`
	Prompt           string            `json:"prompt"`
	SubjectID        string            `json:"subjectID"`
	SubjectName      string            `json:"subjectName,omitempty"`
	SubjectKind      string            `json:"subjectKind,omitempty"`
	Metering         string            `json:"metering,omitempty"`
	ApprovalFallback string            `json:"approvalFallback,omitempty"`
	SessionPolicy    string            `json:"sessionPolicy,omitempty"`
	Source           string            `json:"source,omitempty"`
	Trigger          string            `json:"trigger,omitempty"`
	TimeoutSeconds   int64             `json:"timeoutSeconds,omitempty"`
	ScheduleRef      string            `json:"scheduleRef,omitempty"`
	ScheduledTime    int64             `json:"scheduledTime,omitempty"`
	Attempt          int32             `json:"attempt,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// Admit creates the durable admission for an asynchronous run. The bool
// reports whether this call created it; a duplicate dedup key returns the
// existing row.
func (b *runBiz) Admit(ctx context.Context, req *AdmitRequest) (*model.RunAdmissionM, bool, error) {
	if b.store == nil {
		return nil, false, errno.ErrOperationFailed.WithMessage("run store is not configured")
	}
	if req.Agent == "" || req.Prompt == "" {
		return nil, false, errno.ErrInvalidArgument.WithMessage("agent and prompt are required")
	}
	if req.SubjectID == "" {
		return nil, false, errno.ErrInvalidArgument.WithMessage("subjectID is required")
	}
	// ScheduledTime is Unix milliseconds everywhere else in this schema. A
	// seconds value would be interpreted as a time before 1970 and run
	// immediately, so reject the wrong unit instead of silently running early.
	if req.ScheduledTime > 0 && req.ScheduledTime < 1_000_000_000_000 {
		return nil, false, errno.ErrInvalidArgument.WithMessage("scheduledTime must be Unix milliseconds")
	}

	dedupKey := req.DedupKey
	if dedupKey == "" {
		// No key means no deduplication: a random one keeps the unique index
		// satisfied without collapsing two independent requests.
		dedupKey = uuid.NewString()
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, false, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}

	admission := &model.RunAdmissionM{
		ID:            uuid.NewString(),
		DedupKey:      dedupKey,
		SessionID:     uuid.NewString(),
		Subject:       req.SubjectID,
		Agent:         req.Agent,
		Trigger:       req.Trigger,
		Source:        req.Source,
		ScheduleRef:   req.ScheduleRef,
		ScheduledTime: req.ScheduledTime,
		Attempt:       req.Attempt,
		State:         model.RunStatePending,
		RequestJSON:   string(payload),
		CreatedAt:     time.Now().UnixMilli(),
	}
	return b.store.AdmitRun(ctx, admission)
}

// Admission lease parameters. TTL is three heartbeats so one missed renewal
// does not hand a live run to another replica; the claim count bounds a poison
// pill that crashes its owner before it can record an outcome.
const (
	AdmissionLeaseTTL          = 90 * time.Second
	AdmissionHeartbeatInterval = 30 * time.Second
	MaxAdmissionClaims         = int32(5)
)

// Execute claims one admission and runs it. It is safe to call concurrently:
// ClaimAdmission lets exactly one caller continue. Leadership is a lease, not
// a boolean: the owner renews it while the run executes, and every terminal
// write is fenced on it, so a replica that lost its lease cannot overwrite the
// result of the replica that replaced it.
func (b *runBiz) Execute(ctx context.Context, id string) {
	if b.store == nil || b.agent == nil {
		return
	}
	defer safego.Recover(ctx, "run.Execute "+id)

	owner := uuid.NewString()
	result, err := b.store.ClaimAdmission(ctx, id, owner, AdmissionLeaseTTL, MaxAdmissionClaims)
	if err != nil {
		slog.WarnContext(ctx, "could not claim run admission", "id", id, "error", err)
		return
	}
	switch result {
	case runstore.ClaimNotAcquired:
		return
	case runstore.ClaimExhausted:
		if err := b.store.ExhaustAdmission(ctx, id, MaxAdmissionClaims); err != nil {
			slog.WarnContext(ctx, "could not exhaust run admission", "id", id, "error", err)
		}
		return
	}

	// Re-read after the claim: a cancel can land between the UPDATE and this
	// read, and only the current owner may run the admission.
	admission, err := b.store.GetAdmission(ctx, id)
	if err != nil {
		slog.WarnContext(ctx, "could not read the claimed admission", "id", id, "error", err)
		b.finish(id, owner, model.RunStateFailed, "could not read the admission")
		return
	}
	if admission.State != model.RunStateRunning || admission.LeaseOwner != owner {
		return
	}

	var req AdmitRequest
	if err := json.Unmarshal([]byte(admission.RequestJSON), &req); err != nil {
		b.finish(id, owner, model.RunStateFailed, err.Error())
		return
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultAdmissionTimeout
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stopLease := b.keepLease(runCtx, id, owner, cancel)
	defer stopLease()

	events, err := b.agent.Ask(runCtx, askRequest(admission, &req, timeout))
	if err != nil {
		b.finish(id, owner, model.RunStateFailed, err.Error())
		return
	}
	// Ask's relay persists the run and its terminal state; draining the channel
	// is what lets that relay finish.
	for range events {
	}

	state, message := b.admissionOutcome(id)
	b.finish(id, owner, state, message)
}

// keepLease renews the admission lease until the returned stop function is
// called. Losing the lease means the run was canceled or another replica took
// over, so the engine context is canceled instead of letting this process
// write results nobody asked for.
func (b *runBiz) keepLease(
	ctx context.Context, id, owner string, cancel context.CancelFunc,
) func() {
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(AdmissionHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				held, err := b.store.RenewAdmission(ctx, id, owner, AdmissionLeaseTTL)
				if err != nil {
					// A transient database error may still leave the lease
					// valid; the next tick decides. A sustained outage lets
					// the lease expire and another replica recover the run.
					slog.WarnContext(ctx, "could not renew run admission lease", "id", id, "error", err)
					continue
				}
				if !held {
					slog.WarnContext(ctx, "run admission lease lost; stopping the run", "id", id)
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

// finish records a terminal outcome, logging instead of returning because the
// caller has no way to act on the error: the run itself has already ended.
func (b *runBiz) finish(id, owner, state, message string) {
	ok, err := b.store.FinishAdmission(context.Background(), id, owner, state, message)
	if err != nil {
		slog.Warn("could not record run admission outcome", "id", id, "state", state, "error", err)
		return
	}
	if !ok {
		slog.Warn("run admission outcome was not recorded: lease lost or already terminal", "id", id, "state", state)
	}
}

func askRequest(
	admission *model.RunAdmissionM, req *AdmitRequest, timeout time.Duration,
) *agentapi.AskRequest {
	kind := req.SubjectKind
	if kind == "" {
		kind = agentapi.PrincipalKindUser
	}
	fallback := req.ApprovalFallback
	if fallback == "" {
		fallback = agentapi.ApprovalFallbackDeny
	}
	return &agentapi.AskRequest{
		SessionID: admission.SessionID,
		RunID:     admission.ID,
		Input:     agentapi.Input{Text: req.Prompt},
		Selection: agentapi.Selection{Agents: []string{req.Agent}},
		Subject:   agentapi.Principal{ID: req.SubjectID, Name: req.SubjectName, Kind: kind},
		Metering:  meteringFromString(req.Metering),
		Options: agentapi.ExecutionOptions{
			Deadline: timeout,
			Approval: agentapi.Approval{Channel: agentapi.ApprovalChannelNone, Fallback: fallback},
		},
		Metadata: req.Metadata,
		Source:   req.Source,
	}
}

func meteringFromString(value string) agentapi.Metering {
	switch value {
	case "none":
		return agentapi.MeteringNone
	case "caller":
		return agentapi.MeteringCaller
	case "runtime":
		return agentapi.MeteringRuntime
	default:
		return agentapi.MeteringUnspecified
	}
}

func admissionAsRun(admission *model.RunAdmissionM) *model.RunM {
	return &model.RunM{
		ID:        admission.ID,
		SessionID: admission.SessionID,
		Subject:   admission.Subject,
		State:     admission.State,
		Error:     admission.Message,
		StartedAt: admission.CreatedAt,
		CreatedAt: admission.CreatedAt,
	}
}

// admissionOutcome maps the run record's terminal state to the admission
// vocabulary the client understands.
func (b *runBiz) admissionOutcome(id string) (string, string) {
	row, err := b.store.GetRun(context.Background(), id)
	if err != nil {
		return model.RunStateFailed, "run record was not written"
	}
	switch row.State {
	case model.RunStateCompleted:
		return model.RunStateSucceeded, ""
	case model.RunStateCanceled:
		return model.RunStateCanceled, row.Error
	default:
		return model.RunStateFailed, row.Error
	}
}
