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

// Package escalation executes a group's escalation policy against tickets.
//
// Onexai has no alert/incident source, so acknowledgement is inferred from the
// ticket lifecycle: a ticket moving to processing or saved stops the escalation,
// and one moving to resolved or closed completes it. Manual escalation works
// without the worker; the worker is opt-in because it pages people.
package escalation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/oncall"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/chat/userdir"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// Notifier delivers one page. userID is the platform identity resolved from the
// on-call name map.
type Notifier interface {
	Notify(ctx context.Context, userID, userName, subject, body string) error
}

// Engine owns the escalation state machine.
type Engine struct {
	st            store.IStore
	notifier      Notifier
	resolve       userdir.Resolver
	interval      time.Duration
	workerEnabled bool
	now           func() time.Time
}

// New builds an engine. interval is the worker tick; manual escalation does not
// use it. workerEnabled gates automatic starts only: a deployment with the
// worker off can still escalate a ticket by hand.
func New(
	st store.IStore, notifier Notifier, resolve userdir.Resolver,
	interval time.Duration, workerEnabled bool,
) *Engine {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Engine{
		st:            st,
		notifier:      notifier,
		resolve:       resolve,
		interval:      interval,
		workerEnabled: workerEnabled,
		now:           time.Now,
	}
}

// larkUserID resolves an on-call name to the Lark user_id paging uses. An
// unresolved name returns "", which notifyTier reports as an error rather than
// silently skipping the person.
func (e *Engine) larkUserID(ctx context.Context, name string) string {
	if e == nil || e.resolve == nil {
		return ""
	}
	return e.resolve.LarkUserID(ctx, name)
}

// Start creates a pending escalation for a ticket when its group's policy asks
// for it. It is idempotent and never fails ticket creation: a missing policy or
// a duplicate state is simply no work.
func (e *Engine) Start(ctx context.Context, ticket *model.TicketM) {
	if e == nil || !e.workerEnabled || ticket == nil || strings.TrimSpace(ticket.GroupID) == "" {
		return
	}
	agg, err := e.st.OncallSchedules().GetAggregate(ctx, ticket.GroupID)
	if err != nil || agg == nil || agg.Schedule == nil {
		return
	}
	if !agg.Schedule.EscalationEnabled || !agg.Schedule.EscalationAuto {
		return
	}
	if agg.Schedule.EscalationFromLevel > 0 && ticket.Level > agg.Schedule.EscalationFromLevel {
		return
	}
	existing, err := e.st.TicketEscalation().GetByTicket(ctx, ticket.TicketID)
	if err != nil || existing != nil {
		return
	}
	now := e.now().UnixMilli()
	state := &model.TicketEscalationM{
		TicketID:      ticket.TicketID,
		GroupID:       ticket.GroupID,
		TierIndex:     0,
		Status:        model.EscalationPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := e.st.TicketEscalation().Create(ctx, state); err != nil {
		slog.WarnContext(ctx, "escalation: start", "ticket", ticket.TicketID, "error", err)
	}
}

// EscalateNow is the manual path: it starts at the first tier for a new state,
// advances one tier for an active one, and completes a policy that is already
// exhausted.
func (e *Engine) EscalateNow(ctx context.Context, group, ticketID string) (*api.EscalateResponse, error) {
	if e == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("escalation is not configured")
	}
	agg, err := e.st.OncallSchedules().GetAggregate(ctx, group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if agg == nil || agg.Schedule == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("group %q has no native schedule", group)
	}
	if !agg.Schedule.EscalationEnabled {
		return nil, errno.ErrInvalidArgument.WithMessage("group %q has no enabled escalation policy", group)
	}
	if len(agg.Tiers) == 0 {
		return nil, errno.ErrInvalidArgument.WithMessage("group %q has no escalation tiers", group)
	}
	ticket, err := e.st.Ticket().Get(ctx, where.F("ticket_id", ticketID))
	if err := storeerr.GetError(ticket, err, "ticket %q not found", ticketID); err != nil {
		return nil, err
	}
	if ticket.GroupID != "" && ticket.GroupID != group {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"ticket %q belongs to group %q, not %q", ticketID, ticket.GroupID, group)
	}

	now := e.now().UTC()
	state, err := e.st.TicketEscalation().GetByTicket(ctx, ticketID)
	if err != nil {
		return nil, storeerr.Read(err)
	}

	tierIndex := int64(0)
	if state != nil && state.Status != model.EscalationAcknowledged && state.Status != model.EscalationCompleted {
		// A state still waiting for its first notification is retried at the
		// same tier; an already-notified state advances one. This is what makes
		// a manual "升级" mean "the next step" rather than "skip the step the
		// worker never got to".
		if state.Status == model.EscalationPending && state.LastNotifiedAt == 0 {
			tierIndex = state.TierIndex
		} else {
			tierIndex = state.TierIndex + 1
		}
	}

	if state == nil {
		state = &model.TicketEscalationM{
			TicketID:  ticketID,
			GroupID:   group,
			Status:    model.EscalationPending,
			CreatedAt: now.UnixMilli(),
		}
	}

	for tierIndex < int64(len(agg.Tiers)) {
		tier := agg.Tiers[tierIndex]
		targets, notifyErr := e.notifyTier(ctx, agg, ticket, tierIndex, now)
		state.TierIndex = tierIndex
		state.UpdatedAt = now.UnixMilli()

		if notifyErr != nil {
			state.LastError = notifyErr.Error()
			state.Attempts++
			state.Status = model.EscalationPending
			state.NextAttemptAt = now.Add(staleAfter(len(targets), e.interval)).UnixMilli()
			if err := e.upsertState(ctx, state); err != nil {
				return nil, storeerr.Write(err)
			}
			return &api.EscalateResponse{
				TicketID: ticketID, Group: group, TierIndex: tierIndex,
				Status: state.Status, Targets: targets,
			}, nil
		}
		if len(targets) == 0 {
			tierIndex++
			continue
		}
		state.LastError = ""
		state.Status = model.EscalationNotified
		state.LastNotifiedAt = now.UnixMilli()
		state.NextAttemptAt = now.Add(timeoutOf(tier)).UnixMilli()
		if err := e.upsertState(ctx, state); err != nil {
			return nil, storeerr.Write(err)
		}
		return &api.EscalateResponse{
			TicketID: ticketID, Group: group, TierIndex: tierIndex,
			Status: state.Status, Targets: targets,
		}, nil
	}

	state.Status = model.EscalationCompleted
	state.UpdatedAt = now.UnixMilli()
	if err := e.upsertState(ctx, state); err != nil {
		return nil, storeerr.Write(err)
	}
	return &api.EscalateResponse{
		TicketID: ticketID, Group: group, TierIndex: tierIndex, Status: model.EscalationCompleted,
	}, nil
}

