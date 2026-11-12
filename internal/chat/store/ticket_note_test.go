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
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestTicketNoteAppendAndList covers the shape /mark depends on: notes append
// rather than replace, they come back oldest-first so the log reads as a
// timeline, and one ticket's notes never leak into another's.
func TestTicketNoteAppendAndList(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketNoteM{})
	s := newTicketNoteStore(&store{db: db})
	ctx := context.Background()

	for _, n := range []*model.TicketNoteM{
		{TicketID: "tkt-1", AuthorID: "go.kong", AuthorName: "Lingfei Kong", Note: "先重启网关"},
		{TicketID: "tkt-2", AuthorID: "ops.luo", AuthorName: "罗工", Note: "另一个单子"},
		{TicketID: "tkt-1", AuthorID: "go.kong", AuthorName: "Lingfei Kong", Note: "需要补充 SOP", Review: true},
	} {
		if err := s.Create(ctx, n); err != nil {
			t.Fatalf("Create(%q) = %v, want nil", n.Note, err)
		}
	}

	total, notes, err := s.ListByTicket(ctx, "tkt-1", 0)
	if err != nil {
		t.Fatalf("ListByTicket() = %v, want nil", err)
	}
	if total != 2 || len(notes) != 2 {
		t.Fatalf("ListByTicket() returned %d notes (total %d), want 2", len(notes), total)
	}
	if notes[0].Note != "先重启网关" || notes[1].Note != "需要补充 SOP" {
		t.Errorf("notes = %q, %q; want oldest first", notes[0].Note, notes[1].Note)
	}
	if !notes[1].Review || notes[0].Review {
		t.Errorf("review marks = %v, %v; want false, true", notes[0].Review, notes[1].Review)
	}
	if notes[0].AuthorID != "go.kong" || notes[0].AuthorName != "Lingfei Kong" {
		t.Errorf("author = %q/%q", notes[0].AuthorID, notes[0].AuthorName)
	}

	// A limit trims the rows but not the total, which is how a caller tells a
	// trimmed log from a complete one.
	total, limited, err := s.ListByTicket(ctx, "tkt-1", 1)
	if err != nil {
		t.Fatalf("ListByTicket(limit 1) = %v, want nil", err)
	}
	if total != 2 || len(limited) != 1 || limited[0].Note != "先重启网关" {
		t.Errorf("limit 1 returned %d of total %d, first %q; want the 1 oldest of 2",
			len(limited), total, limited[0].Note)
	}

	// A ticket with no notes is an empty log, not an error.
	total, none, err := s.ListByTicket(ctx, "tkt-none", 0)
	if err != nil || total != 0 || len(none) != 0 {
		t.Errorf("ListByTicket(tkt-none) = %d notes (total %d), err %v; want none", len(none), total, err)
	}

	// An empty ticket id means "no ticket", not "every note": a caller that
	// lost the id must not be handed the whole table.
	total, all, err := s.ListByTicket(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListByTicket(\"\") = %v, want nil", err)
	}
	if total != 0 || len(all) != 0 {
		t.Errorf("ListByTicket(\"\") returned %d notes (total %d), want none", len(all), total)
	}
}
