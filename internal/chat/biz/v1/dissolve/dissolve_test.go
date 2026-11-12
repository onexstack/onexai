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

package dissolve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
)

// fakeSessionStore answers the two calls the sweep makes.
type fakeSessionStore struct {
	store.SessionStore
	due      []*model.SessionM
	listErr  error
	marked   []string
	markErr  error
	gotGrace time.Time
}

func (f *fakeSessionStore) ListDismissable(_ context.Context, before time.Time, _ int) ([]*model.SessionM, error) {
	f.gotGrace = before
	return f.due, f.listErr
}

func (f *fakeSessionStore) MarkDismissed(_ context.Context, sessionID string, _ time.Time) error {
	f.marked = append(f.marked, sessionID)
	return f.markErr
}

// fakeArchive records what was archived.
type fakeArchive struct {
	store.ChatMessageStore
	rows     []*model.ChatMessageM
	archived []string
	err      error
}

func (f *fakeArchive) Archive(_ context.Context, msgs []*model.ChatMessageM) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	for _, m := range msgs {
		f.archived = append(f.archived, m.MessageID)
	}
	return len(msgs), nil
}

// fakeStore carries the two sub-stores.
type fakeStore struct {
	store.IStore
	session *fakeSessionStore
	archive *fakeArchive
}

func (f *fakeStore) Session() store.SessionStore          { return f.session }
func (f *fakeStore) ChatMessages() store.ChatMessageStore { return f.archive }

// fakeLark records the calls, in order, so a test can assert what happened
// before what.
type fakeLark struct {
	history     []*im.Message
	historyErr  error
	dissolveErr error
	calls       []string
}

func (f *fakeLark) GetHistory(_ context.Context, convID string, _ int) ([]*im.Message, error) {
	f.calls = append(f.calls, "history:"+convID)
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.history, nil
}

func (f *fakeLark) DissolveChat(_ context.Context, chatID string) error {
	f.calls = append(f.calls, "dissolve:"+chatID)
	return f.dissolveErr
}

func history(ids ...string) []*im.Message {
	out := make([]*im.Message, 0, len(ids))
	for _, id := range ids {
		out = append(out, &im.Message{
			ID: id, SenderID: "ou_1", SenderName: "Ada",
			Content: im.MessageContent{Type: "text", Text: "hello " + id},
		})
	}
	return out
}

func newSweeper(t *testing.T, st store.IStore, lk Lark) Sweeper {
	t.Helper()
	s := New(st, lk, nil, Options{Enabled: true, GraceAfterClose: time.Hour})
	if s == nil {
		t.Fatal("New returned nil for an enabled sweep")
	}
	return s
}

// TestArchiveHappensBeforeDissolve is the ordering, and it is the whole design.
//
// Lark keeps no history for a group it dissolved: its documentation says so,
// and the messages stop being reachable the moment the call returns. So the
// archive is a precondition. The failure this test exists to prevent is not a
// missing archive — it is a group that was archived *after* it was dissolved,
// which reads as an archive that worked and is in fact empty.
func TestArchiveHappensBeforeDissolve(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{due: []*model.SessionM{{SessionID: "oc_1"}}},
		archive: &fakeArchive{},
	}
	lk := &fakeLark{history: history("m1", "m2")}
	s := newSweeper(t, st, lk)

	n, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("dissolved = %d, want 1", n)
	}

	want := []string{"history:oc_1", "dissolve:oc_1"}
	if strings.Join(lk.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want the archive read before the dissolve", lk.calls)
	}
	if len(st.archive.archived) != 2 {
		t.Errorf("archived %v, want both messages", st.archive.archived)
	}
	if len(st.session.marked) != 1 || st.session.marked[0] != "oc_1" {
		t.Errorf("marked = %v, want the group to be recorded as dissolved", st.session.marked)
	}
	// The mark came last: it is what stops a retry from being needed, and a
	// group marked before it was gone would never be tried again.
	if st.archive.err != nil {
		t.Fatal("unreachable")
	}
}

// TestAFailedArchiveRefusesToDissolve is the other half of the ordering.
//
// A group left standing is a smaller harm than a conversation nobody can read
// again, and leaving it standing costs one more pass.
func TestAFailedArchiveRefusesToDissolve(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{due: []*model.SessionM{{SessionID: "oc_1"}}},
		archive: &fakeArchive{err: errors.New("the archive table is down")},
	}
	lk := &fakeLark{history: history("m1")}
	s := newSweeper(t, st, lk)

	n, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("dissolved = %d, want none", n)
	}
	for _, c := range lk.calls {
		if strings.HasPrefix(c, "dissolve:") {
			t.Fatalf("the group was dissolved despite a failed archive: %v", lk.calls)
		}
	}
	if len(st.session.marked) != 0 {
		t.Errorf("a group that was not dissolved was marked: %v", st.session.marked)
	}
}

