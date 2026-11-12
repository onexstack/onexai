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

// Package run implements the run, session, share-adjacent and revision query
// business logic over the durable run store.
package run

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/caller"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// RunBiz is the run/session/revision query business logic.
type RunBiz interface {
	// Get returns a run and its events.
	Get(ctx context.Context, req *v1.GetRunRequest) (*GetResponse, error)
	// ListSessionRun returns the runs of one conversation.
	ListSessionRun(ctx context.Context, req *v1.ListSessionRunQuery) (*RunsResponse, error)
	// ListEvent returns a run's events after a sequence cursor.
	ListEvent(ctx context.Context, req *v1.ListRunEventQuery) (*EventsResponse, error)
	// ListApproval returns the approvals the caller may see.
	ListApproval(ctx context.Context, req *v1.ListApprovalQuery) (*ApprovalsResponse, error)
	// Cancel stops a run.
	Cancel(ctx context.Context, req *v1.CancelRunRequest) error
	// Admit creates (or returns) the durable admission for an asynchronous run.
	Admit(ctx context.Context, req *AdmitRequest) (*model.RunAdmissionM, bool, error)
	// Execute claims and runs one admitted run.
	Execute(ctx context.Context, id string)
	// Fork copies a conversation's history into a new conversation.
	Fork(ctx context.Context, req *v1.ForkSessionRequest) (*ForkResponse, error)
	// ListRevision returns the revisions an agent has executed.
	ListRevision(ctx context.Context, req *v1.ListRevisionQuery) (*RevisionsResponse, error)
	// GetRevision returns one revision.
	GetRevision(ctx context.Context, req *v1.GetRevisionParams) (*RevisionResponse, error)
	// ListAdminRuns returns a page of asynchronous run admissions for the
	// management surface. It deliberately does not apply the caller scope:
	// every route that reaches it is administrator-gated.
	ListAdminRuns(ctx context.Context, req *v1.ListAdminRunQuery) (*ListAdminRunsResponse, error)
	// ListAdminRunMonitor returns the aggregate monitoring view over a half-open
	// time window. With req.All it covers the whole fleet and carries a
	// per-agent breakdown; otherwise it powers the per-agent monitor drawer.
	ListAdminRunMonitor(ctx context.Context, req *v1.ListAdminRunMonitorQuery) (*AdminRunMonitorResponse, error)
	// ListAdminRunStuck returns asynchronous and interactive runs that have
	// stopped making progress (stale pending, expired lease, or running past
	// the threshold). The console uses it on the observability failures page.
	ListAdminRunStuck(ctx context.Context, req *v1.ListAdminRunStuckQuery) (*AdminRunStuckResponse, error)
	// GetAdminRun returns one admission with its run row and request payload.
	GetAdminRun(ctx context.Context, req *v1.GetAdminRunRequest) (*GetAdminRunResponse, error)
	// ListAdminRunEvents returns the events of one admitted run.
	ListAdminRunEvents(ctx context.Context, req *v1.ListAdminRunEventQuery) (*AdminRunEventsResponse, error)
	// CancelAdminRun stops an admitted run, idempotently.
	CancelAdminRun(ctx context.Context, req *v1.CancelAdminRunRequest) error
	// RetryAdminRun creates a new admission from a terminal one.
	RetryAdminRun(ctx context.Context, req *v1.RetryAdminRunRequest) (*model.RunAdmissionM, error)
}

// GetResponse is a run with its events.
type GetResponse struct {
	Run    *model.RunM        `json:"run"`
	Events []*model.RunEventM `json:"events"`
}

// RunsResponse is a page of runs.
type RunsResponse struct {
	Runs []*model.RunM `json:"runs"`
}

// EventsResponse is a page of run events.
type EventsResponse struct {
	Events []*model.RunEventM `json:"events"`
}

// ApprovalsResponse is a page of approvals.
type ApprovalsResponse struct {
	Approvals []*model.ApprovalM `json:"approvals"`
}

// RevisionsResponse is a page of agent revisions.
type RevisionsResponse struct {
	Revisions []*model.RevisionM `json:"revisions"`
}

