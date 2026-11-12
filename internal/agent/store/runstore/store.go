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

// Package runstore is the single durable write path for sessions, revisions,
// runs and semantic run events. The in-process eino relay and the A2A
// TaskStore service are both adapters over this interface; nothing else writes
// these tables.
package runstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

func nowMillis() int64 { return time.Now().UnixMilli() }

// ErrVersionConflict reports that an optimistic update lost a race.
var ErrVersionConflict = errors.New("runstore: version conflict")

// ErrNoInstance reports that no runtime instance is available for a lease.
var ErrNoInstance = errors.New("runstore: no runtime instance available")

// AdmissionFilter selects a page of asynchronous run admissions.
//
// It is a plain struct rather than where.Options because the management list
// has fixed ordering and a bounded page; every filter is an exact match except
// Keyword, which matches id/subject/session/agent case-insensitively.
type AdmissionFilter struct {
	Offset      int
	Limit       int
	State       string
	Agent       string
	Trigger     string
	Source      string
	ScheduleRef string
	SessionID   string
	Keyword     string
	// From and To bound the admission's created_at (half-open [From, To)).
	// Zero means unbounded; the API requires both or neither.
	From int64
	To   int64
}

// AgentRunWindowFilter selects admissions and interactive runs in a half-open
// time window [From, To). An empty Agent means the whole fleet; the API only
// lets a caller reach that state through an explicit all=true.
type AgentRunWindowFilter struct {
	Agent     string
	From      int64
	To        int64
	ScanLimit int
}

// MonitorRunRow is the joined admission/run shape the monitor aggregates.
//
// It deliberately leaves out request_json, message and error: a seven-day
// window can hold tens of thousands of rows, and the prompt payload is the
// largest column in the table. The monitor needs counts, state, timing,
// source, agent and usage only.
type MonitorRunRow struct {
	ID          string `gorm:"column:id"`
	Agent       string `gorm:"column:agent"`
	Source      string `gorm:"column:source"`
	State       string `gorm:"column:state"`
	Trigger     string `gorm:"column:trigger"`
	ScheduleRef string `gorm:"column:schedule_ref"`
	CreatedAt   int64  `gorm:"column:created_at"`
	RunState    string `gorm:"column:run_state"`
	UsageJSON   string `gorm:"column:usage_json"`
	StartedAt   int64  `gorm:"column:started_at"`
	FinishedAt  int64  `gorm:"column:finished_at"`
}

// StuckRunFilter selects runs that have stopped making progress. The cutoffs
// are computed by the business layer so the thresholds stay in one place; the
// store only applies the predicates.
type StuckRunFilter struct {
	Agent         string
	Now           int64
	PendingBefore int64
	RunningBefore int64
	Limit         int
}

// StuckRunRow is one stuck run, from either side of the admission/run split.
//
// LeaseUntil distinguishes a running admission that lost its owner
// (lease_until in the past) from one that is simply taking too long. Source is
// "admission" or "run" so the console can explain why the row is visible.
type StuckRunRow struct {
	ID            string `gorm:"column:id"`
	Agent         string `gorm:"column:agent"`
	State         string `gorm:"column:state"`
	Source        string `gorm:"column:source"`
	CreatedAt     int64  `gorm:"column:created_at"`
	StartedAt     int64  `gorm:"column:started_at"`
	ScheduledTime int64  `gorm:"column:scheduled_time"`
	LeaseUntil    int64  `gorm:"column:lease_until"`
}

