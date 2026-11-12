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

package controlplanestore

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

type staticRuleStore struct {
	rows []*model.AgentCommandM
	err  error
}

func (s staticRuleStore) ListEnabled(context.Context) ([]*model.AgentCommandM, error) {
	return s.rows, s.err
}

func TestUnionRuleStoreReturnsEverySource(t *testing.T) {
	store := NewRuleStore(
		staticRuleStore{rows: []*model.AgentCommandM{{Name: "agentbinding/a"}}},
		staticRuleStore{rows: []*model.AgentCommandM{{Name: "clusteragentbinding/b"}}},
	)

	rows, err := store.ListEnabled(context.Background())
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rules, want 2", len(rows))
	}
	if rows[0].Name != "agentbinding/a" || rows[1].Name != "clusteragentbinding/b" {
		t.Errorf("rules = [%q, %q]", rows[0].Name, rows[1].Name)
	}
}

func TestUnionRuleStoreFailsAsAWhole(t *testing.T) {
	want := errors.New("control plane unavailable")
	store := NewRuleStore(
		staticRuleStore{rows: []*model.AgentCommandM{{Name: "agentbinding/a"}}},
		staticRuleStore{err: want},
	)

	rows, err := store.ListEnabled(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if rows != nil {
		t.Errorf("rules = %v, want nil: a partial union would revoke one source's grants", rows)
	}
}

func TestUnionRuleStoreSkipsNilStores(t *testing.T) {
	store := NewRuleStore(nil, staticRuleStore{})
	rows, err := store.ListEnabled(context.Background())
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rules, want 0", len(rows))
	}
}