// RevisionResponse is one agent revision.
type RevisionResponse struct {
	Revision *model.RevisionM `json:"revision"`
}

// ForkResponse is the new conversation and how many messages it copied.
type ForkResponse struct {
	Session  *model.SessionM `json:"session"`
	Messages int             `json:"messages"`
}

type runBiz struct {
	store runstore.Store
	agent agentapi.IAgentBiz
}

var _ RunBiz = (*runBiz)(nil)

// New creates a RunBiz. A nil store makes every read refuse with "not
// configured"; a nil agent makes Cancel refuse the same way.
func New(store runstore.Store, agent agentapi.IAgentBiz) *runBiz {
	return &runBiz{store: store, agent: agent}
}

// Event page sizes. A run may produce thousands of events over a long turn;
// Get is the "resume a conversation" read and must stay bounded, while the
// cursor endpoint is how a caller pages through the rest.
const (
	maxGetRunEvents  = 200
	maxListRunEvents = 500
)

// Get implements RunBiz.
func (b *runBiz) Get(ctx context.Context, req *v1.GetRunRequest) (*GetResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	row, err := b.store.GetRun(ctx, req.GetId())
	if err != nil {
		// Only a missing run row can be explained by an admission: an admitted
		// run that has not started yet has no run row, so the submitter can
		// poll without a spurious 404. Any other storage error is a storage
		// error and must not be disguised as an admission.
		if errors.Is(err, store.ErrNotFound) {
			if admission, aerr := b.store.GetAdmission(ctx, req.GetId()); aerr == nil {
				return &GetResponse{Run: admissionAsRun(admission)}, nil
			}
		}
		return nil, runStoreError(err)
	}
	if err := b.requireRunOwner(ctx, row.SessionID); err != nil {
		return nil, err
	}
	events, err := b.store.ListEvent(ctx, row.ID, 0, maxGetRunEvents)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &GetResponse{Run: row, Events: events}, nil
}

// ListSessionRun implements RunBiz.
func (b *runBiz) ListSessionRun(
	ctx context.Context, req *v1.ListSessionRunQuery,
) (*RunsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	if err := b.requireSessionOwner(ctx, req.ID); err != nil {
		return nil, err
	}
	runs, err := b.store.ListRun(ctx, req.ID, req.Limit)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &RunsResponse{Runs: runs}, nil
}

// ListEvent implements RunBiz.
func (b *runBiz) ListEvent(
	ctx context.Context, req *v1.ListRunEventQuery,
) (*EventsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	row, err := b.store.GetRun(ctx, req.ID)
	if err != nil {
		return nil, runStoreError(err)
	}
	if err := b.requireRunOwner(ctx, row.SessionID); err != nil {
		return nil, err
	}
	events, err := b.store.ListEvent(ctx, row.ID, req.After, maxListRunEvents)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &EventsResponse{Events: events}, nil
}

// ListApproval implements RunBiz.
func (b *runBiz) ListApproval(
	ctx context.Context, req *v1.ListApprovalQuery,
) (*ApprovalsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	approvals, err := b.store.ListApproval(ctx, req.Status, caller.SubjectFilter(ctx), req.Limit)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &ApprovalsResponse{Approvals: approvals}, nil
}

// Cancel implements RunBiz.
func (b *runBiz) Cancel(ctx context.Context, req *v1.CancelRunRequest) error {
	if b.store != nil {
		row, err := b.store.GetRun(ctx, req.GetId())
		switch {
		case err == nil:
			if err := b.requireRunOwner(ctx, row.SessionID); err != nil {
				return err
			}
		case errors.Is(err, store.ErrNotFound):
			if _, aerr := b.store.GetAdmission(ctx, req.GetId()); aerr == nil {
				// Admitted but not started: there is no engine run to stop.
				// Cancellation is not fenced on a lease, because the user is
				// canceling the run rather than a particular attempt of it.
				return b.store.CancelAdmission(ctx, req.GetId(), "canceled before start")
			}
		}
	}
	if b.agent == nil {
		return errno.ErrOperationFailed.WithMessage("the agent engine is not configured")
	}
	if err := b.agent.Stop(ctx, req.GetId()); err != nil {
		return err
	}
	if b.store != nil {
		if err := b.store.CancelAdmission(ctx, req.GetId(), "canceled"); err != nil {
			return errno.ErrOperationFailed.WithMessage("%s", err.Error())
		}
	}
	return nil
}