// Store is the durable run/session surface.
type Store interface {
	// Sessions.
	CreateSession(ctx context.Context, session *model.SessionM) error
	GetSession(ctx context.Context, id string) (*model.SessionM, error)
	UpdateSession(ctx context.Context, session *model.SessionM) error
	ListSession(ctx context.Context, owner string, limit int) ([]*model.SessionM, error)
	ListIdleSessions(ctx context.Context, before int64, limit int) ([]*model.SessionM, error)
	MarkSessionDeleted(ctx context.Context, id string, deletedAt int64) error
	// Session messages.
	AppendMessage(ctx context.Context, message *model.SessionMessageM) (int64, error)
	ListMessage(ctx context.Context, sessionID string) ([]*model.SessionMessageM, error)
	ReplaceMessages(ctx context.Context, sessionID string, messages []*model.SessionMessageM) error
	ClearMessages(ctx context.Context, sessionID string) error
	TrimOldestMessages(ctx context.Context, sessionID string, n int) (int, error)
	QueryMessages(ctx context.Context, sessionID, substr string) ([]*model.SessionMessageM, error)
	SetLastActive(ctx context.Context, sessionID string, at int64) error
	GetLastActive(ctx context.Context, sessionID string) (int64, error)
	// Shares.
	CreateShare(ctx context.Context, share *model.SessionShareM) error
	GetShareByHash(ctx context.Context, hash string) (*model.SessionShareM, error)
	ListShare(ctx context.Context, sessionID string) ([]*model.SessionShareM, error)
	RevokeShare(ctx context.Context, id, sessionID string) error
	// Revisions.
	CreateRevision(ctx context.Context, revision *model.RevisionM) error
	GetRevision(ctx context.Context, id string) (*model.RevisionM, error)
	GetRevisionByDigest(ctx context.Context, agent, digest string) (*model.RevisionM, error)
	LatestRevision(ctx context.Context, agent string) (*model.RevisionM, error)
	ListRevision(ctx context.Context, agent string, limit int) ([]*model.RevisionM, error)
	// Runs.
	CreateRun(ctx context.Context, run *model.RunM) error
	GetRun(ctx context.Context, id string) (*model.RunM, error)
	UpdateRun(ctx context.Context, run *model.RunM, expectedVersion int64) error
	UpdateRunTask(ctx context.Context, runID, taskJSON string, expectedVersion int64) (int64, error)
	SettleRun(ctx context.Context, run *model.RunM, expectedVersion int64, events []*model.RunEventM) error
	ListRun(ctx context.Context, sessionID string, limit int) ([]*model.RunM, error)
	// Run admissions (the idempotent async-run queue).
	AdmitRun(ctx context.Context, admission *model.RunAdmissionM) (*model.RunAdmissionM, bool, error)
	GetAdmission(ctx context.Context, id string) (*model.RunAdmissionM, error)
	ClaimAdmission(ctx context.Context, id, owner string, ttl time.Duration, maxClaims int32) (ClaimResult, error)
	RenewAdmission(ctx context.Context, id, owner string, ttl time.Duration) (bool, error)
	FinishAdmission(ctx context.Context, id, owner, state, message string) (bool, error)
	CancelAdmission(ctx context.Context, id, message string) error
	ExhaustAdmission(ctx context.Context, id string, maxClaims int32) error
	ListPendingAdmissions(ctx context.Context, limit int) ([]*model.RunAdmissionM, error)
	// ListAdmissionPage returns one page of admissions, newest first, and the
	// total matching the same filter. It is the read side of the management
	// async-task list.
	ListAdmissionPage(ctx context.Context, filter AdmissionFilter) (int64, []*model.RunAdmissionM, error)
	// ListRunsByIDs loads the run rows for a set of admission ids in one query.
	// A pending admission has no run row, so the caller must tolerate gaps.
	ListRunsByIDs(ctx context.Context, ids []string) ([]*model.RunM, error)
	// ListAgentRunWindow returns joined admission/run rows in [From, To),
	// newest first, capped at ScanLimit. An empty Agent means the whole fleet.
	// It is the single read the monitoring aggregate performs; a pending
	// admission has no run row, so the run half of the result may be empty.
	ListAgentRunWindow(ctx context.Context, filter AgentRunWindowFilter) ([]MonitorRunRow, error)
	// ListStuckRuns returns asynchronous admissions and interactive runs that
	// have stopped making progress, oldest first. The caller supplies the
	// cutoffs and is responsible for merging the two sources into one page.
	ListStuckRuns(ctx context.Context, filter StuckRunFilter) ([]StuckRunRow, error)
	// Approvals.
	CreateApprovals(ctx context.Context, approvals []*model.ApprovalM) error
	ListApproval(ctx context.Context, status, subject string, limit int) ([]*model.ApprovalM, error)
	DecideApproval(ctx context.Context, runID, requestID, status, reason string) error
	ExpireApprovals(ctx context.Context, now int64) (int64, error)
	// Events.
	AppendEvents(ctx context.Context, runID string, events []*model.RunEventM) error
	ListEvent(ctx context.Context, runID string, afterSeq int64, limit int) ([]*model.RunEventM, error)
	// Runtime instances.
	CreateInstance(ctx context.Context, instance *model.RuntimeInstanceM) error
	LeaseInstance(ctx context.Context, profile, agent string, leaseUntil int64) (*model.RuntimeInstanceM, error)
	ReleaseInstance(ctx context.Context, id string) error
	ListInstance(ctx context.Context, profile string) ([]*model.RuntimeInstanceM, error)
}

// SQLStore is the GORM-backed Store.
type SQLStore struct {
	db *gorm.DB
}

var _ Store = (*SQLStore)(nil)

// New returns a store over db.
func New(db *gorm.DB) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) CreateSession(ctx context.Context, session *model.SessionM) error {
	return s.db.WithContext(ctx).Create(session).Error
}

func (s *SQLStore) GetSession(ctx context.Context, id string) (*model.SessionM, error) {
	var session model.SessionM
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SQLStore) UpdateSession(ctx context.Context, session *model.SessionM) error {
	return s.db.WithContext(ctx).Save(session).Error
}

func (s *SQLStore) ListSession(ctx context.Context, owner string, limit int) ([]*model.SessionM, error) {
	db := s.db.WithContext(ctx).Order("last_active_at DESC")
	if owner != "" {
		db = db.Where("owner = ?", owner)
	}
	if limit > 0 {
		db = db.Limit(limit)
	}
	var sessions []*model.SessionM
	return sessions, db.Find(&sessions).Error
}

func (s *SQLStore) ListIdleSessions(ctx context.Context, before int64, limit int) ([]*model.SessionM, error) {
	if limit <= 0 {
		limit = 100
	}
	var sessions []*model.SessionM
	return sessions, s.db.WithContext(ctx).
		Where("state = ? AND deleted_at = 0 AND last_active_at > 0 AND last_active_at < ?", model.SessionStateReady, before).
		Order("last_active_at ASC").Limit(limit).Find(&sessions).Error
}

func (s *SQLStore) MarkSessionDeleted(ctx context.Context, id string, deletedAt int64) error {
	return s.db.WithContext(ctx).Model(&model.SessionM{}).
		Where("id = ? AND deleted_at = 0", id).
		Updates(map[string]any{
			"state":            model.SessionStateDeleted,
			"deleted_at":       deletedAt,
			"runtime_instance": "",
		}).Error
}

