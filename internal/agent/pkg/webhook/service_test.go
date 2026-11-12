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

package webhook

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// fakeStore is a WebhookStore whose absent answer is configurable: the SQL table
// store returns agentstore.ErrNotFound, the control-plane store returns
// (nil, nil). Both must be treated as "the hook does not exist".
type fakeStore struct {
	rows      map[string]*model.WebhookM
	absentNil bool
}

func newFakeStore(absentNil bool, rows ...*model.WebhookM) *fakeStore {
	store := &fakeStore{rows: map[string]*model.WebhookM{}, absentNil: absentNil}
	for _, row := range rows {
		store.rows[row.Name] = row
	}
	return store
}

func (f *fakeStore) Create(_ context.Context, obj *model.WebhookM) error {
	f.rows[obj.Name] = obj
	return nil
}

func (f *fakeStore) Update(_ context.Context, obj *model.WebhookM) error {
	f.rows[obj.Name] = obj
	return nil
}

func (f *fakeStore) Delete(_ context.Context, opts *where.Options) error {
	delete(f.rows, nameOf(opts))
	return nil
}

func (f *fakeStore) Get(_ context.Context, opts *where.Options) (*model.WebhookM, error) {
	row, ok := f.rows[nameOf(opts)]
	if !ok {
		if f.absentNil {
			return nil, nil
		}
		return nil, agentstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeStore) List(_ context.Context, _ *where.Options) (int64, []*model.WebhookM, error) {
	rows := make([]*model.WebhookM, 0, len(f.rows))
	for _, row := range f.rows {
		rows = append(rows, row)
	}
	return int64(len(rows)), rows, nil
}

func nameOf(opts *where.Options) string {
	name, _ := opts.Filters["name"].(string)
	return name
}

func validHook(name string) *model.WebhookM {
	return &model.WebhookM{
		Name:   name,
		URL:    "https://example.com/hook",
		Events: model.StringList{model.EventOnEnd},
	}
}

func TestCreateTreatsAbsentHookAsMissing(t *testing.T) {
	// Regression: the control-plane store answers (nil, nil) for an absent row,
	// and Create used to read that as "already exists", so every create on such
	// a deployment failed with a 400.
	for _, absentNil := range []bool{true, false} {
		svc := New(newFakeStore(absentNil), nil, nil)
		hook := validHook("hook")
		if err := svc.Create(context.Background(), hook); err != nil {
			t.Fatalf("absentNil=%v: Create() error = %v, want nil", absentNil, err)
		}
		if hook.TimeoutSeconds != defaultTimeoutSeconds {
			t.Errorf("absentNil=%v: timeout = %d, want the default %d",
				absentNil, hook.TimeoutSeconds, defaultTimeoutSeconds)
		}
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	for _, absentNil := range []bool{true, false} {
		svc := New(newFakeStore(absentNil, validHook("hook")), nil, nil)
		err := svc.Create(context.Background(), validHook("hook"))
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.Field != "name" {
			t.Fatalf("absentNil=%v: Create() error = %v, want a name ValidationError", absentNil, err)
		}
	}
}

func TestGetMissingReportsNotFound(t *testing.T) {
	for _, absentNil := range []bool{true, false} {
		svc := New(newFakeStore(absentNil), nil, nil)
		row, found, err := svc.Get(context.Background(), "missing")
		if err != nil || found || row != nil {
			t.Fatalf("absentNil=%v: Get() = (%v, %v, %v), want (nil, false, nil)", absentNil, row, found, err)
		}
	}
}

func TestUpdateMissingReportsNotFound(t *testing.T) {
	for _, absentNil := range []bool{true, false} {
		svc := New(newFakeStore(absentNil), nil, nil)
		err := svc.Update(context.Background(), validHook("missing"))
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("absentNil=%v: Update() error = %v, want ErrNotFound", absentNil, err)
		}
	}
}

func TestDeleteMissingReportsNotFound(t *testing.T) {
	for _, absentNil := range []bool{true, false} {
		svc := New(newFakeStore(absentNil), nil, nil)
		err := svc.Delete(context.Background(), "missing")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("absentNil=%v: Delete() error = %v, want ErrNotFound", absentNil, err)
		}
	}
}
