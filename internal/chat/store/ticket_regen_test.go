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
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	duty "github.com/onexstack/onexai/pkg/duty"
)

// fakeTicketStore is the in-memory local ticket table.
type fakeTicketStore struct {
	tickets map[string]*model.TicketM
}

func newFakeTicketStore() *fakeTicketStore {
	return &fakeTicketStore{tickets: map[string]*model.TicketM{}}
}

func (s *fakeTicketStore) Create(_ context.Context, obj *model.TicketM) error {
	s.tickets[obj.TicketID] = ptr.Clone(obj)
	return nil
}

func (s *fakeTicketStore) Update(_ context.Context, obj *model.TicketM) error {
	s.tickets[obj.TicketID] = ptr.Clone(obj)
	return nil
}

func (s *fakeTicketStore) Delete(_ context.Context, _ *where.Options) error { return nil }

func (s *fakeTicketStore) Get(_ context.Context, _ *where.Options) (*model.TicketM, error) {
	for _, t := range s.tickets {
		return ptr.Clone(t), nil
	}
	return nil, ErrRecordNotFound
}

func (s *fakeTicketStore) CountByGroup(_ context.Context, _ string) (int64, error) { return 0, nil }

// List returns everything the fake holds, ignoring the filter and the paging.
//
// That is faithful enough for what this fake is for: the test around it
// exercises the Regen mirror, which lists nothing. A test for filtering or
// paging belongs against the SQL store, where where.Options is actually
// interpreted — asserting it here would be asserting this method against
// itself.
func (s *fakeTicketStore) List(_ context.Context, _ *where.Options) (int64, []*model.TicketM, error) {
	list := make([]*model.TicketM, 0, len(s.tickets))
	for _, t := range s.tickets {
		list = append(list, ptr.Clone(t))
	}
	return int64(len(list)), list, nil
}

// ListForSync returns nothing, and that is the honest answer here.
//
// What it has to be is a method, so that this fake still satisfies TicketStore.
// What it must not be is a second implementation of the candidate query: the
// three conditions it applies — the duty group, the finished status, and the
// presence of a handling note — are exercised against the SQL store in
// ticket_sync_test.go, which is the only implementation that runs in
// production. Restating them here would be asserting this method against
// itself.
//
// The Regen mirror this fake stands in for never asks the question anyway.
func (s *fakeTicketStore) ListForSync(_ context.Context, _, _ []string, _ time.Time, _ int) ([]*model.TicketM, error) {
	return nil, nil
}

// UpdateSummary writes the summary if the row is still empty, which is the part
// of the real store's contract this fake can reproduce honestly: it is the only
// behaviour callers branch on. The conditional UPDATE's cross-process guarantee
// is the database's, and is tested against the SQL store.
func (s *fakeTicketStore) UpdateSummary(_ context.Context, ticketID, summary string, at time.Time) (bool, error) {
	t, ok := s.tickets[ticketID]
	if !ok || t.Summary != "" {
		return false, nil
	}
	clone := ptr.Clone(t)
	clone.Summary = summary
	clone.UpdatedAt = at
	s.tickets[ticketID] = clone
	return true, nil
}

// fakeIncidents records what reached Regen and answers with what the test set.
type fakeIncidents struct {
	created []duty.CreateIncidentRequest
	updates []duty.UpdateIncidentRequest

	createErr    error
	updateErr    error
	incident     *duty.Incident
	incidentErr  error
	gotIncidents []string
}

// CanTransition mirrors the reference provider's one-way state machine, so the
// tests keep exercising the transition guard.
func (f *fakeIncidents) CanTransition(from, to duty.Status) bool {
	switch from {
	case duty.StatusTriggered:
		return to == duty.StatusAcknowledged || to == duty.StatusResolved || to == duty.StatusCanceled
	case duty.StatusAcknowledged:
		return to == duty.StatusResolved
	default:
		return false
	}
}

func (f *fakeIncidents) CreateIncident(_ context.Context, req duty.CreateIncidentRequest) (duty.Incident, error) {
	f.created = append(f.created, req)
	if f.createErr != nil {
		return duty.Incident{}, f.createErr
	}
	// Number the incidents, and keep the prefix distinct from the ids a test
	// seeds, so "the ticket now points at the new incident" is assertable.
	id := "new-inc-" + strconv.Itoa(len(f.created))
	return duty.Incident{ID: id, Status: duty.StatusTriggered, Severity: req.Severity}, nil
}