func (s *SQLStore) AppendMessage(ctx context.Context, message *model.SessionMessageM) (int64, error) {
	var seq int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.SessionMessageM{}).Where("session_id = ?", message.SessionID).
			Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error; err != nil {
			return err
		}
		message.Seq = seq + 1
		seq = message.Seq
		return tx.Create(message).Error
	})
	return seq, err
}

func (s *SQLStore) ListMessage(ctx context.Context, sessionID string) ([]*model.SessionMessageM, error) {
	var messages []*model.SessionMessageM
	return messages, s.db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("seq ASC").Find(&messages).Error
}

func (s *SQLStore) ReplaceMessages(ctx context.Context, sessionID string, messages []*model.SessionMessageM) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", sessionID).Delete(&model.SessionMessageM{}).Error; err != nil {
			return err
		}
		for i := range messages {
			messages[i].SessionID = sessionID
			messages[i].Seq = int64(i + 1)
		}
		if len(messages) == 0 {
			return nil
		}
		return tx.Create(messages).Error
	})
}

func (s *SQLStore) ClearMessages(ctx context.Context, sessionID string) error {
	return s.db.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.SessionMessageM{}).Error
}

func (s *SQLStore) TrimOldestMessages(ctx context.Context, sessionID string, n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	var remaining int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint64
		if err := tx.Model(&model.SessionMessageM{}).Where("session_id = ?", sessionID).
			Order("seq ASC").Limit(n).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := tx.Where("id IN ?", ids).Delete(&model.SessionMessageM{}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.SessionMessageM{}).Where("session_id = ?", sessionID).Count(&remaining).Error
	})
	return int(remaining), err
}

func (s *SQLStore) QueryMessages(ctx context.Context, sessionID, substr string) ([]*model.SessionMessageM, error) {
	var messages []*model.SessionMessageM
	return messages, s.db.WithContext(ctx).
		Where("session_id = ? AND LOWER(content) LIKE ?", sessionID, "%"+strings.ToLower(substr)+"%").
		Order("seq ASC").Find(&messages).Error
}

func (s *SQLStore) SetLastActive(ctx context.Context, sessionID string, at int64) error {
	return s.db.WithContext(ctx).Model(&model.SessionM{}).
		Where("id = ?", sessionID).Update("last_active_at", at).Error
}

func (s *SQLStore) GetLastActive(ctx context.Context, sessionID string) (int64, error) {
	var at int64
	err := s.db.WithContext(ctx).Model(&model.SessionM{}).
		Where("id = ?", sessionID).Select("last_active_at").Scan(&at).Error
	return at, err
}

func (s *SQLStore) CreateShare(ctx context.Context, share *model.SessionShareM) error {
	return s.db.WithContext(ctx).Create(share).Error
}

func (s *SQLStore) GetShareByHash(ctx context.Context, hash string) (*model.SessionShareM, error) {
	var share model.SessionShareM
	if err := s.db.WithContext(ctx).
		Where("token_hash = ? AND (expires_at = 0 OR expires_at > ?)", hash, nowMillis()).
		Take(&share).Error; err != nil {
		return nil, err
	}
	return &share, nil
}

func (s *SQLStore) ListShare(ctx context.Context, sessionID string) ([]*model.SessionShareM, error) {
	var shares []*model.SessionShareM
	return shares, s.db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("created_at DESC").Find(&shares).Error
}

func (s *SQLStore) RevokeShare(ctx context.Context, id, sessionID string) error {
	return s.db.WithContext(ctx).
		Where("id = ? AND session_id = ?", id, sessionID).
		Delete(&model.SessionShareM{}).Error
}

func (s *SQLStore) CreateRevision(ctx context.Context, revision *model.RevisionM) error {
	return s.db.WithContext(ctx).Create(revision).Error
}

func (s *SQLStore) ListRevision(ctx context.Context, agent string, limit int) ([]*model.RevisionM, error) {
	var rows []*model.RevisionM
	db := s.db.WithContext(ctx).Order("created_at DESC")
	if agent != "" {
		db = db.Where("agent = ?", agent)
	}
	if limit > 0 {
		db = db.Limit(limit)
	}
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *SQLStore) GetRevision(ctx context.Context, id string) (*model.RevisionM, error) {
	var revision model.RevisionM
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&revision).Error; err != nil {
		return nil, err
	}
	return &revision, nil
}

func (s *SQLStore) GetRevisionByDigest(ctx context.Context, agent, digest string) (*model.RevisionM, error) {
	var revision model.RevisionM
	if err := s.db.WithContext(ctx).Where("agent = ? AND digest = ?", agent, digest).Take(&revision).Error; err != nil {
		return nil, err
	}
	return &revision, nil
}

func (s *SQLStore) LatestRevision(ctx context.Context, agent string) (*model.RevisionM, error) {
	var revision model.RevisionM
	if err := s.db.WithContext(ctx).Where("agent = ?", agent).Order("created_at DESC").Take(&revision).Error; err != nil {
		return nil, err
	}
	return &revision, nil
}

func (s *SQLStore) CreateRun(ctx context.Context, run *model.RunM) error {
	if run.Version == 0 {
		run.Version = 1
	}
	return s.db.WithContext(ctx).Create(run).Error
}

