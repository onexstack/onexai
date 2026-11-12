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

// Package share implements session-share grants and the anonymous reads they
// authorize.
package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/caller"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ShareBiz is the session-share business logic.
type ShareBiz interface {
	// Create mints a token and records the grant.
	Create(ctx context.Context, req *v1.CreateSessionShareParams) (*CreateResponse, error)
	// List returns the grants of one conversation.
	List(ctx context.Context, req *v1.ListSessionShareRequest) (*ListResponse, error)
	// Revoke deletes one grant.
	Revoke(ctx context.Context, req *v1.RevokeSessionShareRequest) error
	// SharedRun returns a run through a share token.
	SharedRun(ctx context.Context, id, token string) (*SharedRunResponse, error)
	// SharedRunEvents returns a run's events through a share token.
	SharedRunEvents(ctx context.Context, id string, after int64, token string) (*SharedEventsResponse, error)
	// SharedSessionRuns returns a conversation's runs through a share token.
	SharedSessionRuns(ctx context.Context, id, token string) (*SharedRunsResponse, error)
}

// ShareInfo is the public shape of a grant. SessionID is only set on the create
// answer, which is the only response that names the conversation.
type ShareInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// CreateResponse carries the new grant and the only copy of its token.
type CreateResponse struct {
	Share ShareInfo `json:"share"`
	Token string    `json:"token"`
}

// ListResponse is a conversation's grants.
type ListResponse struct {
	Shares []ShareInfo `json:"shares"`
}

// SharedRunResponse is a run with its events.
type SharedRunResponse struct {
	Run    *model.RunM        `json:"run"`
	Events []*model.RunEventM `json:"events"`
}

// SharedEventsResponse is a run's events.
type SharedEventsResponse struct {
	Events []*model.RunEventM `json:"events"`
}

// SharedRunsResponse is a conversation's runs.
type SharedRunsResponse struct {
	Runs []*model.RunM `json:"runs"`
}

type shareBiz struct {
	store runstore.Store
}

// maxSharedRunEvents bounds the events returned through one share read. A
// shared page is a conversation preview, not an export; older events are
// available by paging the cursor endpoint.
const maxSharedRunEvents = 200

var _ ShareBiz = (*shareBiz)(nil)

// New creates a ShareBiz.
func New(store runstore.Store) *shareBiz { return &shareBiz{store: store} }

// Create implements ShareBiz.
func (b *shareBiz) Create(
	ctx context.Context, req *v1.CreateSessionShareParams,
) (*CreateResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	// Default filled TTLSeconds when the caller omitted it.
	ttl := time.Duration(defaultTTL(req.TTLSeconds)) * time.Second
	session, err := b.store.GetSession(ctx, req.ID)
	if err != nil {
		return nil, runStoreError(err)
	}
	// Minting a share hands out anonymous read access to this conversation, so
	// the caller has to be the conversation's owner — otherwise any signed caller
	// could publish anyone's transcript by guessing an id.
	if !caller.Owns(ctx, session.Owner) {
		return nil, errno.ErrNotFound.WithMessage("session not found")
	}
	token, hash, err := newToken()
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("could not generate a share token")
	}
	now := time.Now()
	row := &model.SessionShareM{
		ID:        uuid.NewString(),
		SessionID: req.ID,
		TokenHash: hash,
		CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(ttl).UnixMilli(),
	}
	if err := b.store.CreateShare(ctx, row); err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &CreateResponse{
		Share: ShareInfo{
			ID:        row.ID,
			SessionID: row.SessionID,
			CreatedAt: row.CreatedAt,
			ExpiresAt: row.ExpiresAt,
		},
		Token: token,
	}, nil
}

// List implements ShareBiz.
func (b *shareBiz) List(
	ctx context.Context, req *v1.ListSessionShareRequest,
) (*ListResponse, error) {
	if err := b.requireSessionOwner(ctx, req.GetId()); err != nil {
		return nil, err
	}
	shares, err := b.store.ListShare(ctx, req.GetId())
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	out := make([]ShareInfo, 0, len(shares))
	for _, share := range shares {
		out = append(out, ShareInfo{
			ID:        share.ID,
			CreatedAt: share.CreatedAt,
			ExpiresAt: share.ExpiresAt,
		})
	}
	return &ListResponse{Shares: out}, nil
}

// Revoke implements ShareBiz.
func (b *shareBiz) Revoke(ctx context.Context, req *v1.RevokeSessionShareRequest) error {
	if err := b.requireSessionOwner(ctx, req.GetId()); err != nil {
		return err
	}
	if err := b.store.RevokeShare(ctx, req.GetShareId(), req.GetId()); err != nil {
		return errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return nil
}

// SharedRun implements ShareBiz.
func (b *shareBiz) SharedRun(ctx context.Context, id, token string) (*SharedRunResponse, error) {
	grant, err := b.authorize(ctx, token)
	if err != nil {
		return nil, err
	}
	row, err := b.store.GetRun(ctx, id)
	if err != nil {
		return nil, runStoreError(err)
	}
	if row.SessionID != grant.SessionID {
		return nil, errno.ErrPermissionDenied.WithMessage("the share does not cover this run")
	}
	events, err := b.store.ListEvent(ctx, row.ID, 0, maxSharedRunEvents)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &SharedRunResponse{Run: row, Events: events}, nil
}

// SharedRunEvents implements ShareBiz.
func (b *shareBiz) SharedRunEvents(
	ctx context.Context, id string, after int64, token string,
) (*SharedEventsResponse, error) {
	grant, err := b.authorize(ctx, token)
	if err != nil {
		return nil, err
	}
	row, err := b.store.GetRun(ctx, id)
	if err != nil {
		return nil, runStoreError(err)
	}
	if row.SessionID != grant.SessionID {
		return nil, errno.ErrPermissionDenied.WithMessage("the share does not cover this run")
	}
	events, err := b.store.ListEvent(ctx, row.ID, after, maxSharedRunEvents)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &SharedEventsResponse{Events: events}, nil
}

// SharedSessionRuns implements ShareBiz.
func (b *shareBiz) SharedSessionRuns(ctx context.Context, id, token string) (*SharedRunsResponse, error) {
	grant, err := b.authorize(ctx, token)
	if err != nil {
		return nil, err
	}
	if grant.SessionID != id {
		return nil, errno.ErrPermissionDenied.WithMessage("the share does not cover this session")
	}
	runs, err := b.store.ListRun(ctx, grant.SessionID, 0)
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return &SharedRunsResponse{Runs: runs}, nil
}

func (b *shareBiz) requireSessionOwner(ctx context.Context, id string) error {
	if b.store == nil {
		return errno.ErrNotFound.WithMessage("run store is not configured")
	}
	session, err := b.store.GetSession(ctx, id)
	if err != nil {
		return runStoreError(err)
	}
	if !caller.Owns(ctx, session.Owner) {
		return errno.ErrNotFound.WithMessage("session not found")
	}
	return nil
}

func (b *shareBiz) authorize(ctx context.Context, token string) (*model.SessionShareM, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}
	if token == "" {
		return nil, errno.ErrPermissionDenied.WithMessage("a share token is required")
	}
	grant, err := b.store.GetShareByHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errno.ErrPermissionDenied.WithMessage("the share token is invalid or expired")
		}
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	return grant, nil
}

func defaultTTL(seconds *int) int {
	if seconds == nil {
		return 7 * 24 * 60 * 60
	}
	return *seconds
}

func newToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func runStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errno.ErrNotFound.WithMessage("run not found")
	}
	return errno.ErrOperationFailed.WithMessage("%s", err.Error())
}
