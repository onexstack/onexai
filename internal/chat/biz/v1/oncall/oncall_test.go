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

package oncall

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
)

// stubStore answers only the lookups these tests reach.
//
// It embeds the interface rather than implementing it: the on-call read surface
// touches four stores, and fifteen methods that no test calls would be fifteen
// chances to write a fake that agrees with the code instead of with the world.
// An unexpected call panics on the nil interface, which is the intended
// behaviour — the fix is to teach this stub the method, not to make it guess.
type stubStore struct {
	store.IStore
	session   *model.SessionM
	sessionAt error
	archived  []*model.ChatMessageM
	archiveAt error
}

func (s *stubStore) Session() store.SessionStore { return &stubSessions{data: s} }
func (s *stubStore) ChatMessages() store.ChatMessageStore {
	return &stubChatMessages{data: s}
}

// The two sub-stores follow the same shape: the interface is embedded so only
// the method under test has to be written, and the answers are read off the
// stub that owns them.
type stubSessions struct {
	store.SessionStore
	data *stubStore
}

func (s *stubSessions) Get(_ context.Context, _ *where.Options) (*model.SessionM, error) {
	if s.data.sessionAt != nil {
		return nil, s.data.sessionAt
	}
	if s.data.session == nil {
		return nil, store.ErrRecordNotFound
	}
	return s.data.session, nil
}

type stubChatMessages struct {
	store.ChatMessageStore
	data *stubStore
}

func (s *stubChatMessages) ListByGroup(_ context.Context, _ string, _ int) ([]*model.ChatMessageM, error) {
	return s.data.archived, s.data.archiveAt
}

// stubHistory stands in for the live IM history surface.
type stubHistory struct {
	msgs []*im.Message
	err  error
	// calls counts the lookups, so a test can assert that a ticket answered from
	// the archive never went to the platform first.
	calls int
}

func (s *stubHistory) GetHistory(_ context.Context, _ string, _ int) ([]*im.Message, error) {
	s.calls++
	return s.msgs, s.err
}

// TestTranscriptFallsBackInOrder pins the one decision this method makes: the
// live conversation is read first because it is the only complete source while
// the group exists, the archive second because past the day it is dissolved the
// archive is the only source left, and neither answers SourceNone — which is a
// different fact from an empty conversation.
func TestTranscriptFallsBackInOrder(t *testing.T) {
	session := &model.SessionM{SessionID: "oc_group", AssociatedTicketID: "tkt-1"}
	live := []*im.Message{{ID: "m1", SenderName: "Lingfei Kong", Content: im.MessageContent{Text: "重启试试"}}}

	t.Run("the live conversation answers and the archive is not read", func(t *testing.T) {
		st := &stubStore{session: session, archived: []*model.ChatMessageM{{MessageID: "old"}}}
		hist := &stubHistory{msgs: live}

		got, err := New(st, hist).Transcript(context.Background(), "tkt-1")
		if err != nil {
			t.Fatalf("Transcript() = %v, want nil", err)
		}
		if got.Source != SourceIM {
			t.Errorf("Source = %q, want %q", got.Source, SourceIM)
		}
		if len(got.Messages) != 1 || got.Messages[0].ID != "m1" {
			t.Errorf("Messages = %v, want the live one", got.Messages)
		}
		if got.ConvID != "oc_group" {
			t.Errorf("ConvID = %q, want oc_group", got.ConvID)
		}
		if hist.calls != 1 {
			t.Errorf("history calls = %d, want 1", hist.calls)
		}
	})

	t.Run("an empty live answer falls through to the archive", func(t *testing.T) {
		noteAt := time.Now().Add(-48 * time.Hour)
		st := &stubStore{
			session:  session,
			archived: []*model.ChatMessageM{{MessageID: "a1", SenderName: "罗工", Content: "换机器解决", Time: &noteAt}},
		}

		got, err := New(st, &stubHistory{}).Transcript(context.Background(), "tkt-1")
		if err != nil {
			t.Fatalf("Transcript() = %v, want nil", err)
		}
		if got.Source != SourceArchive {
			t.Errorf("Source = %q, want %q", got.Source, SourceArchive)
		}
		if len(got.Messages) != 1 || got.Messages[0].Content.Text != "换机器解决" {
			t.Errorf("Messages = %v, want the archived one", got.Messages)
		}
		// The archived row's own time is carried, not the moment it was read:
		// a transcript with invented timestamps would date the answer wrong.
		if !got.Messages[0].CreateTime.Equal(noteAt) {
			t.Errorf("CreateTime = %v, want the message's %v", got.Messages[0].CreateTime, noteAt)
		}
	})

	t.Run("nothing anywhere is SourceNone, not an error", func(t *testing.T) {
		got, err := New(&stubStore{session: session}, &stubHistory{}).Transcript(context.Background(), "tkt-1")
		if err != nil {
			t.Fatalf("Transcript() = %v, want nil", err)
		}
		if got.Source != SourceNone || len(got.Messages) != 0 {
			t.Errorf("got source %q with %d messages, want %q and none", got.Source, len(got.Messages), SourceNone)
		}
	})

	t.Run("a ticket with no group is SourceNone and reads nothing", func(t *testing.T) {
		hist := &stubHistory{msgs: live}
		got, err := New(&stubStore{}, hist).Transcript(context.Background(), "tkt-1")
		if err != nil {
			t.Fatalf("Transcript() = %v, want nil", err)
		}
		if got.Source != SourceNone || got.ConvID != "" {
			t.Errorf("got source %q conv %q, want %q and no conversation", got.Source, got.ConvID, SourceNone)
		}
		if hist.calls != 0 {
			t.Errorf("history calls = %d, want 0: there is no conversation to ask about", hist.calls)
		}
	})
}

