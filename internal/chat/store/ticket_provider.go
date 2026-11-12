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

package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	duty "github.com/onexstack/onexai/pkg/duty"
)

// ticketSourceMirrored is the source written when a ticket is mirrored to an
// external incident provider. "regen" is kept as the stored value for
// compatibility with existing rows.
const ticketSourceMirrored = "regen"

// incidentSource is the slice of the duty provider the ticket mirror needs.
type incidentSource = duty.Incidents

// providerStore wraps an IStore so its Ticket() store keeps the remote incident
// in step with the local chat.ticket row. Every other store is passed through.
//
// Decorating here rather than inside each command is what keeps the duty
// commands unchanged: /close, /open, /save, /level, /hand and /mark all go
// through store.Ticket(), so they all get the mirror without knowing it exists.
type providerStore struct {
	IStore
	cli incidentSource
}

// NewProviderSyncedStore returns base with its tickets mirrored to the duty
// provider. When cli is nil, base is returned unchanged.
func NewProviderSyncedStore(base IStore, cli incidentSource) IStore {
	if cli == nil {
		return base
	}
	return &providerStore{IStore: base, cli: cli}
}

// Ticket returns the provider-synced ticket store.
func (s *providerStore) Ticket() TicketStore {
	return &providerTicketStore{TicketStore: s.IStore.Ticket(), cli: s.cli}
}

// providerTicketStore mirrors ticket mutations onto an external incident.
type providerTicketStore struct {
	TicketStore
	cli incidentSource
}

var _ TicketStore = (*providerTicketStore)(nil)

// Create opens the remote incident first and only then writes the local row, so
// a provider outage leaves no orphaned ticket behind. The incident id is stored on
// the ticket, which is the only link between the two systems.
func (s *providerTicketStore) Create(ctx context.Context, obj *model.TicketM) error {
	incident, err := s.cli.CreateIncident(ctx, duty.CreateIncidentRequest{
		Title:       obj.Title,
		Severity:    severityForLevel(obj.Level),
		Description: obj.Description,
	})
	if err != nil {
		return fmt.Errorf("failed to create the remote incident: %w", err)
	}

	obj.IncidentID = incident.ID
	if obj.Source == "" {
		obj.Source = ticketSourceMirrored
	}
	return s.TicketStore.Create(ctx, obj)
}

// Update pushes the change to the provider before touching the local row. Pushing
// second would let a failed mirror leave the two out of step with no way to
// tell which one is right.
func (s *providerTicketStore) Update(ctx context.Context, obj *model.TicketM) error {
	if err := s.push(ctx, obj); err != nil {
		return err
	}
	return s.TicketStore.Update(ctx, obj)
}

// reopenStatuses are the statuses a ticket moves *back* to. A provider whose
// incident state machine is one-way cannot accept them from a finished
// incident, so a reopen creates a fresh incident instead.
var reopenStatuses = map[duty.Status]bool{
	duty.StatusTriggered:    true,
	duty.StatusAcknowledged: true,
}

// Get reads the local row and then reconciles it with the incident: the
// provider is where a human acknowledges or resolves an incident, and that
// state has to be visible to the commands.
func (s *providerTicketStore) Get(ctx context.Context, opts *where.Options) (*model.TicketM, error) {
	ticket, err := s.TicketStore.Get(ctx, opts)
	if err != nil || ticket == nil {
		return ticket, err
	}
	s.reconcile(ctx, ticket)
	return ticket, nil
}

// push sends the ticket's status and severity to the provider.
//
// A provider's state machine may be one-way (Regen's is):
//
//	triggered    → acknowledged, resolved, canceled
//	acknowledged → resolved
//	resolved     → (terminal)
//	canceled     → (terminal)
//
// so a status may be one the provider will not accept from the incident's
// current state. The incident's state is therefore read first and the push is
// decided against it, rather than sending the status blind and turning the
// machine's refusal into a failed user command.
func (s *providerTicketStore) push(ctx context.Context, obj *model.TicketM) error {
	// A ticket created before a provider was configured has no incident to update.
	if obj.IncidentID == "" {
		return nil
	}

	current, err := s.cli.GetIncident(ctx, obj.IncidentID)
	if err != nil {
		return fmt.Errorf("failed to read ticket %s from the incident provider: %w", obj.TicketID, err)
	}

	desired := incidentStatusFor(obj.Status)
	severity := severityForLevel(obj.Level)

	// An empty status is omitted from the request, which is how the local-only
	// saved state stays local: the severity still travels, the status does not.
	req := duty.UpdateIncidentRequest{Severity: severity}
	switch {
	case desired == "" || desired == current.Status:
		// Nothing to transition; still carry the severity across.
	case canTransition(s.cli, current.Status, desired):
		req.Status = desired
	case reopenStatuses[desired]:
		// The ticket is being reopened (/open) and the incident is either
		// already finished or further along than the reopened ticket. A one-way
		// state machine cannot walk an incident back, so the reopened ticket
		// gets a fresh incident and the old one stays as the record of the first
		// round.
		incident, err := s.cli.CreateIncident(ctx, duty.CreateIncidentRequest{
			Title:       obj.Title,
			Severity:    severity,
			Description: obj.Description,
		})
		if err != nil {
			return fmt.Errorf("failed to reopen ticket %s as a new incident: %w", obj.TicketID, err)
		}
		obj.IncidentID = incident.ID
		// A fresh incident starts at triggered. If the reopened ticket is
		// already further along, carry that across too, or the two sides would
		// disagree until the next write.
		if desired != duty.StatusTriggered {
			if _, err := s.cli.UpdateIncident(ctx, incident.ID, duty.UpdateIncidentRequest{Status: desired, Severity: severity}); err != nil {
				return fmt.Errorf("failed to advance the reopened incident for ticket %s: %w", obj.TicketID, err)
			}
		}
		return nil
	default:
		// A lower terminal state (closed → resolved while the incident was
		// already canceled). There is nothing useful to send.
		slog.WarnContext(ctx, "store: the incident provider has no equivalent transition, leaving the incident as it is",
			"ticket_id", obj.TicketID, "incident_status", current.Status, "ticket_status", string(obj.Status))
	}

	if _, err := s.cli.UpdateIncident(ctx, obj.IncidentID, req); err != nil {
		return fmt.Errorf("failed to sync ticket %s to the incident provider: %w", obj.TicketID, err)
	}
	return nil
}

