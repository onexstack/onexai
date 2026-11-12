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

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// seedSession writes one session row and returns the store over it.
//
// Hooks are skipped: SessionM.AfterCreate mints a chat id, and these tests are
// about which columns an update touches, not about id minting.
func seedSession(t *testing.T, sessionID, refs string) (*sessionStore, *gorm.DB) {
	t.Helper()

	db := newStoreTestDB(t, &model.SessionM{})
	row := &model.SessionM{
		SessionID:   sessionID,
		SessionType: model.SessionTypeNormalGroup,
		Refs:        refs,
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(row).Error; err != nil {
		t.Fatalf("seed the session row: %v", err)
	}
	return newSessionStore(&store{db: db}), db
}

// readSession reads a row back by its session id.
func readSession(t *testing.T, db *gorm.DB, sessionID string) *model.SessionM {
	t.Helper()

	var got model.SessionM
	if err := db.Where("session_id = ?", sessionID).First(&got).Error; err != nil {
		t.Fatalf("read the session row back: %v", err)
	}
	return &got
}

// TestSetRefsWritesTheDeclaration pins the shape /agent depends on: one column,
// named, holding exactly the JSON array it was given.
//
// It is the only thing a session row stores about agents. The resolved result
// used to be written beside it by the scheduler and read back by whoever
// rendered a menu; that column is gone because the answer belongs to a speaker
// rather than to a conversation, so this is now the whole of the write side.
func TestSetRefsWritesTheDeclaration(t *testing.T) {
	ctx := context.Background()
	s, db := seedSession(t, "oc_group_1", `["@default"]`)

	if err := s.SetRefs(ctx, "oc_group_1", `["@peer","ops-agent"]`); err != nil {
		t.Fatalf("SetRefs() = %v", err)
	}

	if got := readSession(t, db, "oc_group_1").Refs; got != `["@peer","ops-agent"]` {
		t.Errorf("Refs = %q, want the declaration just written", got)
	}
}

// TestSetRefsIsScopedToOneSession pins that the write names its row. The where
// clause is the whole of it, and a statement that matched more than one
// conversation would re-point every group from whichever command ran last.
func TestSetRefsIsScopedToOneSession(t *testing.T) {
	ctx := context.Background()
	s, db := seedSession(t, "oc_group_1", `[]`)

	other := &model.SessionM{SessionID: "oc_group_2", SessionType: model.SessionTypeNormalGroup, Refs: `["@default"]`}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(other).Error; err != nil {
		t.Fatalf("seed the second session: %v", err)
	}

	if err := s.SetRefs(ctx, "oc_group_1", `["@peer"]`); err != nil {
		t.Fatalf("SetRefs() = %v", err)
	}

	if got := readSession(t, db, "oc_group_2").Refs; got != `["@default"]` {
		t.Errorf("the other conversation's Refs = %q, want it untouched", got)
	}
}

// TestSetRefsCanClearADeclaration pins what /agent --reset writes. An empty
// column is not the same as `["@default"]`: the first follows the conversation
// type's template and keeps following it when the template changes, the second
// freezes today's answer.
func TestSetRefsCanClearADeclaration(t *testing.T) {
	ctx := context.Background()
	s, db := seedSession(t, "oc_group_1", `["@peer"]`)

	if err := s.SetRefs(ctx, "oc_group_1", ""); err != nil {
		t.Fatalf("SetRefs() = %v", err)
	}

	if got := readSession(t, db, "oc_group_1").Refs; got != "" {
		t.Errorf("Refs = %q, want it cleared so the template applies again", got)
	}
}

// TestColumnWritesWithoutARowAreNoops pins the guards on the write: an unknown
// session id is a conversation the bot has not recorded yet, and there is
// nothing to update. Returning an error there would fail a command over a row
// that is about to be created anyway.
func TestColumnWritesWithoutARowAreNoops(t *testing.T) {
	ctx := context.Background()
	s, _ := seedSession(t, "oc_group_1", "")

	if err := s.SetRefs(ctx, "oc_absent", `["@peer"]`); err != nil {
		t.Errorf("SetRefs() on an absent row = %v, want a no-op", err)
	}
	if err := s.SetRefs(ctx, "", `["@peer"]`); err != nil {
		t.Errorf("SetRefs() with no session id = %v, want a no-op", err)
	}
}
