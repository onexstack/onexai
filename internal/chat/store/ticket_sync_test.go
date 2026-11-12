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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/model"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// TestListForSyncFiltersByGroupStatusAndSummary covers the three conditions that
// make a ticket a candidate.
//
// The last one is the newest and the least obvious: a closing summary that is a
// write-up. The model answers with a brief marker for tickets it considers not
// worth recording, and those must not reach a consumer that would publish them as
// questions with nothing under them.
func TestListForSyncFiltersByGroupStatusAndSummary(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	now := time.Now()

	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-in", Title: "网关报错", Status: model.TicketStatusClosed,
		GroupID: "onex", ReporterOpenID: "ou_reporter", Summary: "## 问题现象\n…", UpdatedAt: now,
	})
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-other-group", Title: "别的线", Status: model.TicketStatusClosed,
		GroupID: "eks", Summary: "## 问题现象\n…", UpdatedAt: now,
	})
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-open", Title: "还开着", Status: model.TicketStatusOpen,
		GroupID: "onex", Summary: "## 问题现象\n…", UpdatedAt: now,
	})
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-no-summary", Title: "总结还没生成", Status: model.TicketStatusClosed,
		GroupID: "onex", UpdatedAt: now,
	})
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-brief", Title: "纯咨询", Status: model.TicketStatusClosed,
		GroupID: "onex", Summary: chatknown.SummaryBrief, UpdatedAt: now,
	})

	got, err := s.ListForSync(context.Background(), []string{"onex"}, []string{"resolved", "closed"}, time.Time{}, 0)
	require.NoError(t, err)

	ids := make([]string, 0, len(got))
	for _, tk := range got {
		ids = append(ids, tk.TicketID)
	}
	assert.Equal(t, []string{"tkt-in"}, ids,
		"only a closed ticket of the right group with a real write-up is a candidate")

	// The reporter's app-scoped id has to survive the round trip: it is the only
	// handle the consumer can look a person up by.
	require.Len(t, got, 1)
	assert.Equal(t, "ou_reporter", got[0].ReporterOpenID)
	assert.Equal(t, "## 问题现象\n…", got[0].Summary)
}

// TestListForSyncWalksUpdatedAt is the watermark's contract, and the reason the
// query no longer needs the handling log.
//
// A summary write moves updated_at (see UpdateSummary), so everything the reader
// must see is reachable through one column. The old query asked the note table too,
// because a /mark entry did not move the ticket — that reason is gone with the
// write-up moving onto the ticket itself.
func TestListForSyncWalksUpdatedAt(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	ctx := context.Background()

	seen := time.Now().Add(-time.Hour)
	mustCreateTicket(t, s, &model.TicketM{
		TicketID: "tkt-old", Status: model.TicketStatusClosed, GroupID: "onex",
		Summary: "## 问题现象\n…", UpdatedAt: seen,
	})

	got, err := s.ListForSync(ctx, []string{"onex"}, []string{"closed"}, seen, 0)
	require.NoError(t, err)
	assert.Empty(t, got, "a ticket at the watermark has been seen already")
}

// TestListForSyncOrdersByUpdatedAtAndLimits pins the order and the limit
// together: a caller that fills a page and advances its watermark to the last
// row's updated_at would skip whatever sorted after it if the order did not match
// the value the watermark takes.
func TestListForSyncOrdersByUpdatedAtAndLimits(t *testing.T) {
	db := newStoreTestDB(t, &model.TicketM{})
	s := newTicketStore(&store{db: db})
	base := time.Now().Add(-time.Hour)

	for _, tc := range []struct {
		id string
		at time.Time
	}{
		{"tkt-b", base},
		{"tkt-a", base.Add(-time.Minute)},
		{"tkt-c", base.Add(time.Minute)},
	} {
		mustCreateTicket(t, s, &model.TicketM{
			TicketID: tc.id, Status: model.TicketStatusClosed, GroupID: "onex",
			Summary: "## 问题现象\n…", UpdatedAt: tc.at,
		})
	}

	got, err := s.ListForSync(context.Background(), []string{"onex"}, []string{"closed"}, time.Time{}, 2)
	require.NoError(t, err)
	require.Len(t, got, 2, "the limit must bound the page")
	assert.Equal(t, "tkt-a", got[0].TicketID, "oldest first")
	assert.Equal(t, "tkt-b", got[1].TicketID)
}

func mustCreateTicket(t *testing.T, s TicketStore, obj *model.TicketM) {
	t.Helper()
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = obj.UpdatedAt
	}
	if err := s.Create(context.Background(), obj); err != nil {
		t.Fatalf("create ticket %s: %v", obj.TicketID, err)
	}
}
