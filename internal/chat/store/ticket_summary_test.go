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
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestUpdateSummaryWritesOnceAndOnlyOnce is the guarantee two /close invocations
// lean on: the second one pays for nothing.
//
// The conditional UPDATE is the entire mechanism — there is no lock — so what this
// pins is that the condition is really in the statement and that a losing writer
// is told it lost rather than being handed a success it did not earn.
func TestUpdateSummaryWritesOnceAndOnlyOnce(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	ctx := context.Background()

	created := time.Now().Add(-time.Hour)
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-1", Title: "网关报错", Status: model.TicketStatusClosed,
		GroupID: "go", UpdatedAt: created,
	})

	at := time.Now()
	wrote, err := s.UpdateSummary(ctx, "tkt-1", "## 问题现象\n…", at)
	require.NoError(t, err)
	assert.True(t, wrote, "the first writer must be told it wrote")

	// The winner's text survives a loser's call. This is the assertion that would
	// catch a dropped WHERE clause: without it the second call overwrites a
	// summary that cost a model run.
	wrote, err = s.UpdateSummary(ctx, "tkt-1", "第二个总结", at.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, wrote, "a ticket that already has a summary must not be written again")

	got, err := s.Get(ctx, where.F("ticket_id", "tkt-1"))
	require.NoError(t, err)
	assert.Equal(t, "## 问题现象\n…", got.Summary, "the first summary must survive the second attempt")
}

// TestUpdateSummaryMovesUpdatedAt is the sync path's contract, and the one that
// fails silently if it is broken.
//
// The downstream reader walks a watermark over updated_at. A summary written
// without moving that column is a summary the reader never looks at — the ticket
// is closed, the text is right there in the row, and nothing ever publishes it,
// with no error anywhere to explain why.
func TestUpdateSummaryMovesUpdatedAt(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	ctx := context.Background()

	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-1", Status: model.TicketStatusClosed, GroupID: "go", UpdatedAt: created,
	})

	at := created.Add(30 * time.Minute)
	wrote, err := s.UpdateSummary(ctx, "tkt-1", "总结", at)
	require.NoError(t, err)
	require.True(t, wrote)

	got, err := s.Get(ctx, where.F("ticket_id", "tkt-1"))
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.Equal(at), "updated_at = %v, want the write's %v", got.UpdatedAt, at)
}

// TestUpdateSummaryRefusesAnEmptyWrite pins the guard.
//
// An empty summary is the state this function moves *away* from, so writing one
// would match nothing and report success about a change that never happened —
// and every caller branches on that answer.
func TestUpdateSummaryRefusesAnEmptyWrite(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	ctx := context.Background()

	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-1", Status: model.TicketStatusClosed, GroupID: "go", UpdatedAt: time.Now(),
	})

	for _, tc := range []struct{ why, id, summary string }{
		{"no ticket", "", "总结"},
		{"no summary", "tkt-1", ""},
		{"only whitespace is not empty, so it is accepted", "tkt-1", " "},
	} {
		t.Run(tc.why, func(t *testing.T) {
			wrote, err := s.UpdateSummary(ctx, tc.id, tc.summary, time.Now())
			if tc.summary == " " {
				// A whitespace summary is a real value, not an absent one: the model
				// answered, and refusing it here would make a legitimate (if odd)
				// answer indistinguishable from a broken call.
				require.NoError(t, err)
				assert.True(t, wrote)
				return
			}
			require.Error(t, err)
			assert.False(t, wrote)
		})
	}
}