// TestTranscriptSeparatesAMissingRowFromAReadFailure pins the one error case
// that must not be swallowed: a ticket with no session is an answer, a database
// that could not be asked is not.
func TestTranscriptSeparatesAMissingRowFromAReadFailure(t *testing.T) {
	broken := errors.New("connection reset")
	_, err := New(&stubStore{sessionAt: broken}, nil).Transcript(context.Background(), "tkt-1")
	if err == nil {
		t.Fatal("Transcript() = nil, want the read failure surfaced")
	}
	if !errors.Is(err, broken) {
		t.Errorf("Transcript() = %v, want it to wrap the cause", err)
	}
}

// TestTranscriptRequiresATicketID keeps an unscoped read impossible: with no id
// there is nothing to scope the session lookup to.
func TestTranscriptRequiresATicketID(t *testing.T) {
	if _, err := New(&stubStore{}, nil).Transcript(context.Background(), "  "); err == nil {
		t.Fatal("Transcript(\"\") = nil, want an argument error")
	}
}

// TestListForSyncRejectsAnEmptyOrUnknownQuery pins the two inputs that would
// otherwise produce a silently empty page.
//
// An empty page is a legitimate answer here — "nothing has happened" — so a
// caller that omitted its duty groups, or misspelled a status, would read the
// same emptiness and conclude there was no work. Both have to be loud instead.
func TestListForSyncRejectsAnEmptyOrUnknownQuery(t *testing.T) {
	b := New(&stubStore{}, nil)

	if _, err := b.ListForSync(context.Background(), SyncQuery{
		Statuses: []string{"closed"},
	}); err == nil {
		t.Error("no groups: want an argument error, got nil")
	}

	if _, err := b.ListForSync(context.Background(), SyncQuery{
		GroupIDs: []string{"onex"},
	}); err == nil {
		t.Error("no statuses: want an argument error, got nil")
	}

	if _, err := b.ListForSync(context.Background(), SyncQuery{
		GroupIDs: []string{"onex"},
		Statuses: []string{"clsoed"},
	}); err == nil {
		t.Error("a misspelled status: want an argument error, got nil")
	}
}