func (f *fakeIncidents) GetIncident(_ context.Context, id string) (duty.Incident, error) {
	f.gotIncidents = append(f.gotIncidents, id)
	if f.incidentErr != nil {
		return duty.Incident{}, f.incidentErr
	}
	if f.incident == nil {
		// The real server always answers with an incident; stand in with a live
		// one so an unset fake means "nothing has happened to it yet".
		return duty.Incident{ID: id, Status: duty.StatusTriggered}, nil
	}
	return *f.incident, nil
}

func (f *fakeIncidents) UpdateIncident(_ context.Context, _ string, req duty.UpdateIncidentRequest) (duty.Incident, error) {
	f.updates = append(f.updates, req)
	if f.updateErr != nil {
		return duty.Incident{}, f.updateErr
	}
	return duty.Incident{ID: "inc-1", Status: req.Status, Severity: req.Severity}, nil
}

// fakeStore is the IStore the decorator wraps. Only Ticket() is implemented;
// the embedded nil interface makes any other call panic, which is the point —
// the decorator must pass them straight through without touching them.
type fakeStore struct {
	IStore
	tickets TicketStore
}

func (s *fakeStore) Ticket() TicketStore { return s.tickets }

// newSyncedTicket returns the decorated ticket store and the undecorated
// in-memory table behind it, so a test can assert what actually landed.
func newSyncedTicket(t *testing.T, cli incidentSource) (TicketStore, *fakeTicketStore) {
	t.Helper()
	base := newFakeTicketStore()
	return NewProviderSyncedStore(&fakeStore{tickets: base}, cli).Ticket(), base
}

func TestRegenCreateMirrorsTicket(t *testing.T) {
	cli := &fakeIncidents{}
	tickets, base := newSyncedTicket(t, cli)

	ticket := &model.TicketM{TicketID: "tkt-1", Title: "DB is down", Level: 1, Status: model.TicketStatusOpen}
	if err := tickets.Create(context.Background(), ticket); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if ticket.IncidentID != "new-inc-1" {
		t.Errorf("IncidentID = %q, want new-inc-1", ticket.IncidentID)
	}
	if ticket.Source != ticketSourceMirrored {
		t.Errorf("Source = %q, want %q", ticket.Source, ticketSourceMirrored)
	}
	if len(cli.created) != 1 || cli.created[0].Severity != duty.SeverityCritical {
		t.Fatalf("CreateIncident calls = %+v, want one critical incident", cli.created)
	}
	if _, ok := base.tickets["tkt-1"]; !ok {
		t.Error("the local row was not written")
	}
}

// TestRegenCreateFailureLeavesNoOrphan is why Create talks to Regen first: a
// rejected incident must not leave a ticket that claims to be mirrored.
func TestRegenCreateFailureLeavesNoOrphan(t *testing.T) {
	cli := &fakeIncidents{createErr: errors.New("regen is down")}
	tickets, base := newSyncedTicket(t, cli)

	ticket := &model.TicketM{TicketID: "tkt-1", Title: "DB is down", Status: model.TicketStatusOpen}
	if err := tickets.Create(context.Background(), ticket); err == nil {
		t.Fatal("Create returned no error while regen was failing")
	}
	if len(base.tickets) != 0 {
		t.Error("a ticket was written even though the regen incident was not created")
	}
}

func TestRegenUpdatePushesBeforeWritingLocally(t *testing.T) {
	cli := &fakeIncidents{}
	tickets, base := newSyncedTicket(t, cli)

	ticket := &model.TicketM{
		TicketID:   "tkt-1",
		Status:     model.TicketStatusProcessing,
		Level:      2,
		IncidentID: "inc-1",
	}
	if err := tickets.Update(context.Background(), ticket); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	if len(cli.updates) != 1 || cli.updates[0].Status != duty.StatusAcknowledged || cli.updates[0].Severity != duty.SeverityHigh {
		t.Fatalf("UpdateIncident calls = %+v, want acknowledged/high", cli.updates)
	}
	if got := base.tickets["tkt-1"]; got == nil || got.Status != model.TicketStatusProcessing {
		t.Errorf("local ticket = %+v, want it written after the push", got)
	}
}

func TestRegenUpdateFailureKeepsLocalStateBack(t *testing.T) {
	cli := &fakeIncidents{updateErr: errors.New("regen is down")}
	tickets, base := newSyncedTicket(t, cli)

	ticket := &model.TicketM{TicketID: "tkt-1", Status: model.TicketStatusClosed, IncidentID: "inc-1"}
	if err := tickets.Update(context.Background(), ticket); err == nil {
		t.Fatal("Update returned no error while regen was failing")
	}
	if len(base.tickets) != 0 {
		t.Error("the local row advanced even though regen rejected the change")
	}
}

