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

package authzrule

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
)

// fakeStore is an in-memory Store. It stores copies, like a database, so a
// test cannot observe a value the service never wrote.
type fakeStore struct {
	mu    sync.Mutex
	rules map[string]*model.AuthzRuleM
}

func newFakeStore() *fakeStore { return &fakeStore{rules: map[string]*model.AuthzRuleM{}} }

func (f *fakeStore) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*model.AuthzRuleM, 0, len(f.rules))
	for _, r := range f.rules {
		out = append(out, ptr.Clone(r))
	}
	return int64(len(out)), out, nil
}

func (f *fakeStore) Get(_ context.Context, opts *where.Options) (*model.AuthzRuleM, error) {
	name, _ := opts.Filters["name"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return ptr.Clone(r), nil
}

func (f *fakeStore) Create(_ context.Context, rule *model.AuthzRuleM) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rules[rule.Name]; ok {
		return errors.New("duplicate key")
	}
	f.rules[rule.Name] = ptr.Clone(rule)
	return nil
}

func (f *fakeStore) Update(_ context.Context, rule *model.AuthzRuleM) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rules[rule.Name]; !ok {
		return errors.New("missing row")
	}
	f.rules[rule.Name] = ptr.Clone(rule)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, opts *where.Options) error {
	name, _ := opts.Filters["name"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rules, name)
	return nil
}

func validRule(name string) *model.AuthzRuleM {
	return &model.AuthzRuleM{
		Name:         name,
		SubjectType:  "user",
		SubjectID:    []string{"u_alice"},
		ResourceKind: "agent",
		ResourceName: []string{"default"},
		Verbs:        []string{"read"},
		Effect:       "allow",
		Status:       model.StatusEnabled,
		Owner:        "ops",
	}
}

// TestCreateGetUpdateDeleteRoundTrip exercises the CRUD against the in-memory
// store, including that the service, not the client, stamps the timestamps.
func TestCreateGetUpdateDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := New(st, nil)
	svc.now = func() time.Time { return time.UnixMilli(1700000000000) }

	rule := validRule("rule-1")
	rule.CreatedAt = 1 // client-supplied, must be overwritten
	rule.UpdatedAt = 1
	if err := svc.Create(ctx, rule); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rule.CreatedAt != 1700000000000 || rule.UpdatedAt != 1700000000000 {
		t.Fatalf("server must stamp timestamps, got created=%d updated=%d", rule.CreatedAt, rule.UpdatedAt)
	}

	got, found, err := svc.Get(ctx, "rule-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(got, rule) {
		t.Fatalf("round trip changed the rule:\n got %+v\nwant %+v", got, rule)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "rule-1" {
		t.Fatalf("List got %+v, want the one rule", list)
	}

	updated := ptr.Clone(got)
	updated.SubjectID = []string{"u_bob"}
	updated.UpdatedAt = 42 // overwritten
	svc.now = func() time.Time { return time.UnixMilli(1700000001000) }
	if err := svc.Update(ctx, updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _, _ := svc.Get(ctx, "rule-1")
	if got2.SubjectID[0] != "u_bob" || got2.CreatedAt != 1700000000000 || got2.UpdatedAt != 1700000001000 {
		t.Fatalf("Update did not preserve created_at or failed to restamp updated_at: %+v", got2)
	}

	if err := svc.Delete(ctx, "rule-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := svc.Get(ctx, "rule-1"); found {
		t.Fatal("rule still present after Delete")
	}
}

// TestListKeepsDisabledRules pins that the administration surface shows every
// row: a disabled rule is exactly what an operator re-enables, so hiding it
// would make the API useless for its purpose.
func TestListKeepsDisabledRules(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	disabled := validRule("off")
	disabled.Status = model.StatusDisabled
	_ = st.Create(ctx, disabled)
	_ = st.Create(ctx, validRule("on"))

	svc := New(st, nil)
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d rules, want both enabled and disabled", len(list))
	}
}

func TestDuplicateNameIsAValidationError(t *testing.T) {
	ctx := context.Background()
	svc := New(newFakeStore(), nil)
	if err := svc.Create(ctx, validRule("dup")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := svc.Create(ctx, validRule("dup"))
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "name" {
		t.Fatalf("duplicate name must be a name ValidationError, got %v", err)
	}
}

func TestUpdateAndDeleteMissingRuleReportNotFound(t *testing.T) {
	ctx := context.Background()
	svc := New(newFakeStore(), nil)

	if err := svc.Update(ctx, validRule("ghost")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update of a missing rule: got %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of a missing rule: got %v, want ErrNotFound", err)
	}
}

// TestWriteReloadsTheInMemoryRuleSet is the point of the whole feature: after a
// write returns, the rule set the OPA authorizer evaluates must already contain
// the change — not a refresh interval later. It wires the real RuleStore, so the
// assertion is about the actual memory the policy reads.
func TestWriteReloadsTheInMemoryRuleSet(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	ruleStore := opa.NewRuleStore(st) // st's List backs the snapshot
	svc := New(st, func(ctx context.Context) error { _, err := ruleStore.Sync(ctx); return err })

	if got := ruleStore.Snapshot(); len(got) != 0 {
		t.Fatalf("precondition: rule set should start empty, got %+v", got)
	}

	rule := validRule("live")
	if err := svc.Create(ctx, rule); err != nil {
		t.Fatalf("Create: %v", err)
	}
	snap := ruleStore.Snapshot()
	if len(snap) != 1 || snap[0].Name != "live" {
		t.Fatalf("after Create the in-memory set is %+v, want the new rule present immediately", snap)
	}

	updated := snap[0]
	updated.Effect = "deny"
	if err := svc.Update(ctx, &updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := ruleStore.Snapshot(); len(got) != 1 || got[0].Effect != "deny" {
		t.Fatalf("after Update the in-memory set is %+v, want effect=deny immediately", got)
	}

	if err := svc.Delete(ctx, "live"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := ruleStore.Snapshot(); len(got) != 0 {
		t.Fatalf("after Delete the in-memory set is %+v, want it empty immediately", got)
	}
}

// TestReloadFailureIsReportedWithoutLosingTheWrite pins the awkward case: the
// row is persisted but the in-memory set could not be refreshed. The error must
// say so, and the row must still be readable — a caller retrying the write
// would otherwise be surprised by a duplicate.
func TestReloadFailureIsReportedWithoutLosingTheWrite(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := New(st, func(context.Context) error { return errors.New("load failed") })

	err := svc.Create(ctx, validRule("persisted"))
	if err == nil || !strings.Contains(err.Error(), "rule persisted") {
		t.Fatalf("got %v, want an error saying the rule was persisted but not reloaded", err)
	}
	if _, found, _ := svc.Get(ctx, "persisted"); !found {
		t.Fatal("the write must be persisted even when the reload fails")
	}
}

// TestNilServiceReportsNotConfigured covers a deployment without a SQL rule
// store: the handler is built with a nil service and every call must refuse
// explicitly rather than panic.
func TestNilServiceReportsNotConfigured(t *testing.T) {
	ctx := context.Background()
	var svc *Service

	if _, _, err := svc.Get(ctx, "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Get on a nil service: got %v, want ErrNotConfigured", err)
	}
	if err := svc.Create(ctx, validRule("x")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Create on a nil service: got %v, want ErrNotConfigured", err)
	}
	if err := svc.Delete(ctx, "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Delete on a nil service: got %v, want ErrNotConfigured", err)
	}
}
