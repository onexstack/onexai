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

// Package dissolve closes the loop on a promise this service already makes.
//
// When a duty ticket is closed, the bot tells the group:
//
//	为避免不必要的打扰，本群将在 1 天后自动解散。
//	(To avoid unnecessary disruption, this group will be dissolved automatically in one day.)
//
// Until this package existed, nothing dissolved anything. The message was sent,
// the group stayed, and the only thing that happened after a day was that
// nothing happened — which is worse than not making the promise, because people
// plan around it.
//
// # The order is the whole design
//
// Lark's dissolve call does not keep the group's history: its documentation
// says so, and the messages stop being reachable from the platform as soon as
// it returns. So the archive is a precondition, not a follow-up:
//
//	archive → dissolve → mark
//
// A failure at any step leaves the group alone to be retried, and a group left
// standing is a far smaller harm than a conversation nobody can read again.
// That is also why the mark comes last: a pass interrupted between the two
// finishes the work on its next run, because the archive is idempotent.
package dissolve

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
)

// DefaultGraceAfterClose is how long a closed group is left alone.
//
// A day, because that is the number the bot already promised the group. It is
// the only place the promise is written down other than the message itself; a
// change here changes what people were told, so it is configuration rather than
// a constant — see Options.
const DefaultGraceAfterClose = 24 * time.Hour

// DefaultInterval is how often a pass runs.
//
// A minute, matching the neighbouring sweeps (the peer-bind claims, the ticket
// claims). The work is one indexed query and a handful of rows, and the thing
// being measured is a day: an hour of jitter on a day is not worth a different
// schedule, and a minute is small enough that an operator restarting the
// service does not wonder whether the sweep is running.
const DefaultInterval = time.Minute

// DefaultBatch bounds one pass.
const DefaultBatch = 50

// Lark is the slice of the IM backend this sweep uses.
type Lark interface {
	// GetHistory returns a conversation's recent messages, oldest-first.
	GetHistory(ctx context.Context, convID string, limit int) ([]*im.Message, error)
	// DissolveChat dissolves a group. It is irreversible and takes the group's
	// history with it.
	DissolveChat(ctx context.Context, chatID string) error
}

// Options configures the sweep.
type Options struct {
	// Enabled turns the sweep on. Off by default: dissolving a group cannot be
	// undone, and a deployment should opt into that rather than discover it.
	Enabled bool
	// GraceAfterClose is how long after a group was closed it may be dissolved.
	// Zero uses DefaultGraceAfterClose.
	GraceAfterClose time.Duration
	// Interval is how often a pass runs. Zero uses DefaultInterval.
	Interval time.Duration
	// Batch bounds one pass. Zero uses DefaultBatch.
	Batch int
	// HistoryLimit bounds how many messages are archived per group. Zero uses a
	// package default.
	HistoryLimit int
}

// Sweeper runs the dissolution passes until its context is done.
//
// It is an interface rather than the concrete type so a disabled deployment
// holds a nil interface rather than a typed nil: the caller starts it
// unconditionally, and a typed nil in an interface would be a non-nil value
// whose method panics on the first call.
type Sweeper interface {
	Run(ctx context.Context)
	// SweepOnce runs a single pass and reports how many groups were dissolved.
	SweepOnce(ctx context.Context) (int, error)
}

type sweeper struct {
	store  store.IStore
	lark   Lark
	logger *slog.Logger
	opt    Options
}

// New builds a sweeper, or nil when it is disabled.
//
// A nil sweeper is the ordinary case for a deployment that has not opted in,
// and Run treats it as "nothing to do" rather than as an error, so the caller
// starts the loop unconditionally.
func New(st store.IStore, lark Lark, logger *slog.Logger, opt Options) Sweeper {
	if !opt.Enabled || st == nil || lark == nil {
		return nil
	}
	if opt.GraceAfterClose <= 0 {
		opt.GraceAfterClose = DefaultGraceAfterClose
	}
	if opt.Interval <= 0 {
		opt.Interval = DefaultInterval
	}
	if opt.Batch <= 0 {
		opt.Batch = DefaultBatch
	}
	if opt.HistoryLimit <= 0 {
		opt.HistoryLimit = 500
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &sweeper{store: st, lark: lark, logger: logger, opt: opt}
}

// Run sweeps until ctx is done.
func (s *sweeper) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(s.opt.Interval)
	defer ticker.Stop()

	s.logger.Info("group dissolution sweep started",
		"grace_after_close", s.opt.GraceAfterClose.String(), "interval", s.opt.Interval.String())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.SweepOnce(ctx); err != nil {
				// A failed pass is retried by the next one. Logged rather than
				// returned: this runs for the life of the process, and a caller
				// that treated one failure as final would stop sweeping for good.
				s.logger.WarnContext(ctx, "group dissolution sweep failed", "dissolved", n, "error", err)
			}
		}
	}
}