func (s *SQLStore) GetRun(ctx context.Context, id string) (*model.RunM, error) {
	var run model.RunM
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// AdmitRun creates an admission, or returns the one already stored for the
// dedup key. The bool reports whether this call created the row.
func (s *SQLStore) AdmitRun(
	ctx context.Context, admission *model.RunAdmissionM,
) (*model.RunAdmissionM, bool, error) {
	if admission.DedupKey != "" {
		existing, err := s.GetAdmissionByDedupKey(ctx, admission.DedupKey)
		switch {
		case err == nil:
			return existing, false, nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return nil, false, err
		}
	}
	if admission.CreatedAt == 0 {
		admission.CreatedAt = time.Now().UnixMilli()
	}
	if err := s.db.WithContext(ctx).Create(admission).Error; err != nil {
		// A concurrent writer won the unique index; return its row.
		if admission.DedupKey != "" {
			if existing, getErr := s.GetAdmissionByDedupKey(ctx, admission.DedupKey); getErr == nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	return admission, true, nil
}

func (s *SQLStore) GetAdmissionByDedupKey(ctx context.Context, key string) (*model.RunAdmissionM, error) {
	var admission model.RunAdmissionM
	if err := s.db.WithContext(ctx).Where("dedup_key = ?", key).Take(&admission).Error; err != nil {
		return nil, err
	}
	return &admission, nil
}

func (s *SQLStore) GetAdmission(ctx context.Context, id string) (*model.RunAdmissionM, error) {
	var admission model.RunAdmissionM
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&admission).Error; err != nil {
		return nil, err
	}
	return &admission, nil
}

// ClaimResult reports what ClaimAdmission did.
type ClaimResult uint8

const (
	// ClaimNotAcquired means another owner holds a live lease, the admission
	// is already terminal, or it is not due yet.
	ClaimNotAcquired ClaimResult = iota
	// ClaimAcquired means this caller now owns the admission.
	ClaimAcquired
	// ClaimExhausted means the row hit the claim limit and can never run.
	ClaimExhausted
)

// dbNowMillis returns the database's clock in Unix milliseconds.
//
// Leases are compared across replicas, so they must be measured on one clock.
// Using the application clock would let a fast replica steal a live lease and
// let a slow one leave a dead run unrecovered.
func (s *SQLStore) dbNowMillis(ctx context.Context) (int64, error) {
	var now int64
	var err error
	switch s.db.Dialector.Name() {
	case "mysql":
		err = s.db.WithContext(ctx).
			Raw("SELECT CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)").Scan(&now).Error
	case "postgres":
		err = s.db.WithContext(ctx).
			Raw("SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000 AS BIGINT)").Scan(&now).Error
	case "sqlite":
		err = s.db.WithContext(ctx).
			Raw("SELECT CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)").Scan(&now).Error
	default:
		return 0, fmt.Errorf("runstore: unsupported dialect %q", s.db.Dialector.Name())
	}
	return now, err
}

// ClaimAdmission takes ownership of an admission atomically. A pending row is
// claimable only once its scheduled time has arrived; a running row is
// claimable only after its lease expired, which is how a crashed owner's work
// is recovered. The owner is a per-claim identity, and every later write is
// fenced on it so a zombie that lost its lease cannot overwrite the new
// owner's result.
func (s *SQLStore) ClaimAdmission(
	ctx context.Context, id, owner string, ttl time.Duration, maxClaims int32,
) (ClaimResult, error) {
	now, err := s.dbNowMillis(ctx)
	if err != nil {
		return ClaimNotAcquired, err
	}
	until := now + ttl.Milliseconds()
	result := s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Where("id = ? AND claim_count < ?", id, maxClaims).
		Where(
			"(state = ? AND scheduled_time <= ?) OR (state = ? AND lease_until <= ?)",
			model.RunStatePending, now, model.RunStateRunning, now,
		).
		Updates(map[string]any{
			"state":       model.RunStateRunning,
			"lease_owner": owner,
			"lease_until": until,
			"claim_count": gorm.Expr("claim_count + 1"),
		})
	if result.Error != nil {
		return ClaimNotAcquired, result.Error
	}
	if result.RowsAffected > 0 {
		return ClaimAcquired, nil
	}

	// Nothing was claimed: distinguish "someone else owns it" from "it can
	// never run", so the caller can terminalize an exhausted row instead of
	// sweeping it forever. Only a row that would otherwise be claimable is
	// exhausted — a live lease at the limit still belongs to its owner.
	var row model.RunAdmissionM
	if err := s.db.WithContext(ctx).Select("state", "claim_count", "scheduled_time", "lease_until").
		Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ClaimNotAcquired, nil
		}
		return ClaimNotAcquired, err
	}
	claimable := (row.State == model.RunStatePending && row.ScheduledTime <= now) ||
		(row.State == model.RunStateRunning && row.LeaseUntil <= now)
	if row.ClaimCount >= maxClaims && claimable {
		return ClaimExhausted, nil
	}
	return ClaimNotAcquired, nil
}

