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

package opa

import (
	"context"
	"log/slog"
	"reflect"
	"sync"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexstack/pkg/astore"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// RuleSource is the minimal read capability RuleStore needs. It is declared
// here (rather than reusing the shared resource-store interface) so the authz
// package tree does not depend on the whole store surface, and so the only
// implementation that must exist is the SQL store that actually backs the rule
// table.
type RuleSource interface {
	List(ctx context.Context, opts *where.Options) (int64, []*model.AuthzRuleM, error)
}

// RuleStore holds the in-memory AuthzRuleM set as an immutable snapshot that is
// replaced whole. The policy data (data.authz.rules) is rebuilt from Snapshot
// after every Sync, so a reader never observes a half-applied rule set.
type RuleStore struct {
	source RuleSource
	rules  *astore.Store[[]model.AuthzRuleM]

	// syncMu serializes Sync. The change detection compares the generation
	// before a refresh with the one after it, which is only exact while no
	// other Sync can publish in between.
	syncMu sync.Mutex
	// emptyWarned records that the emptiness currently in force was already
	// reported. The refresher runs every RuleTableRefreshInterval, and an empty
	// rule table denies every request, so warning on each pass would turn one
	// misconfiguration into a logged line every minute.
	emptyWarned bool
}

// NewRuleStore creates an empty RuleStore reading from source. Call Sync
// (directly, or via the periodic refresher) to populate it.
func NewRuleStore(source RuleSource) *RuleStore {
	s := &RuleStore{source: source}
	s.rules = astore.New(astore.Options{Name: "authz.rules"}, s.load)
	return s
}

// load reads and filters one generation of the rule set. It runs inside the
// store's refresh, so it must not mutate what it returns.
func (s *RuleStore) load(ctx context.Context) ([]model.AuthzRuleM, error) {
	_, records, err := s.source.List(ctx, where.L(-1))
	if err != nil {
		return nil, err
	}

	rules := make([]model.AuthzRuleM, 0, len(records))
	for _, r := range records {
		if r == nil || r.Name == "" || !r.Enabled() {
			continue
		}
		// A rule written before the kind was renamed still says "workspace".
		// Normalizing on load is what lets the rename ship without a data
		// migration: the policy document only ever sees the current spelling.
		// The record is copied rather than edited in place — the source may hand
		// out pointers into its own cache.
		rule := *r
		if rule.ResourceKind == string(authz.LegacyResourceKindWorkspace) {
			rule.ResourceKind = string(authz.ResourceKindNamespace)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Sync loads every rule from the source and atomically swaps them into the
// store. A fetch error leaves the previous snapshot (or an empty one, at
// startup) in place, so a transient database outage does not wipe out the
// currently-enforced rule set. Rows that are nameless or disabled are dropped
// here; the loader reports what is stored.
//
// It reports whether the rule set actually changed. Callers use that to decide
// whether cached decisions are worth dropping: re-preparing the query and
// clearing every cached answer on a timer that fired with nothing new would
// defeat the cache, while never clearing it lets a revoked grant survive for
// the whole TTL.
func (s *RuleStore) Sync(ctx context.Context) (bool, error) {
	if s == nil || s.source == nil {
		return false, nil
	}

	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	before, hadBefore := s.rules.Current()
	if err := s.rules.Refresh(ctx); err != nil {
		// The store already logged the failure and kept the previous snapshot.
		return false, err
	}
	after, ok := s.rules.Current()
	if !ok {
		return false, nil
	}

	rules := after.Value
	changed := !hadBefore || !reflect.DeepEqual(before.Value, rules)

	alreadyWarned := s.emptyWarned
	s.emptyWarned = len(rules) == 0

	// An empty rule table is not a quiet state: the policy has no rule to match,
	// so every request falls through to fail-closed and is denied. Nothing else
	// in the process says so at a level anyone would see, which turns a missing
	// configuration into a stream of "you lack permission" for users who have
	// exactly the permissions the (empty) table grants. Warned once per
	// transition so a long outage does not flood the log.
	switch {
	case len(rules) == 0 && !alreadyWarned:
		slog.WarnContext(ctx,
			"authz.RuleStore: no authorization rules are loaded; every request will be denied (fail-closed)",
			"count", 0)
	case len(rules) > 0 && alreadyWarned:
		slog.InfoContext(ctx, "authz.RuleStore: authorization rules are loaded again", "count", len(rules))
	default:
		slog.DebugContext(ctx, "authz.RuleStore: synced rules", "count", len(rules), "changed", changed)
	}
	return changed, nil
}

// Snapshot returns a defensive copy of the currently active rule set, for
// building the OPA data.authz.rules document.
func (s *RuleStore) Snapshot() []model.AuthzRuleM {
	if s == nil {
		return nil
	}
	gen, ok := s.rules.Current()
	if !ok {
		return nil
	}
	out := make([]model.AuthzRuleM, len(gen.Value))
	copy(out, gen.Value)
	return out
}