// Fork implements RunBiz.
//
// The fork copies history up to a message sequence, not a live process: the new
// session runs the same agent and revision but has its own identity, so the two
// conversations cannot affect each other.
func (b *runBiz) Fork(
	ctx context.Context, req *v1.ForkSessionRequest,
) (*ForkResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	source, err := b.store.GetSession(ctx, req.GetId())
	if err != nil {
		return nil, runStoreError(err)
	}
	// A fork copies the whole history into a conversation the caller owns, so a
	// caller who could fork someone else's would end up owning a copy of it.
	if !caller.Owns(ctx, source.Owner) {
		return nil, errno.ErrNotFound.WithMessage("session not found")
	}
	messages, err := b.store.ListMessage(ctx, source.ID)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	now := time.Now().UnixMilli()
	forked := &model.SessionM{
		ID:             uuid.NewString(),
		Namespace:      source.Namespace,
		Agent:          source.Agent,
		RuntimeKind:    source.RuntimeKind,
		RuntimeProfile: source.RuntimeProfile,
		RevisionID:     source.RevisionID,
		Owner:          source.Owner,
		Name:           req.GetName(),
		State:          model.SessionStateReady,
		LastActiveAt:   now,
		CreatedAt:      now,
	}
	if err := b.store.CreateSession(ctx, forked); err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	copied := make([]*model.SessionMessageM, 0, len(messages))
	for _, message := range messages {
		if req.GetUpToSeq() > 0 && message.Seq > req.GetUpToSeq() {
			break
		}
		copied = append(copied, &model.SessionMessageM{
			Role:      message.Role,
			Content:   message.Content,
			Images:    message.Images,
			CreatedAt: message.CreatedAt,
		})
	}
	if err := b.store.ReplaceMessages(ctx, forked.ID, copied); err != nil {
		_ = b.store.MarkSessionDeleted(ctx, forked.ID, time.Now().UnixMilli())
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &ForkResponse{Session: forked, Messages: len(copied)}, nil
}

// ListRevision implements RunBiz.
//
// A revision is compiled when a run first needs it, so this answers "which
// versions of this agent have actually executed". An agent with no revisions has
// never run — it does not mean the agent is broken.
func (b *runBiz) ListRevision(
	ctx context.Context, req *v1.ListRevisionQuery,
) (*RevisionsResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	revisions, err := b.store.ListRevision(ctx, req.Agent, req.Limit)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &RevisionsResponse{Revisions: revisions}, nil
}

// GetRevision implements RunBiz.
func (b *runBiz) GetRevision(
	ctx context.Context, req *v1.GetRevisionParams,
) (*RevisionResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	revision, err := b.store.GetRevision(ctx, req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errno.ErrNotFound.WithMessage("revision not found")
		}
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &RevisionResponse{Revision: revision}, nil
}

// requireSessionOwner resolves a conversation and answers "not found" unless the
// caller owns it. A missing conversation answers the same way.
func (b *runBiz) requireSessionOwner(ctx context.Context, id string) error {
	session, err := b.store.GetSession(ctx, id)
	if err != nil {
		return runStoreError(err)
	}
	if !caller.Owns(ctx, session.Owner) {
		return errno.ErrNotFound.WithMessage("session not found")
	}
	return nil
}

// requireRunOwner resolves a run's conversation and answers "not found" unless
// the caller owns it. A run whose conversation is gone is refused the same way:
// an orphaned run has no owner to check against, and reading it would be reading
// a conversation the caller cannot otherwise see.
func (b *runBiz) requireRunOwner(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return b.requireSessionOwner(ctx, sessionID)
}

func runStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errno.ErrNotFound.WithMessage("run not found")
	}
	return errno.ErrOperationFailed.WithMessage("%s", err.Error())
}