// Run is the worker loop. It is started only when the deployment opts in.
func (e *Engine) Run(ctx context.Context) {
	if e == nil {
		return
	}
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.tick(ctx); err != nil {
				slog.WarnContext(ctx, "escalation: worker tick", "error", err)
			}
		}
	}
}

func (e *Engine) tick(ctx context.Context) error {
	now := e.now().UTC()
	rows, err := e.st.TicketEscalation().ListDue(ctx, now.UnixMilli(), 50)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		// The conditional update is what makes multiple replicas safe: the loser
		// does not process the same row.
		claimed, err := e.st.TicketEscalation().ClaimDue(
			ctx, row.ID, now.UnixMilli(), now.Add(e.interval).UnixMilli())
		if err != nil || !claimed {
			continue
		}
		if err := e.advance(ctx, row, now); err != nil {
			slog.WarnContext(ctx, "escalation: advance", "ticket", row.TicketID, "error", err)
		}
	}
	return nil
}

func (e *Engine) advance(ctx context.Context, state *model.TicketEscalationM, now time.Time) error {
	ticket, err := e.st.Ticket().Get(ctx, where.F("ticket_id", state.TicketID))
	if err != nil || ticket == nil {
		state.Status = model.EscalationCompleted
		state.UpdatedAt = now.UnixMilli()
		return e.st.TicketEscalation().Update(ctx, state)
	}
	switch ticket.Status {
	case model.TicketStatusProcessing, model.TicketStatusSaved:
		state.Status = model.EscalationAcknowledged
		state.AcknowledgedAt = now.UnixMilli()
		state.UpdatedAt = now.UnixMilli()
		return e.st.TicketEscalation().Update(ctx, state)
	case model.TicketStatusResolved, model.TicketStatusClosed:
		state.Status = model.EscalationCompleted
		state.UpdatedAt = now.UnixMilli()
		return e.st.TicketEscalation().Update(ctx, state)
	}

	agg, err := e.st.OncallSchedules().GetAggregate(ctx, state.GroupID)
	if err != nil || agg == nil || agg.Schedule == nil || !agg.Schedule.EscalationEnabled || len(agg.Tiers) == 0 {
		state.Status = model.EscalationCompleted
		state.UpdatedAt = now.UnixMilli()
		return e.st.TicketEscalation().Update(ctx, state)
	}

	tierIndex := state.TierIndex
	if state.Status == model.EscalationNotified {
		tier := agg.Tiers[tierIndex]
		if now.UnixMilli() < state.LastNotifiedAt+timeoutOf(tier).Milliseconds() {
			state.NextAttemptAt = now.Add(timeoutOf(tier)).UnixMilli()
			state.UpdatedAt = now.UnixMilli()
			return e.st.TicketEscalation().Update(ctx, state)
		}
		tierIndex++
	}

	// A tier with no resolvable target (empty schedule, nobody on call) is
	// skipped immediately, the same way Regen advances through an empty step
	// instead of waiting its timeout for a notification that was never sent.
	for tierIndex < int64(len(agg.Tiers)) {
		tier := agg.Tiers[tierIndex]
		targets, notifyErr := e.notifyTier(ctx, agg, ticket, tierIndex, now)
		state.TierIndex = tierIndex
		state.UpdatedAt = now.UnixMilli()

		if notifyErr != nil {
			state.LastError = notifyErr.Error()
			state.Attempts++
			state.Status = model.EscalationPending
			state.NextAttemptAt = now.Add(staleAfter(len(targets), e.interval)).UnixMilli()
			return e.st.TicketEscalation().Update(ctx, state)
		}
		if len(targets) == 0 {
			tierIndex++
			continue
		}
		state.LastError = ""
		state.Status = model.EscalationNotified
		state.LastNotifiedAt = now.UnixMilli()
		state.NextAttemptAt = now.Add(timeoutOf(tier)).UnixMilli()
		return e.st.TicketEscalation().Update(ctx, state)
	}

	state.Status = model.EscalationCompleted
	state.UpdatedAt = now.UnixMilli()
	return e.st.TicketEscalation().Update(ctx, state)
}