// SweepOnce runs a single pass and reports how many groups were dissolved.
//
// It is exported so an operator — or a test — can drive one pass without
// waiting for a ticker.
func (s *sweeper) SweepOnce(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	due, err := s.store.Session().ListDismissable(ctx, time.Now().Add(-s.opt.GraceAfterClose), s.opt.Batch)
	if err != nil {
		return 0, err
	}

	dissolved := 0
	for _, sess := range due {
		if ctx.Err() != nil {
			return dissolved, ctx.Err()
		}
		ok, err := s.dissolveOne(ctx, sess)
		if err != nil {
			// One group's failure must not stop the pass. The rows are
			// independent, and a group that cannot be dissolved — a bot that is
			// not its owner, say — would otherwise block every group behind it
			// for ever.
			s.logger.WarnContext(ctx, "could not dissolve a group",
				"session_id", sess.SessionID, "error", err)
			continue
		}
		if ok {
			dissolved++
		}
	}
	return dissolved, nil
}

// dissolveOne archives and dissolves one group.
//
// It reports whether the group was dissolved, which is false both for a group
// that failed and for one another replica got to first.
func (s *sweeper) dissolveOne(ctx context.Context, sess *model.SessionM) (bool, error) {
	if sess == nil || sess.SessionID == "" {
		return false, nil
	}

	// 1. Archive. Nothing after this point may run if it fails: the messages
	//    are about to become unreachable from the platform.
	archived, err := s.archive(ctx, sess.SessionID)
	if err != nil {
		return false, err
	}

	// 2. Dissolve.
	if err := s.lark.DissolveChat(ctx, sess.SessionID); err != nil {
		// The archive stands, and the next pass rewrites nothing: it is
		// idempotent by message id, so a retry costs one query and no rows.
		return false, err
	}

	// 3. Mark. Last, so an interruption above leaves the group to be finished
	//    rather than forgotten.
	if err := s.store.Session().MarkDismissed(ctx, sess.SessionID, time.Now()); err != nil {
		// The group is gone and the platform will refuse to dissolve it again,
		// so the next pass records the failure and moves on. Saying so is what
		// keeps that from looking like a sweep that never ran.
		s.logger.WarnContext(ctx, "a dissolved group could not be marked",
			"session_id", sess.SessionID, "error", err)
	}

	s.logger.InfoContext(ctx, "dissolved a closed group",
		"session_id", sess.SessionID, "archived", archived)
	return true, nil
}

// archive copies a group's messages into the archive table.
func (s *sweeper) archive(ctx context.Context, chatID string) (int, error) {
	history, err := s.lark.GetHistory(ctx, chatID, s.opt.HistoryLimit)
	if err != nil {
		return 0, err
	}
	if len(history) == 0 {
		// A group with nothing to keep is not a reason to refuse dissolving it.
		return 0, nil
	}

	rows := make([]*model.ChatMessageM, 0, len(history))
	for _, m := range history {
		if m == nil || m.ID == "" {
			continue
		}
		rows = append(rows, &model.ChatMessageM{
			MessageID:  m.ID,
			GroupID:    chatID,
			SenderID:   m.SenderID,
			SenderName: m.SenderName,
			Content:    m.Content.Text,
			Time:       timePtr(m.CreateTime),
			ParentID:   m.ParentID,
		})
	}
	return s.store.ChatMessages().Archive(ctx, rows)
}

// timePtr returns a pointer to t, or nil for the zero time.
//
// The column is nullable and the distinction is the same one every other
// optional time in this schema makes: a message with no time is one whose time
// the platform did not report, not one from the year zero.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