// TestSavedStaysLocal pins the one status Regen has no equivalent for.
func TestSavedStaysLocal(t *testing.T) {
	cli := &fakeIncidents{}
	tickets, _ := newSyncedTicket(t, cli)

	ticket := &model.TicketM{TicketID: "tkt-1", Status: model.TicketStatusSaved, Level: 4, IncidentID: "inc-1"}
	if err := tickets.Update(context.Background(), ticket); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if len(cli.updates) != 1 || cli.updates[0].Status != "" {
		t.Fatalf("UpdateIncident calls = %+v, want the status omitted", cli.updates)
	}
}

func TestRegenGetReconcilesFromIncident(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusAcknowledged}}
	tickets, _ := newSyncedTicket(t, cli)

	local := &model.TicketM{TicketID: "tkt-1", Status: model.TicketStatusOpen, IncidentID: "inc-1"}
	if err := tickets.Create(context.Background(), local); err != nil {
		t.Fatalf("seeding the local row failed: %v", err)
	}
	// Create() sets the incident id; keep the seeded status.
	local.Status = model.TicketStatusOpen

	got, err := tickets.Get(context.Background(), where.F("ticket_id", "tkt-1"))
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status != model.TicketStatusProcessing {
		t.Errorf("Get status = %q, want processing (from the acknowledged incident)", got.Status)
	}
	if len(cli.gotIncidents) != 1 {
		t.Errorf("GetIncident calls = %v, want one", cli.gotIncidents)
	}
}

func TestRegenGetDoesNotOverwriteSaved(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusAcknowledged}}
	tickets, base := newSyncedTicket(t, cli)

	base.tickets["tkt-1"] = &model.TicketM{
		TicketID: "tkt-1", Status: model.TicketStatusSaved, IncidentID: "inc-1",
	}

	got, err := tickets.Get(context.Background(), where.F("ticket_id", "tkt-1"))
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status != model.TicketStatusSaved {
		t.Errorf("Get status = %q, want saved untouched", got.Status)
	}
	if len(cli.gotIncidents) != 0 {
		t.Errorf("the incident was read for a saved ticket: %v", cli.gotIncidents)
	}
}

func TestRegenGetToleratesIncidentFailure(t *testing.T) {
	cli := &fakeIncidents{incidentErr: errors.New("regen is down")}
	tickets, base := newSyncedTicket(t, cli)

	base.tickets["tkt-1"] = &model.TicketM{
		TicketID: "tkt-1", Status: model.TicketStatusOpen, IncidentID: "inc-1",
	}

	got, err := tickets.Get(context.Background(), where.F("ticket_id", "tkt-1"))
	if err != nil {
		t.Fatalf("Get returned error although the local row was readable: %v", err)
	}
	if got.Status != model.TicketStatusOpen {
		t.Errorf("Get status = %q, want the local open state", got.Status)
	}
}

func TestStatusMappings(t *testing.T) {
	localToProvider := []struct {
		local model.TicketStatus
		want  duty.Status
	}{
		{model.TicketStatusOpen, duty.StatusTriggered},
		{model.TicketStatusProcessing, duty.StatusAcknowledged},
		// closed and resolved share a Regen state: Regen cannot cancel a ticket
		// someone has already acknowledged, so canceled is unreachable there.
		{model.TicketStatusResolved, duty.StatusResolved},
		{model.TicketStatusClosed, duty.StatusResolved},
		{model.TicketStatusSaved, ""},
	}
	for _, tt := range localToProvider {
		if got := incidentStatusFor(tt.local); got != tt.want {
			t.Errorf("incidentStatusFor(%q) = %q, want %q", tt.local, got, tt.want)
		}
	}

	providerToLocal := []struct {
		remote duty.Status
		want   model.TicketStatus
	}{
		{duty.StatusTriggered, model.TicketStatusOpen},
		{duty.StatusAcknowledged, model.TicketStatusProcessing},
		{duty.StatusResolved, model.TicketStatusResolved},
		{duty.StatusCanceled, model.TicketStatusClosed},
		{duty.Status("something-new"), ""},
	}
	for _, tt := range providerToLocal {
		if got := localStatusFor(tt.remote); got != tt.want {
			t.Errorf("localStatusFor(%q) = %q, want %q", tt.remote, got, tt.want)
		}
	}

	levels := []struct {
		level int64
		want  duty.Severity
	}{
		{1, duty.SeverityCritical},
		{2, duty.SeverityHigh},
		{3, duty.SeverityMedium},
		{4, duty.SeverityLow},
		{0, duty.SeverityLow},
	}
	for _, tt := range levels {
		if got := severityForLevel(tt.level); got != tt.want {
			t.Errorf("severityForLevel(%d) = %q, want %q", tt.level, got, tt.want)
		}
	}

	// The pull refuses to walk a ticket backwards, so closed has to outrank
	// resolved even though they map to the same incident state.
	if ticketProgress(model.TicketStatusClosed) <= ticketProgress(model.TicketStatusResolved) {
		t.Error("closed must outrank resolved, or a resolved incident would undo a closed ticket")
	}
}

