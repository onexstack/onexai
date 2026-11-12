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
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestSessionGetReadsTheCurrentTransaction pins that Get goes through the
// store's DB provider rather than a raw handle captured at construction. The
// inherited generic Get reads the transaction out of ctx; an override that
// dereferenced s.db would miss the row it had just written in the same unit of
// work.
func TestSessionGetReadsTheCurrentTransaction(t *testing.T) {
	ctx := context.Background()
	db := newStoreTestDB(t, &model.SessionM{})
	ds := &store{db: db}
	s := newSessionStore(ds)

	err := ds.TX(ctx, func(ctx context.Context) error {
		row := &model.SessionM{SessionID: "oc_tx_1", SessionType: model.SessionTypeNormalGroup}
		if err := s.Create(ctx, row); err != nil {
			return err
		}

		got, err := s.Get(ctx, where.F("session_id", "oc_tx_1"))
		if err != nil {
			return err
		}
		if got.SessionID != "oc_tx_1" {
			t.Errorf("Get() inside TX returned %q, want the row just written", got.SessionID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TX() = %v", err)
	}
}

// TestSessionGetMissesWithTheSentinel pins the contract every caller relies on:
// a missing row is reported as ErrRecordNotFound (the gorm sentinel), not as an
// opaque error.
func TestSessionGetMissesWithTheSentinel(t *testing.T) {
	ctx := context.Background()
	db := newStoreTestDB(t, &model.SessionM{})
	s := newSessionStore(&store{db: db})

	if _, err := s.Get(ctx, where.F("session_id", "oc_absent")); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("Get() on an absent row = %v, want ErrRecordNotFound", err)
	}
}