// canTransition reports whether the provider's state machine allows the move.
// A provider that does not implement duty.TransitionChecker is assumed to allow
// every transition.
func canTransition(cli incidentSource, from, to duty.Status) bool {
	if tc, ok := cli.(duty.TransitionChecker); ok {
		return tc.CanTransition(from, to)
	}
	return true
}

// reconcile pulls the incident state back onto the ticket.
//
// A failure to read is logged and tolerated: the local row is still the best
// answer available, and failing the read would break every command that
// resolves the current ticket.
func (s *providerTicketStore) reconcile(ctx context.Context, ticket *model.TicketM) {
	if ticket.IncidentID == "" {
		return
	}
	// saved has no provider equivalent; it is a local bookmark and the provider
	// must not overwrite it.
	if ticket.Status == model.TicketStatusSaved {
		return
	}

	incident, err := s.cli.GetIncident(ctx, ticket.IncidentID)
	if err != nil {
		slog.WarnContext(ctx, "store: failed to read the remote incident, keeping the local ticket state",
			"ticket_id", ticket.TicketID, "incident_id", ticket.IncidentID, "error", err)
		return
	}

	status := localStatusFor(incident.Status)
	if status == "" || status == ticket.Status {
		return
	}
	// The provider cannot tell a closed ticket from a resolved one — both mirror
	// "resolved" — so a pull that would undo the local distinction is dropped
	// rather than applied. Without this, /close would be reverted by the next
	// read.
	if ticketProgress(status) < ticketProgress(ticket.Status) {
		return
	}

	now := time.Now()
	ticket.Status = status
	ticket.UpdatedAt = now
	switch status {
	case model.TicketStatusResolved:
		if ticket.ResolvedAt == nil {
			ticket.ResolvedAt = &now
		}
	case model.TicketStatusClosed:
		if ticket.ClosedAt == nil {
			ticket.ClosedAt = &now
		}
	}

	// The raw store on purpose: this write is the reconciliation itself, and
	// pushing it back would bounce the same state to the provider again.
	if err := s.TicketStore.Update(ctx, ticket); err != nil {
		slog.WarnContext(ctx, "store: failed to persist the incident state locally",
			"ticket_id", ticket.TicketID, "error", err)
	}
}

// incidentStatusFor maps a local ticket status onto a provider incident status. An
// empty string means "no equivalent" and is omitted from the request.
//
// closed and resolved share one provider state on purpose. A one-way machine allows
// acknowledged → resolved but not acknowledged → canceled, so mapping a closed
// ticket to canceled would make /close fail on any ticket someone had already
// acknowledged; resolved is reachable from every live state. The cost is that
// The provider cannot tell the two apart, which is why the pull below refuses to walk
// a ticket backwards.
func incidentStatusFor(status model.TicketStatus) duty.Status {
	switch status {
	case model.TicketStatusOpen:
		return duty.StatusTriggered
	case model.TicketStatusProcessing:
		return duty.StatusAcknowledged
	case model.TicketStatusResolved, model.TicketStatusClosed:
		return duty.StatusResolved
	default:
		// saved — local only.
		return ""
	}
}

// ticketProgress orders the local statuses so a pull cannot move a ticket
// backwards. saved deliberately has no rank: it is a local bookmark and is
// handled before this is consulted.
func ticketProgress(status model.TicketStatus) int {
	switch status {
	case model.TicketStatusOpen:
		return 0
	case model.TicketStatusProcessing:
		return 1
	case model.TicketStatusResolved:
		return 2
	case model.TicketStatusClosed:
		return 3
	default:
		return 0
	}
}

// localStatusFor maps a provider incident status back onto the local ticket
// status. An empty string means "not recognised" and leaves the ticket alone.
func localStatusFor(status duty.Status) model.TicketStatus {
	switch status {
	case duty.StatusTriggered:
		return model.TicketStatusOpen
	case duty.StatusAcknowledged:
		return model.TicketStatusProcessing
	case duty.StatusResolved:
		return model.TicketStatusResolved
	case duty.StatusCanceled:
		return model.TicketStatusClosed
	default:
		return ""
	}
}

// severityForLevel maps the ticket level (1 = highest) onto a provider severity.
func severityForLevel(level int64) duty.Severity {
	switch level {
	case 1:
		return duty.SeverityCritical
	case 2:
		return duty.SeverityHigh
	case 3:
		return duty.SeverityMedium
	default:
		// 4 is the default level; an unset 0 lands here too.
		return duty.SeverityLow
	}
}
