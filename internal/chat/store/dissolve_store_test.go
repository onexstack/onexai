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

	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestListDismissableSelectsClosedUnsavedGroups is the query every clause of
// which is load-bearing.
//
// It is the one thing standing between a sweep and a group somebody is still
// using, or one somebody deliberately kept — and neither of those failures
// errors. They simply dissolve a conversation that should have stayed.
func TestListDismissableSelectsClosedUnsavedGroups(t *testing.T) {
	db := newStoreTestDB(t, &model.SessionM{})
	s := &sessionStore{db: db}
	ctx := context.Background()
	now := time.Now()

	past := now.Add(-2 * time.Hour)
	recent := now.Add(-time.Minute)

	rows := []*model.SessionM{
		// Due: closed long ago, never saved, never dissolved.
		{SessionID: "oc_due", ChatID: "oc_due", ClosedAt: &past},
		// Not due: closed, but somebody ran /save.
		{SessionID: "oc_saved", ChatID: "oc_saved", ClosedAt: &past, SavedAt: &past},
		// Not due: already dissolved. This is the sweep's idempotence — without
		// it, an interrupted pass would dissolve the same group for ever.
		{SessionID: "oc_done", ChatID: "oc_done", ClosedAt: &past, DismissedAt: &past},
		// Not due: never closed. A conversation nobody closed is still in use.
		{SessionID: "oc_open", ChatID: "oc_open"},
		// Not due: closed, but inside the grace period.
		{SessionID: "oc_recent", ChatID: "oc_recent", ClosedAt: &recent},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.SessionID, err)
		}
	}

	got, err := s.ListDismissable(ctx, now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListDismissable: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "oc_due" {
		var ids []string
		for _, r := range got {
			ids = append(ids, r.SessionID)
		}
		t.Fatalf("ListDismissable returned %v, want only oc_due", ids)
	}
}

// TestMarkDismissedClaimsTheRowOnce pins the predicate that makes this a claim
// rather than a write: two replicas sweeping at the same moment must not both
// believe they were the one that dissolved the group.
func TestMarkDismissedClaimsTheRowOnce(t *testing.T) {
	db := newStoreTestDB(t, &model.SessionM{})
	s := &sessionStore{db: db}
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour)
	if err := db.Create(&model.SessionM{SessionID: "oc_1", ChatID: "oc_1", ClosedAt: &past}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.MarkDismissed(ctx, "oc_1", time.Now()); err != nil {
		t.Fatalf("MarkDismissed: %v", err)
	}

	var got model.SessionM
	if err := db.Where("session_id = ?", "oc_1").First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DismissedAt == nil {
		t.Fatal("dismissed_at was not written")
	}
	first := ptr.From(got.DismissedAt)

	// A second mark is a no-op, which is what the sweep's ordering relies on:
	// the row it would clobber is the record that the group is already gone.
	later := first.Add(time.Hour)
	if err := s.MarkDismissed(ctx, "oc_1", later); err != nil {
		t.Fatalf("second MarkDismissed: %v", err)
	}
	if err := db.Where("session_id = ?", "oc_1").First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.DismissedAt.Equal(first) {
		t.Errorf("dismissed_at moved from %v to %v", first, got.DismissedAt)
	}
}

// TestArchiveIsIdempotent pins the property the sweep's retry depends on: a
// pass interrupted between the copy and the mark must be able to finish the job
// rather than fail on its own earlier work.
func TestArchiveIsIdempotent(t *testing.T) {
	db := newStoreTestDB(t, &model.ChatMessageM{})
	s := &chatMessageStore{db: db}
	ctx := context.Background()

	msgs := []*model.ChatMessageM{
		{MessageID: "m1", GroupID: "oc_1", Content: "first"},
		{MessageID: "m2", GroupID: "oc_1", Content: "second"},
	}

	n, err := s.Archive(ctx, msgs)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if n != 2 {
		t.Errorf("first Archive wrote %d rows, want 2", n)
	}

	// The same messages again: an interrupted pass retrying.
	n, err = s.Archive(ctx, msgs)
	if err != nil {
		t.Fatalf("second Archive: %v", err)
	}
	if n != 0 {
		t.Errorf("second Archive wrote %d rows, want none", n)
	}

	var back []model.ChatMessageM
	if err := db.Where("group_id = ?", "oc_1").Order("time ASC").Find(&back).Error; err != nil {
		t.Fatalf("read back the archive: %v", err)
	}
	if len(back) != 2 {
		t.Errorf("the archive holds %d messages, want 2", len(back))
	}
	// Oldest first, which is the order a reader expects of a conversation.
	if back[0].MessageID != "m1" {
		t.Errorf("ListByGroup returned %s first, want m1", back[0].MessageID)
	}
}

// TestArchiveKeepsWhatTheSweepCopied pins the columns the archive exists for.
//
// It is the only record left once the group is gone, so a field the sweep
// collected and this dropped is a fact nobody can recover.
func TestArchiveKeepsWhatTheSweepCopied(t *testing.T) {
	db := newStoreTestDB(t, &model.ChatMessageM{})
	s := &chatMessageStore{db: db}
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	_, err := s.Archive(ctx, []*model.ChatMessageM{{
		MessageID: "m1", GroupID: "oc_1", SenderID: "go.kong", SenderName: "Ada",
		Content: "看图", Time: &at, ParentID: "m0", Images: `[{"asset_id":"img_a"}]`,
	}})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	var back []model.ChatMessageM
	if err := db.Where("group_id = ?", "oc_1").Find(&back).Error; err != nil {
		t.Fatalf("read back the archive: %v", err)
	}
	if len(back) != 1 {
		t.Fatal("the message was not archived")
	}
	got := back[0]
	if got.SenderID != "go.kong" || got.SenderName != "Ada" || got.Content != "看图" {
		t.Errorf("the text fields were lost: %+v", got)
	}
	if got.ParentID != "m0" {
		t.Errorf("ParentID = %q, want the quoted message it answered", got.ParentID)
	}
	if got.Time == nil || !got.Time.Equal(at) {
		t.Errorf("Time = %v, want %v", got.Time, at)
	}
	if got.Images != `[{"asset_id":"img_a"}]` {
		t.Errorf("Images = %q, want the references kept", got.Images)
	}
}
