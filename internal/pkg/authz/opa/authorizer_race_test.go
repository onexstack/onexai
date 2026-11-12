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
	"sync"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// lockedRuleSource is a RuleSource safe to mutate while another goroutine is
// syncing. The package's fakeRuleSource is deliberately lock-free because every
// other test uses it single-threaded; reusing it here would put a data race in
// the test itself and hide the one under test.
type lockedRuleSource struct {
	mu    sync.Mutex
	rules []*model.AuthzRuleM
}

func (l *lockedRuleSource) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := append([]*model.AuthzRuleM(nil), l.rules...)
	return int64(len(rows)), rows, nil
}

func (l *lockedRuleSource) set(rules []*model.AuthzRuleM) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rules = rules
}

// TestAuthorizeToleratesAConcurrentReload pins the swap that the periodic
// refresher (every RuleTableRefreshInterval) and the rule-write path perform
// while request goroutines are evaluating the previous policy.
//
// The prepared query is replaced as a whole, and a reader must either see the
// whole old one or the whole new one — never a partially built query, and never
// a nil one. Reproducing the original defect needs the race detector, which
// needs cgo, which this project's local test environment cannot use (see
// CLAUDE.md's note on go-m1cpu); the test therefore asserts the observable
// property rather than the memory model: every call returns a decision, and the
// decision is never valid-but-undefined.
func TestAuthorizeToleratesAConcurrentReload(t *testing.T) {
	const (
		writers  = 4
		readers  = 8
		perLoop  = 40
		ruleName = "allow-agent"
	)

	allow := func(status string) []*model.AuthzRuleM {
		return []*model.AuthzRuleM{{
			Name: ruleName, SubjectType: "user", SubjectID: []string{"u1"},
			ResourceKind: "agent", ResourceName: []string{"a"},
			Verbs: []string{"read"}, Effect: "allow", Status: status,
		}}
	}

	// Disabled <-> enabled flips the answer, so a reader that observes a
	// half-applied reload is likely to report the wrong decision rather than
	// merely a stale one.
	source := &lockedRuleSource{rules: allow(model.StatusEnabled)}
	store := NewRuleStore(source)
	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	req := authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u1"},
		Verb:      authz.VerbRead,
		AgentName: "a",
	}

	var wg sync.WaitGroup
	reloadErr := make(chan error, writers*perLoop)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < perLoop; n++ {
				status := model.StatusEnabled
				if (i+n)%2 == 0 {
					status = model.StatusDisabled
				}
				source.set(allow(status))
				if _, err := store.Sync(context.Background()); err != nil {
					reloadErr <- err
					return
				}
				if err := a.Reload(context.Background()); err != nil {
					reloadErr <- err
					return
				}
			}
		}(i)
	}

	// NoOpinion is a legitimate answer here: a disabled rule is dropped by Sync,
	// which leaves the store empty. What must never happen is an evaluation
	// error or an undefined Decision.
	bad := make(chan string, readers*perLoop)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < perLoop; n++ {
				decision, _, err := a.Authorize(context.Background(), req)
				if err != nil {
					bad <- "authorize error: " + err.Error()
					return
				}
				switch decision {
				case authz.DecisionAllow, authz.DecisionDeny, authz.DecisionNoOpinion:
				default:
					bad <- "undefined decision: " + decision.String()
					return
				}
			}
		}()
	}

	wg.Wait()
	close(reloadErr)
	close(bad)

	for err := range reloadErr {
		t.Fatalf("concurrent reload failed: %v", err)
	}
	for failure := range bad {
		t.Fatalf("concurrent authorize failed while the policy was being swapped: %s", failure)
	}
}
