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

package di

import (
	"context"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
)

// fakeRuleSource is a RuleSource whose rule set can be swapped mid-test, which
// is what lets one test see both "before the rule existed" and "after it was
// written".
type fakeRuleSource struct{ rules []model.AuthzRuleM }

func (f *fakeRuleSource) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	out := make([]*model.AuthzRuleM, 0, len(f.rules))
	for i := range f.rules {
		out = append(out, &f.rules[i])
	}
	return int64(len(out)), out, nil
}

// TestRuleChangeReloadClearsCachedDecisions pins the one thing a policy reload
// cannot do by itself, and the condition both callers of authzRuleReloader
// share: flush when the rule set actually changed, either way the change
// arrived (the write API, a DBA's UPDATE, the seed SQL).
//
// # The bug this exists to prevent
//
// The result cache is keyed on the request and holds the *answer*. Writing a
// rule changes the *policy*. So reloading the policy leaves every cached answer
// untouched: a rule that grants access to a request shape that was decided while
// it was denied stays invisible until the entry ages out — up to
// authz.DefaultCacheTTL, five minutes, with no error and no log line. The
// symptom is "I wrote the rule and it didn't work", which invites the wrong fix
// (rewriting the rule) while the real one is a cache clear.
//
// # Why "changed" is the condition
//
// Clearing the cache on *every* reload would be just as wrong in the other
// direction — the periodic refresher fires every minute, and a timer firing is
// not evidence that anything changed, so flushing there would cut the cache's
// hit rate to near zero. RuleStore.Sync reports whether the snapshot moved, so
// both assertions below hold with one code path.
func TestRuleChangeReloadClearsCachedDecisions(t *testing.T) {
	ctx := context.Background()

	grant := model.AuthzRuleM{
		Name:         "onex-user-user-1",
		SubjectType:  "user",
		SubjectID:    []string{"user-1"},
		ResourceKind: "agent",
		ResourceName: []string{"onex-u-user-1"},
		Verbs:        []string{"read"},
		Effect:       "allow",
		Status:       model.StatusEnabled,
	}
	req := authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "user-1"},
		Verb:      authz.VerbRead,
		AgentName: "onex-u-user-1",
	}

	// Start with no rules — the state a user is in before provisioning.
	source := &fakeRuleSource{}
	ruleStore := opa.NewRuleStore(source)
	authorizer, err := opa.NewAuthorizer(ctx, ruleStore)
	require.NoError(t, err)
	cache, err := authz.NewCachedAuthorizer(authorizer, time.Minute)
	require.NoError(t, err)

	// Prime the cache under the empty policy.
	if _, _, err := cache.Authorize(ctx, req); err != nil {
		t.Fatalf("priming authorize: %v", err)
	}
	cache.Wait() // ristretto writes are async — wait before the next read.

	// A refresh that finds the same rule set changes nothing, so it must not
	// clear the cache: the stale answer is still served and the policy is
	// untouched.
	require.NoError(t, authzRuleReloader(ruleStore, authorizer, authzInvalidator(cache))(ctx))
	if decision, _, err := cache.Authorize(ctx, req); err != nil {
		t.Fatalf("authorize after a no-op refresh: %v", err)
	} else if decision == authz.DecisionAllow {
		t.Fatal("a refresh that found the same rules cleared the cache; every tick would then throw the cache away")
	}

	// The rule is written (provisioning, or an operator through /v1/authz-rules)
	// — and only then is the cache cleared, which is what makes the guarantee
	// "a written rule is in force for the next request" true.
	source.rules = []model.AuthzRuleM{grant}
	require.NoError(t, authzRuleReloader(ruleStore, authorizer, authzInvalidator(cache))(ctx))
	if decision, _, err := cache.Authorize(ctx, req); err != nil {
		t.Fatalf("authorize after rule write: %v", err)
	} else if decision != authz.DecisionAllow {
		t.Fatalf("decision = %v after the rule was written; the cached answer survived the reload", decision)
	}
}

// TestAuthzInvalidatorIsNilSafe pins the nil case: the cache is absent whenever
// it could not be built, and the reloader must then behave exactly as it did
// before the flush existed.
func TestAuthzInvalidatorIsNilSafe(t *testing.T) {
	if authzInvalidator(nil) != nil {
		t.Fatal("a nil cache must produce a nil hook, not a hook that panics when called")
	}
}
