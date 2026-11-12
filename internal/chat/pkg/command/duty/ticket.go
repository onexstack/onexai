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

package duty

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/id"
	"github.com/onexstack/onexstack/pkg/rid"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// The ticket id generator is built on first use and shared by the process.
var (
	ticketIDGenOnce sync.Once
	ticketIDGen     *id.Sonyflake
	ticketIDGenErr  error
)

// newTicketID generates a unique ticket id, "tkt-XXXXXX".
//
// The prefix is the one these ids have always carried. The counter is drawn
// before the insert rather than derived from the autoincrement key afterwards:
// ticket_id carries a unique index, so a post-insert hook would write an empty
// string first and two tickets created at once would collide on it.
func newTicketID(ctx context.Context) (string, error) {
	ticketIDGenOnce.Do(func() { ticketIDGen, ticketIDGenErr = id.NewSonyflake() })
	if ticketIDGenErr != nil {
		return "", fmt.Errorf("ticket id generator is unavailable: %w", ticketIDGenErr)
	}
	n, err := ticketIDGen.ID(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to generate a ticket id: %w", err)
	}
	return rid.NewResourceID("tkt").New(n), nil
}

// createTicket persists a new ticket and associates the given chat's session
// with it. The chatID is the newly created Lark group (stored as session_id).
//
// reporterName may be empty: it is the reporter's display name, which needs a
// contact lookup that can fail. The id is the part that is always known, and an
// unnamed ticket is still a ticket.
//
// reporterOpenID comes from the request's own runtime rather than from a
// lookup, so it costs nothing and is exact; see TicketM.ReporterOpenID for why
// it is stored beside the user_id instead of instead of it.
func createTicket(
	ctx context.Context,
	sr *runtime.StaticRuntime,
	chatID, groupID, title string,
	level int64,
	reporterID, reporterName, reporterOpenID string,
) (*model.TicketM, error) {
	ticketID, err := newTicketID(ctx)
	if err != nil {
		return nil, err
	}
	ticket := &model.TicketM{
		TicketID:       ticketID,
		Title:          title,
		Status:         model.TicketStatusOpen,
		Level:          level,
		GroupID:        groupID,
		ReporterID:     reporterID,
		ReporterName:   reporterName,
		ReporterOpenID: reporterOpenID,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := sr.Store.Ticket().Create(ctx, ticket); err != nil {
		return nil, fmt.Errorf("failed to persist ticket: %w", err)
	}

	// Record oncall ticket metric: split group id into parent/sub for aggregation.
	oncallGroup, subGroup := groupID, ""
	if g, ok, err := sr.Duty.GetGroupMeta(ctx, groupID); err == nil && ok && g.ParentGroupID != "" {
		oncallGroup, subGroup = g.ParentGroupID, groupID
	}
	metrics.M.RecordOncallTicketProcessed(ctx, oncallGroup, subGroup)

	// Associate the chat session with the new ticket.
	if session, err := sr.Store.Session().Get(ctx, where.F("session_id", chatID)); err == nil {
		session.AssociatedTicketID = ticket.TicketID
		session.UpdatedAt = time.Now()
		_ = sr.Store.Session().Update(ctx, session)
	}

	// A group whose policy opts in gets an escalation state. It runs after the
	// ticket exists and must never fail ticket creation.
	if sr.Escalation != nil {
		escalationCtx := context.WithoutCancel(ctx)
		runtime.GoSafe(escalationCtx, "chat.escalation.start", func() {
			sr.Escalation.Start(escalationCtx, ticket)
		})
	}
	return ticket, nil
}

// activeTicket returns the ticket associated with the current session, or an
// error if the conversation has none.
func activeTicket(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) (*model.TicketM, error) {
	if dr.TicketM != nil {
		return dr.TicketM, nil
	}
	if dr.SessionM != nil && dr.SessionM.AssociatedTicketID != "" {
		return sr.Store.Ticket().Get(ctx, where.F("ticket_id", dr.SessionM.AssociatedTicketID))
	}
	return nil, fmt.Errorf("当前会话没有关联的工单，请先使用 /oncall 建单")
}

// updateTicketStatus transitions the associated ticket to the given status.
func updateTicketStatus(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, status model.TicketStatus) (*model.TicketM, error) {
	ticket, err := activeTicket(ctx, sr, dr)
	if err != nil {
		return nil, err
	}
	ticket.Status = status
	ticket.UpdatedAt = time.Now()
	if status == model.TicketStatusClosed {
		now := time.Now()
		ticket.ClosedAt = &now
	}
	if err := sr.Store.Ticket().Update(ctx, ticket); err != nil {
		return nil, fmt.Errorf("failed to update ticket: %w", err)
	}
	return ticket, nil
}