// RenewAdmission extends a lease this owner still holds. It returns false once
// the lease is gone: the run was canceled, or another owner reclaimed it after
// expiry.
func (s *SQLStore) RenewAdmission(
	ctx context.Context, id, owner string, ttl time.Duration,
) (bool, error) {
	now, err := s.dbNowMillis(ctx)
	if err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Where("id = ? AND lease_owner = ? AND state = ?", id, owner, model.RunStateRunning).
		Updates(map[string]any{"lease_until": now + ttl.Milliseconds()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// FinishAdmission records a terminal admission state. The write is fenced on
// the owner: a process whose lease expired cannot overwrite the result of the
// owner that replaced it. An empty owner means "no lease to check", which is
// used only by paths that never claimed the row.
func (s *SQLStore) FinishAdmission(
	ctx context.Context, id, owner, state, message string,
) (bool, error) {
	db := s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Where("id = ? AND state NOT IN (?, ?, ?)", id,
			model.RunStateSucceeded, model.RunStateFailed, model.RunStateCanceled)
	if owner != "" {
		db = db.Where("lease_owner = ?", owner)
	}
	result := db.Updates(map[string]any{
		"state":       state,
		"message":     message,
		"lease_owner": "",
		"lease_until": 0,
	})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// CancelAdmission terminalizes an admission regardless of which owner holds
// the lease. Cancellation is a user action on the run, not a claim outcome, so
// it must not be fenced on an owner. The running owner observes the lost lease
// on its next renewal and stops.
func (s *SQLStore) CancelAdmission(ctx context.Context, id, message string) error {
	return s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Where("id = ? AND state NOT IN (?, ?, ?)", id,
			model.RunStateSucceeded, model.RunStateFailed, model.RunStateCanceled).
		Updates(map[string]any{
			"state":       model.RunStateCanceled,
			"message":     message,
			"lease_owner": "",
			"lease_until": 0,
		}).Error
}

// ExhaustAdmission terminalizes an admission that has used every claim. It is
// conditional on the same "claimable" predicate as ClaimAdmission, so a row
// that is still running under a live lease is never failed underneath its
// owner.
func (s *SQLStore) ExhaustAdmission(ctx context.Context, id string, maxClaims int32) error {
	now, err := s.dbNowMillis(ctx)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Where("id = ? AND claim_count >= ?", id, maxClaims).
		Where(
			"(state = ? AND scheduled_time <= ?) OR (state = ? AND lease_until <= ?)",
			model.RunStatePending, now, model.RunStateRunning, now,
		).
		Updates(map[string]any{
			"state":       model.RunStateFailed,
			"message":     "admission attempts exhausted",
			"lease_owner": "",
			"lease_until": 0,
		}).Error
}

// ListPendingAdmissions returns admissions that are due and claimable: pending
// rows whose scheduled time has arrived, plus running rows whose owner stopped
// renewing the lease.
func (s *SQLStore) ListPendingAdmissions(ctx context.Context, limit int) ([]*model.RunAdmissionM, error) {
	now, err := s.dbNowMillis(ctx)
	if err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx).
		Where(
			"(state = ? AND scheduled_time <= ?) OR (state = ? AND lease_until <= ?)",
			model.RunStatePending, now, model.RunStateRunning, now,
		).
		Order("scheduled_time ASC, created_at ASC")
	if limit > 0 {
		db = db.Limit(limit)
	}
	var admissions []*model.RunAdmissionM
	return admissions, db.Find(&admissions).Error
}

// ListAdmissionPage returns a page of admissions and the total that matches the
// same filter. The filter is applied twice (count and page) so a page can never
// be wider than its count.
func (s *SQLStore) ListAdmissionPage(
	ctx context.Context, filter AdmissionFilter,
) (int64, []*model.RunAdmissionM, error) {
	base := func() *gorm.DB {
		db := s.db.WithContext(ctx).Model(&model.RunAdmissionM{})
		if filter.State != "" {
			db = db.Where("state = ?", filter.State)
		}
		if filter.Agent != "" {
			db = db.Where("agent = ?", filter.Agent)
		}
		if filter.Trigger != "" {
			db = db.Where("trigger = ?", filter.Trigger)
		}
		if filter.Source != "" {
			db = db.Where("source = ?", filter.Source)
		}
		if filter.ScheduleRef != "" {
			db = db.Where("schedule_ref = ?", filter.ScheduleRef)
		}
		if filter.SessionID != "" {
			db = db.Where("session_id = ?", filter.SessionID)
		}
		if filter.Keyword != "" {
			like := "%" + strings.ToLower(filter.Keyword) + "%"
			db = db.Where(
				"(LOWER(id) LIKE ? OR LOWER(subject) LIKE ? OR LOWER(session_id) LIKE ? OR LOWER(agent) LIKE ?)",
				like, like, like, like,
			)
		}
		// The window is anchored on the admission's created_at, which is the
		// same anchor the run monitor uses for asynchronous work.
		if filter.From > 0 {
			db = db.Where("created_at >= ?", filter.From)
		}
		if filter.To > 0 {
			db = db.Where("created_at < ?", filter.To)
		}
		return db
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return 0, nil, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	var rows []*model.RunAdmissionM
	err := base().
		Order("created_at DESC").
		Order("id DESC").
		Offset(filter.Offset).
		Limit(limit).
		Find(&rows).Error
	return total, rows, err
}

// ListRunsByIDs loads run rows for a set of ids in one query. Callers must
// tolerate missing rows: a pending admission has not created one yet.
func (s *SQLStore) ListRunsByIDs(ctx context.Context, ids []string) ([]*model.RunM, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var runs []*model.RunM
	return runs, s.db.WithContext(ctx).Where("id IN ?", ids).Find(&runs).Error
}

// ListAgentRunWindow returns the runs in the requested window, newest first.
//
// Two sources make up the history and both matter to the monitor:
//
//   - **Admissions** are the asynchronous queue (scheduled runs, API dispatch).
//     A pending admission has no run row yet, so it must be read from the
//     admission side to count toward the active total.
//   - **Interactive runs** are console chats and chat-apiserver turns. They are
//     never admitted: the engine records them directly in the run table, and
//     their agent is only known through the owning session. Reading admissions
//     alone made every chat invisible to the monitor.
//
// An empty filter.Agent covers the whole fleet; the validation layer only lets
// a caller reach that through an explicit all=true.
//
// The interactive branch excludes ids that already have an admission so an
// admitted run is never counted twice. Its window anchor is started_at because
// RunM.created_at is written by GORM's default auto-timestamp (seconds, not the
// milliseconds the rest of the schema uses); the recorder always writes
// started_at in milliseconds.
//
// The query is raw because it spans three tables and selects the reserved
// column name "trigger"; every identifier goes through the dialect's quoting,
// so Postgres, MySQL and SQLite all see the same meaning. Each UNION branch
// builds its own WHERE fragment and argument slice so a shared mutable slice
// cannot silently shift the placeholders.
func (s *SQLStore) ListAgentRunWindow(
	ctx context.Context, filter AgentRunWindowFilter,
) ([]MonitorRunRow, error) {
	limit := filter.ScanLimit
	if limit <= 0 {
		limit = 50001
	}

	column := func(alias, name string) string { return quoteIdentifier(s.db, alias+"."+name) }
	admissions := quoteIdentifier(s.db, model.RunAdmissionM{}.TableName())
	runs := quoteIdentifier(s.db, model.RunM{}.TableName())
	sessions := quoteIdentifier(s.db, model.SessionM{}.TableName())
	// RunM.created_at is in seconds; started_at is the millisecond anchor.
	runCreatedAt := fmt.Sprintf("CASE WHEN %s > 0 THEN %s ELSE %s * 1000 END",
		column("r", "started_at"), column("r", "started_at"), column("r", "created_at"))

	admissionWhere, admissionArgs := buildAdmissionWindowWhere(column, filter)
	interactiveWhere, interactiveArgs := buildInteractiveWindowWhere(column, filter)

	query := fmt.Sprintf(`
SELECT
  w.id, w.agent, w.source, w.state, w.trigger, w.schedule_ref, w.created_at,
  w.run_state, w.usage_json, w.started_at, w.finished_at
FROM (
  SELECT
    %s AS id,
    %s AS agent,
    'admission' AS source,
    %s AS state,
    %s AS trigger,
    %s AS schedule_ref,
    %s AS created_at,
    COALESCE(%s, '') AS run_state,
    COALESCE(%s, '') AS usage_json,
    COALESCE(%s, 0) AS started_at,
    COALESCE(%s, 0) AS finished_at
  FROM %s AS a
  LEFT JOIN %s AS r ON r.%s = a.%s
  WHERE %s

  UNION ALL

  SELECT
    %s AS id,
    %s AS agent,
    'run' AS source,
    %s AS state,
    'manual' AS trigger,
    '' AS schedule_ref,
    %s AS created_at,
    COALESCE(%s, '') AS run_state,
    COALESCE(%s, '') AS usage_json,
    COALESCE(%s, 0) AS started_at,
    COALESCE(%s, 0) AS finished_at
  FROM %s AS r
  JOIN %s AS s ON s.%s = r.%s
  WHERE %s
    AND NOT EXISTS (
      SELECT 1 FROM %s AS a2 WHERE a2.%s = r.%s
    )
) AS w
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?`,
		// Asynchronous admissions, with the run half when execution started.
		column("a", "id"), column("a", "agent"), column("a", "state"), column("a", "trigger"),
		column("a", "schedule_ref"), column("a", "created_at"),
		column("r", "state"), column("r", "usage_json"),
		column("r", "started_at"), column("r", "finished_at"),
		admissions, runs,
		quoteIdentifier(s.db, "id"), quoteIdentifier(s.db, "id"),
		admissionWhere,
		// Interactive runs: agent resolved through the owning session.
		column("r", "id"), column("s", "agent"), column("r", "state"), runCreatedAt,
		column("r", "state"), column("r", "usage_json"),
		column("r", "started_at"), column("r", "finished_at"),
		runs, sessions,
		quoteIdentifier(s.db, "id"), quoteIdentifier(s.db, "session_id"),
		interactiveWhere,
		admissions, quoteIdentifier(s.db, "id"), quoteIdentifier(s.db, "id"),
	)

	args := append(append([]any{}, admissionArgs...), interactiveArgs...)
	args = append(args, limit)

	rows := make([]MonitorRunRow, 0)
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListStuckRuns returns asynchronous admissions and interactive runs that have
// stopped making progress. The caller computes the cutoffs; the store applies
// them and returns each source oldest first, capped at filter.Limit. Merging
// and truncating to a single page is the caller's job.
func (s *SQLStore) ListStuckRuns(
	ctx context.Context, filter StuckRunFilter,
) ([]StuckRunRow, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	rows := make([]StuckRunRow, 0, limit)

	// Pending admissions: due now (or with no scheduled time) but never
	// claimed. Future scheduled work is excluded by the scheduled_time guard.
	pendingQuery := s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Select("id, agent, state, 'admission' AS source, created_at, 0 AS started_at, scheduled_time, lease_until").
		Where("state = ?", model.RunStatePending).
		Where("(scheduled_time = 0 OR scheduled_time <= ?)", filter.Now).
		Where("created_at < ?", filter.PendingBefore)
	if filter.Agent != "" {
		pendingQuery = pendingQuery.Where("agent = ?", filter.Agent)
	}
	var pending []StuckRunRow
	if err := pendingQuery.Order("created_at ASC").Limit(limit).Find(&pending).Error; err != nil {
		return nil, err
	}
	rows = append(rows, pending...)

	// Running admissions: the lease expired (no owner is renewing it) or the
	// run has simply been running past the threshold.
	runningQuery := s.db.WithContext(ctx).Model(&model.RunAdmissionM{}).
		Select("id, agent, state, 'admission' AS source, created_at, 0 AS started_at, scheduled_time, lease_until").
		Where("state = ?", model.RunStateRunning).
		Where("(created_at < ? OR (lease_until > 0 AND lease_until < ?))",
			filter.RunningBefore, filter.Now)
	if filter.Agent != "" {
		runningQuery = runningQuery.Where("agent = ?", filter.Agent)
	}
	var running []StuckRunRow
	if err := runningQuery.Order("created_at ASC").Limit(limit).Find(&running).Error; err != nil {
		return nil, err
	}
	rows = append(rows, running...)

	// Interactive runs have no admission; their agent comes from the owning
	// session. RunM.created_at is in seconds, started_at is the millisecond
	// anchor the recorder always writes.
	column := func(alias, name string) string { return quoteIdentifier(s.db, alias+"."+name) }
	admissions := quoteIdentifier(s.db, model.RunAdmissionM{}.TableName())
	runs := quoteIdentifier(s.db, model.RunM{}.TableName())
	sessions := quoteIdentifier(s.db, model.SessionM{}.TableName())
	agentClause := ""
	args := []any{model.RunStatePending, model.RunStateRunning, filter.RunningBefore}
	if filter.Agent != "" {
		agentClause = " AND " + column("s", "agent") + " = ?"
		args = append(args, filter.Agent)
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
SELECT
  %s AS id,
  %s AS agent,
  %s AS state,
  'run' AS source,
  COALESCE(%s, 0) * 1000 AS created_at,
  %s AS started_at,
  0 AS scheduled_time,
  0 AS lease_until
FROM %s AS r
JOIN %s AS s ON s.%s = r.%s
WHERE %s IN (?, ?) AND %s > 0 AND %s < ?%s
  AND NOT EXISTS (
    SELECT 1 FROM %s AS a2 WHERE a2.%s = r.%s
  )
ORDER BY %s ASC
LIMIT ?`,
		column("r", "id"), column("s", "agent"), column("r", "state"), column("r", "created_at"),
		column("r", "started_at"),
		runs, sessions,
		quoteIdentifier(s.db, "id"), quoteIdentifier(s.db, "session_id"),
		column("r", "state"), column("r", "started_at"), column("r", "started_at"), agentClause,
		admissions, quoteIdentifier(s.db, "id"), quoteIdentifier(s.db, "id"),
		column("r", "started_at"),
	)

	var interactive []StuckRunRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&interactive).Error; err != nil {
		return nil, err
	}
	rows = append(rows, interactive...)

	return rows, nil
}

// buildAdmissionWindowWhere builds the admission branch's WHERE clause and its
// arguments. The agent predicate is omitted for a fleet scan so the
// (agent, created_at) index keeps serving the per-agent path.
func buildAdmissionWindowWhere(
	column func(alias, name string) string, filter AgentRunWindowFilter,
) (string, []any) {
	where := fmt.Sprintf("%s >= ? AND %s < ?",
		column("a", "created_at"), column("a", "created_at"))
	args := []any{filter.From, filter.To}
	if filter.Agent != "" {
		where += " AND " + column("a", "agent") + " = ?"
		args = append(args, filter.Agent)
	}
	return where, args
}

// buildInteractiveWindowWhere builds the interactive branch's WHERE clause and
// its arguments.
func buildInteractiveWindowWhere(
	column func(alias, name string) string, filter AgentRunWindowFilter,
) (string, []any) {
	where := fmt.Sprintf("%s >= ? AND %s < ?",
		column("r", "started_at"), column("r", "started_at"))
	args := []any{filter.From, filter.To}
	if filter.Agent != "" {
		where += " AND " + column("s", "agent") + " = ?"
		args = append(args, filter.Agent)
	}
	return where, args
}

// quoteIdentifier quotes a schema-qualified table or table.column name with the
// active dialect's delimiter.
func quoteIdentifier(db *gorm.DB, name string) string {
	var builder strings.Builder
	db.Dialector.QuoteTo(&builder, name)
	return builder.String()
}

// UpdateRun writes a non-terminal change. The update is keyed by the expected
// version so two replicas cannot both advance the same run.
func (s *SQLStore) UpdateRun(ctx context.Context, run *model.RunM, expectedVersion int64) error {
	result := s.db.WithContext(ctx).Model(&model.RunM{}).
		Where("id = ? AND version = ?", run.ID, expectedVersion).
		Updates(map[string]any{
			"state":      run.State,
			"task_id":    run.TaskID,
			"error":      run.Error,
			"usage_json": run.UsageJSON,
			"version":    expectedVersion + 1,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	run.Version = expectedVersion + 1
	return nil
}

// UpdateRunTask writes one versioned task snapshot. It is the TaskStore's
// optimistic update; a lost response is retried with the same expected version.
func (s *SQLStore) UpdateRunTask(
	ctx context.Context, runID, taskJSON string, expectedVersion int64,
) (int64, error) {
	result := s.db.WithContext(ctx).Model(&model.RunM{}).
		Where("id = ? AND version = ?", runID, expectedVersion).
		Updates(map[string]any{"task_json": taskJSON, "version": expectedVersion + 1})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrVersionConflict
	}
	return expectedVersion + 1, nil
}

// SettleRun publishes the terminal state and its events atomically. It is the
// only write that is allowed to make a run terminal, which is what lets a
// client trust a completed run's history.
func (s *SQLStore) SettleRun(
	ctx context.Context, run *model.RunM, expectedVersion int64, events []*model.RunEventM,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RunM{}).
			Where("id = ? AND version = ?", run.ID, expectedVersion).
			Updates(map[string]any{
				"state":       run.State,
				"error":       run.Error,
				"usage_json":  run.UsageJSON,
				"task_json":   run.TaskJSON,
				"finished_at": run.FinishedAt,
				"version":     expectedVersion + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrVersionConflict
		}
		if len(events) == 0 {
			return nil
		}
		var maxSeq int64
		if err := tx.Model(&model.RunEventM{}).Where("run_id = ?", run.ID).
			Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		for i, event := range events {
			event.RunID = run.ID
			event.Seq = maxSeq + int64(i) + 1
		}
		return tx.Create(events).Error
	})
}

func (s *SQLStore) ListRun(ctx context.Context, sessionID string, limit int) ([]*model.RunM, error) {
	db := s.db.WithContext(ctx).Where("session_id = ?", sessionID).Order("started_at ASC")
	if limit > 0 {
		db = db.Limit(limit)
	}
	var runs []*model.RunM
	return runs, db.Find(&runs).Error
}

func (s *SQLStore) CreateApprovals(ctx context.Context, approvals []*model.ApprovalM) error {
	if len(approvals) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(approvals).Error
}

func (s *SQLStore) ListApproval(ctx context.Context, status, subject string, limit int) ([]*model.ApprovalM, error) {
	db := s.db.WithContext(ctx).Order("created_at DESC")
	if status != "" {
		db = db.Where("status = ?", status)
	}
	// Filtered in SQL, before the limit: filtering after the page would return
	// fewer rows than asked for and silently hide the caller's own approvals.
	if subject != "" {
		db = db.Where("subject = ?", subject)
	}
	if limit > 0 {
		db = db.Limit(limit)
	}
	var approvals []*model.ApprovalM
	return approvals, db.Find(&approvals).Error
}

func (s *SQLStore) DecideApproval(ctx context.Context, runID, requestID, status, reason string) error {
	return s.db.WithContext(ctx).Model(&model.ApprovalM{}).
		Where("run_id = ? AND request_id = ?", runID, requestID).
		Updates(map[string]any{"status": status, "reason": reason}).Error
}

func (s *SQLStore) ExpireApprovals(ctx context.Context, now int64) (int64, error) {
	result := s.db.WithContext(ctx).Model(&model.ApprovalM{}).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", "pending", now).
		Update("status", "expired")
	return result.RowsAffected, result.Error
}

func (s *SQLStore) AppendEvents(ctx context.Context, runID string, events []*model.RunEventM) error {
	if len(events) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&model.RunEventM{}).Where("run_id = ?", runID).
			Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		for i, event := range events {
			event.RunID = runID
			event.Seq = maxSeq + int64(i) + 1
		}
		return tx.Create(events).Error
	})
}

func (s *SQLStore) ListEvent(
	ctx context.Context, runID string, afterSeq int64, limit int,
) ([]*model.RunEventM, error) {
	var events []*model.RunEventM
	db := s.db.WithContext(ctx).
		Where("run_id = ? AND seq > ?", runID, afterSeq).
		Order("seq ASC")
	if limit > 0 {
		db = db.Limit(limit)
	}
	return events, db.Find(&events).Error
}

func (s *SQLStore) CreateInstance(ctx context.Context, instance *model.RuntimeInstanceM) error {
	return s.db.WithContext(ctx).Create(instance).Error
}

// LeaseInstance assigns a ready instance of profile to agent. The update is
// conditional on the instance being idle or already leased to the same agent,
// so two callers cannot take the same instance.
func (s *SQLStore) LeaseInstance(
	ctx context.Context, profile, agent string, leaseUntil int64,
) (*model.RuntimeInstanceM, error) {
	var leased *model.RuntimeInstanceM
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instances []*model.RuntimeInstanceM
		if err := tx.Where("profile = ? AND state = ? AND (agent = '' OR agent = ? OR lease_until < ?)", profile, model.InstanceStateReady, agent, nowMillis()).
			Order("last_used_at ASC").Limit(1).Find(&instances).Error; err != nil {
			return err
		}
		if len(instances) == 0 {
			return ErrNoInstance
		}
		instance := instances[0]
		result := tx.Model(&model.RuntimeInstanceM{}).
			Where("id = ? AND (agent = '' OR agent = ?)", instance.ID, agent).
			Updates(map[string]any{"agent": agent, "lease_until": leaseUntil, "last_used_at": nowMillis()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNoInstance
		}
		instance.Agent = agent
		instance.LeaseUntil = leaseUntil
		leased = instance
		return nil
	})
	if err != nil {
		return nil, err
	}
	if leased == nil {
		return nil, fmt.Errorf("runstore: lease returned no instance")
	}
	return leased, nil
}

func (s *SQLStore) ReleaseInstance(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&model.RuntimeInstanceM{}).
		Where("id = ?", id).
		Updates(map[string]any{"agent": "", "lease_until": 0, "last_used_at": nowMillis()}).Error
}

func (s *SQLStore) ListInstance(ctx context.Context, profile string) ([]*model.RuntimeInstanceM, error) {
	var instances []*model.RuntimeInstanceM
	db := s.db.WithContext(ctx)
	if profile != "" {
		db = db.Where("profile = ?", profile)
	}
	return instances, db.Order("last_used_at DESC").Find(&instances).Error
}