// TestAFailedHistoryReadRefusesToDissolve pins the same rule one step earlier:
// there is nothing to archive from a read that failed, and dissolving anyway
// would destroy the very messages the archive was for.
func TestAFailedHistoryReadRefusesToDissolve(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{due: []*model.SessionM{{SessionID: "oc_1"}}},
		archive: &fakeArchive{},
	}
	lk := &fakeLark{historyErr: errors.New("lark is unreachable")}
	s := newSweeper(t, st, lk)

	if _, err := s.SweepOnce(context.Background()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if len(st.archive.archived) != 0 {
		t.Errorf("messages were archived from a failed read: %v", st.archive.archived)
	}
	if len(lk.calls) != 1 || !strings.HasPrefix(lk.calls[0], "history:") {
		t.Errorf("calls = %v, want the read that failed and nothing after it", lk.calls)
	}
}

// TestOneFailureDoesNotStopThePass pins the isolation.
//
// A group the bot cannot dissolve — it is not the owner, say — would otherwise
// block every group behind it for ever, and the sweep would look alive while
// dissolving nothing.
func TestOneFailureDoesNotStopThePass(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{due: []*model.SessionM{
			{SessionID: "oc_bad"}, {SessionID: "oc_good"},
		}},
		archive: &fakeArchive{},
	}
	// The first dissolve fails, the rest work.
	lk := &failingOnceLark{failOn: "oc_bad"}
	s := newSweeper(t, st, lk)

	n, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("dissolved = %d, want the one that worked", n)
	}
	if len(st.session.marked) != 1 || st.session.marked[0] != "oc_good" {
		t.Errorf("marked = %v, want only the group that was dissolved", st.session.marked)
	}
}

// failingOnceLark refuses to dissolve one group and succeeds for the rest.
type failingOnceLark struct {
	fakeLark
	failOn string
}

func (f *failingOnceLark) GetHistory(_ context.Context, convID string, _ int) ([]*im.Message, error) {
	return history("m_" + convID), nil
}

func (f *failingOnceLark) DissolveChat(_ context.Context, chatID string) error {
	if chatID == f.failOn {
		return errors.New("the bot does not own this group")
	}
	return nil
}

// TestAFailedMarkDoesNotUndoTheDissolve pins that the last step is best-effort.
//
// The group is already gone and the platform will refuse to dissolve it again,
// so a failed mark costs a wasted query on the next pass — not a second
// dissolution, and not a pass that stops.
func TestAFailedMarkDoesNotUndoTheDissolve(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{
			due:     []*model.SessionM{{SessionID: "oc_1"}},
			markErr: errors.New("the database is down"),
		},
		archive: &fakeArchive{},
	}
	lk := &fakeLark{history: history("m1")}
	s := newSweeper(t, st, lk)

	n, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Errorf("dissolved = %d, want the group to count as dissolved", n)
	}
}

// TestTheGracePeriodIsCountedBackFromNow pins the deadline arithmetic: the
// query is asked for groups closed before now minus the grace, so a group
// closed a minute ago is not a candidate.
func TestTheGracePeriodIsCountedBackFromNow(t *testing.T) {
	st := &fakeStore{session: &fakeSessionStore{}, archive: &fakeArchive{}}
	s := New(st, &fakeLark{}, nil, Options{Enabled: true, GraceAfterClose: 2 * time.Hour})

	before := time.Now().Add(-2 * time.Hour).Add(-time.Second)
	if _, err := s.SweepOnce(context.Background()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	after := time.Now().Add(-2 * time.Hour).Add(time.Second)

	got := st.session.gotGrace
	if got.Before(before) || got.After(after) {
		t.Errorf("the sweep asked for groups closed before %v, want about two hours ago", got)
	}
}

// TestDisabledBuildsNothing pins the default. Dissolving cannot be undone, so a
// deployment opts in rather than discovering after an upgrade that its closed
// groups are going.
func TestDisabledBuildsNothing(t *testing.T) {
	st := &fakeStore{session: &fakeSessionStore{}, archive: &fakeArchive{}}
	if got := New(st, &fakeLark{}, nil, Options{}); got != nil {
		t.Errorf("New with Enabled false returned %T, want nil", got)
	}
	if got := New(nil, &fakeLark{}, nil, Options{Enabled: true}); got != nil {
		t.Errorf("New with no store returned %T, want nil", got)
	}
	if got := New(st, nil, nil, Options{Enabled: true}); got != nil {
		t.Errorf("New with no IM backend returned %T, want nil", got)
	}
	// The disabled case is a genuinely nil interface, which is what the server's
	// own guard tests. A nil *sweeper behind a non-nil interface would pass that
	// guard and then panic on the first call, so the distinction is the reason
	// New returns the interface rather than the concrete type.
	var disabled Sweeper = New(st, &fakeLark{}, nil, Options{})
	if disabled != nil {
		t.Errorf("a disabled deployment holds %#v, want a nil interface", disabled)
	}
}

// TestAnEmptyHistoryStillDissolves pins that a group with nothing to keep is
// not a reason to refuse. Its messages are gone either way; leaving it standing
// would be keeping a group open for the sake of nothing.
func TestAnEmptyHistoryStillDissolves(t *testing.T) {
	st := &fakeStore{
		session: &fakeSessionStore{due: []*model.SessionM{{SessionID: "oc_1"}}},
		archive: &fakeArchive{},
	}
	s := newSweeper(t, st, &fakeLark{})

	n, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Errorf("dissolved = %d, want 1", n)
	}
}