// TestRegenUpdateReopenCreatesNewIncident covers /open on a finished incident:
// Regen's machine cannot go back to triggered, so the reopened ticket is given
// a fresh incident rather than the command failing.
func TestRegenUpdateReopenCreatesNewIncident(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusResolved}}
	tickets, base := newSyncedTicket(t, cli)

	ticket := &model.TicketM{
		TicketID:   "tkt-1",
		Title:      "DB is down",
		Status:     model.TicketStatusOpen,
		IncidentID: "inc-1",
	}
	if err := tickets.Update(context.Background(), ticket); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	if len(cli.created) != 1 {
		t.Fatalf("CreateIncident calls = %d, want 1 for the reopen", len(cli.created))
	}
	if len(cli.updates) != 0 {
		t.Errorf("UpdateIncident calls = %+v, want none for a terminal incident", cli.updates)
	}
	if ticket.IncidentID == "inc-1" {
		t.Error("the ticket still points at the finished incident")
	}
	if got := base.tickets["tkt-1"]; got == nil || got.IncidentID != ticket.IncidentID {
		t.Errorf("the local row does not carry the new incident id: %+v", got)
	}
}

// TestRegenUpdateReopenAdvancesTheNewIncident covers a reopen that lands past
// triggered: the fresh incident has to be moved on as well, or the ticket and
// the incident disagree until the next write.
func TestRegenUpdateReopenAdvancesTheNewIncident(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusResolved}}
	tickets, _ := newSyncedTicket(t, cli)

	ticket := &model.TicketM{
		TicketID:   "tkt-1",
		Title:      "DB is down",
		Status:     model.TicketStatusProcessing,
		IncidentID: "inc-1",
	}
	if err := tickets.Update(context.Background(), ticket); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	if len(cli.created) != 1 {
		t.Fatalf("CreateIncident calls = %d, want 1", len(cli.created))
	}
	if len(cli.updates) != 1 || cli.updates[0].Status != duty.StatusAcknowledged {
		t.Fatalf("UpdateIncident calls = %+v, want one acknowledged move on the new incident", cli.updates)
	}
}

// TestRegenUpdateTerminalToLowerTerminalLeavesIncidentAlone covers a closed
// ticket whose incident was already canceled: there is no transition to send,
// and the command must still succeed.
func TestRegenUpdateTerminalToLowerTerminalLeavesIncidentAlone(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusCanceled}}
	tickets, _ := newSyncedTicket(t, cli)

	ticket := &model.TicketM{TicketID: "tkt-1", Status: model.TicketStatusClosed, IncidentID: "inc-1"}
	if err := tickets.Update(context.Background(), ticket); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if len(cli.created) != 0 {
		t.Errorf("CreateIncident calls = %d, want none: closing is not a reopen", len(cli.created))
	}
	if len(cli.updates) != 1 || cli.updates[0].Status != "" {
		t.Errorf("UpdateIncident calls = %+v, want one severity-only update", cli.updates)
	}
}

// TestRegenGetNeverWalksBackwards is what keeps /close from being undone: a
// closed ticket and a resolved ticket both mirror "resolved" in Regen, so the
// pull must not read that back as "not closed after all".
func TestRegenGetNeverWalksBackwards(t *testing.T) {
	cli := &fakeIncidents{incident: &duty.Incident{ID: "inc-1", Status: duty.StatusResolved}}
	tickets, base := newSyncedTicket(t, cli)

	base.tickets["tkt-1"] = &model.TicketM{
		TicketID: "tkt-1", Status: model.TicketStatusClosed, IncidentID: "inc-1",
	}

	got, err := tickets.Get(context.Background(), where.F("ticket_id", "tkt-1"))
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status != model.TicketStatusClosed {
		t.Errorf("Get status = %q, want closed preserved", got.Status)
	}
}

func TestNewProviderSyncedStoreSkipsNil(t *testing.T) {
	base := &fakeStore{tickets: newFakeTicketStore()}
	if got := NewProviderSyncedStore(base, nil); got != IStore(base) {
		t.Error("a nil provider should return the store unchanged")
	}
}