func (e *Engine) upsertState(ctx context.Context, state *model.TicketEscalationM) error {
	if state.ID != 0 {
		return e.st.TicketEscalation().Update(ctx, state)
	}
	return e.st.TicketEscalation().Create(ctx, state)
}

func (e *Engine) notifyTier(
	ctx context.Context,
	agg *model.OncallScheduleAggregate,
	ticket *model.TicketM,
	tierIndex int64,
	now time.Time,
) ([]api.EscalationTarget, error) {
	tier := agg.Tiers[tierIndex]
	targets := e.resolveTargets(ctx, agg, tier, now)
	if len(targets) == 0 {
		return nil, nil
	}

	subject := fmt.Sprintf("[值班升级] 工单 %s 已升级到第 %d 级", ticket.TicketID, tierIndex+1)
	body := fmt.Sprintf("工单：%s\n标题：%s\n值班组：%s\n级别：P%d",
		ticket.TicketID, ticket.Title, ticket.GroupID, ticket.Level)

	var failures []string
	var successes []string
	for _, target := range targets {
		if target.UserID == "" {
			failures = append(failures, target.UserName+"（未配置飞书 user_id）")
			continue
		}
		if e.notifier == nil {
			failures = append(failures, target.UserName+"（未配置通知通道）")
			continue
		}
		if err := e.notifier.Notify(ctx, target.UserID, target.UserName, subject, body); err != nil {
			failures = append(failures, target.UserName+"（"+err.Error()+"）")
			continue
		}
		successes = append(successes, target.UserName)
	}

	note := fmt.Sprintf("升级到第 %d 级。通知成功：%s", tierIndex+1, joinOrDash(successes))
	if len(failures) > 0 {
		note += "；失败：" + strings.Join(failures, "、")
	}
	_ = e.st.TicketNote().Create(ctx, &model.TicketNoteM{
		TicketID:   ticket.TicketID,
		AuthorID:   "system",
		AuthorName: "值班升级",
		Note:       note,
		CreatedAt:  now,
	})

	if len(successes) == 0 {
		return targets, fmt.Errorf("no target could be notified: %s", strings.Join(failures, "、"))
	}
	return targets, nil
}

func (e *Engine) resolveTargets(
	ctx context.Context,
	agg *model.OncallScheduleAggregate,
	tier *model.OncallEscalationTierM,
	now time.Time,
) []api.EscalationTarget {
	out := make([]api.EscalationTarget, 0, len(tier.UserList())+1)
	seen := make(map[string]struct{})

	add := func(name, userID, source string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, api.EscalationTarget{UserName: name, UserID: userID, Source: source})
	}

	if tier.TargetType == model.EscalationTargetSchedule || tier.TargetType == model.EscalationTargetBoth {
		targetGroup := strings.TrimSpace(tier.TargetGroupName)
		if targetGroup == "" {
			targetGroup = agg.Schedule.GroupName
		}
		targetAgg := agg
		if targetGroup != agg.Schedule.GroupName {
			other, err := e.st.OncallSchedules().GetAggregate(ctx, targetGroup)
			if err == nil && other != nil && other.Schedule != nil {
				targetAgg = other
			} else {
				targetAgg = nil
			}
		}
		if targetAgg != nil {
			if current, ok := oncall.CurrentAt(targetAgg, now); ok {
				add(current.UserName, e.larkUserID(ctx, current.UserName), "schedule")
			}
		}
	}

	if tier.TargetType == model.EscalationTargetUsers || tier.TargetType == model.EscalationTargetBoth {
		for _, name := range tier.UserList() {
			add(name, e.larkUserID(ctx, name), "users")
		}
	}
	return out
}

func timeoutOf(tier *model.OncallEscalationTierM) time.Duration {
	if tier == nil {
		return 5 * time.Minute
	}
	if tier.TimeoutSeconds <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(tier.TimeoutSeconds) * time.Second
}

func staleAfter(_ int, interval time.Duration) time.Duration {
	if interval < time.Minute {
		return time.Minute
	}
	return interval
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, "、")
}
