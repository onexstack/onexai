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

	"github.com/onexstack/onexai/internal/chat/model"
)

// RuleStore reads the stored command rules.
type RuleStore interface {
	ListEnabled(ctx context.Context) ([]*model.AgentCommandM, error)
}

// UnionRuleStore is the additive union of several projections.
//
// It fails as a whole when one source fails: the resolver keeps the last
// complete snapshot on error, and a partial union would silently revoke the
// grants of whichever source happened to fail.
type UnionRuleStore []RuleStore

// NewRuleStore composes the projections a deployment reads.
func NewRuleStore(stores ...RuleStore) RuleStore { return UnionRuleStore(stores) }

// ListEnabled returns every enabled rule from every source.
func (s UnionRuleStore) ListEnabled(ctx context.Context) ([]*model.AgentCommandM, error) {
	var out []*model.AgentCommandM
	for _, store := range s {
		if store == nil {
			continue
		}
		rows, err := store.ListEnabled(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}
