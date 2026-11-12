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
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// fakeSecretStore models both store behaviors: the control-plane store answers
// (nil, nil) for an absent row, the SQL store returns ErrNotFound.
type fakeSecretStore struct {
	rows      map[string]*model.SecretM
	absentNil bool
	creates   int
	updates   int
	deletes   int
}

func newFakeSecretStore(rows ...*model.SecretM) *fakeSecretStore {
	store := &fakeSecretStore{rows: map[string]*model.SecretM{}, absentNil: true}
	for _, row := range rows {
		store.rows[row.SecretID] = row
	}
	return store
}

func (f *fakeSecretStore) Create(_ context.Context, obj *model.SecretM) error {
	if _, exists := f.rows[obj.SecretID]; exists {
		return errors.New("already exists")
	}
	f.rows[obj.SecretID] = obj
	f.creates++
	return nil
}

func (f *fakeSecretStore) Update(_ context.Context, obj *model.SecretM) error {
	f.rows[obj.SecretID] = obj
	f.updates++
	return nil
}

func (f *fakeSecretStore) Delete(_ context.Context, opts *where.Options) error {
	delete(f.rows, secretIDOf(opts))
	f.deletes++
	return nil
}

func (f *fakeSecretStore) Get(_ context.Context, opts *where.Options) (*model.SecretM, error) {
	row, ok := f.rows[secretIDOf(opts)]
	if !ok {
		if f.absentNil {
			return nil, nil
		}
		return nil, agentstore.ErrNotFound
	}
	return row, nil
}

func secretIDOf(opts *where.Options) string {
	id, _ := opts.Filters["secret_id"].(string)
	return id
}

func TestCreateMaterializesToken(t *testing.T) {
	store := newFakeStore(true)
	secrets := newFakeSecretStore()
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.AuthToken = "tok-123"
	if err := svc.Create(context.Background(), hook); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := managedTokenSecretID("hook")
	if hook.SecretRef != id {
		t.Errorf("SecretRef = %q, want the managed id %q", hook.SecretRef, id)
	}
	if hook.AuthToken != "" {
		t.Errorf("AuthToken = %q, want it cleared after materialization", hook.AuthToken)
	}
	row := secrets.rows[id]
	if row == nil {
		t.Fatalf("managed secret %q was not created", id)
	}
	if row.SecretKey != "tok-123" {
		t.Errorf("stored token = %q, want tok-123", row.SecretKey)
	}
	if row.Kind != model.SecretKindWebhook {
		t.Errorf("secret kind = %q, want %q", row.Kind, model.SecretKindWebhook)
	}
	if store.rows["hook"].SecretRef != id {
		t.Errorf("persisted hook SecretRef = %q, want %q", store.rows["hook"].SecretRef, id)
	}
}

func TestUpdateRotatesManagedTokenInPlace(t *testing.T) {
	store := newFakeStore(true)
	secrets := newFakeSecretStore()
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.AuthToken = "old"
	if err := svc.Create(context.Background(), hook); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	next := validHook("hook")
	next.AuthToken = "new"
	if err := svc.Update(context.Background(), next); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	id := managedTokenSecretID("hook")
	if next.SecretRef != id {
		t.Errorf("SecretRef = %q, want %q", next.SecretRef, id)
	}
	if len(secrets.rows) != 1 {
		t.Errorf("secret rows = %d, want 1 (rotation must reuse the id)", len(secrets.rows))
	}
	if secrets.rows[id].SecretKey != "new" {
		t.Errorf("stored token = %q, want new", secrets.rows[id].SecretKey)
	}
	if secrets.creates != 1 || secrets.updates != 1 {
		t.Errorf("creates/updates = %d/%d, want 1/1", secrets.creates, secrets.updates)
	}
}

func TestUpdateToNoAuthRetiresManagedToken(t *testing.T) {
	store := newFakeStore(true)
	secrets := newFakeSecretStore()
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.AuthToken = "old"
	if err := svc.Create(context.Background(), hook); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	next := validHook("hook")
	if err := svc.Update(context.Background(), next); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if next.SecretRef != "" {
		t.Errorf("SecretRef = %q, want empty", next.SecretRef)
	}
	if _, ok := secrets.rows[managedTokenSecretID("hook")]; ok {
		t.Error("the managed secret should be deleted when auth is removed")
	}
}

func TestDeleteRetiresManagedToken(t *testing.T) {
	store := newFakeStore(true)
	secrets := newFakeSecretStore()
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.AuthToken = "tok"
	if err := svc.Create(context.Background(), hook); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := svc.Delete(context.Background(), "hook"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, ok := secrets.rows[managedTokenSecretID("hook")]; ok {
		t.Error("the managed secret should be deleted with the hook")
	}
}

func TestUpdateToUserSecretKeepsIt(t *testing.T) {
	store := newFakeStore(true)
	userSecret := &model.SecretM{SecretID: "user-secret", SecretKey: "k", Kind: model.SecretKindAKSK}
	secrets := newFakeSecretStore(userSecret)
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.SecretRef = "user-secret"
	if err := svc.Create(context.Background(), hook); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	next := validHook("hook")
	if err := svc.Update(context.Background(), next); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if _, ok := secrets.rows["user-secret"]; !ok {
		t.Error("a user-created secret must not be deleted by the hook lifecycle")
	}
}

func TestCreateRefusesToOverwriteForeignKey(t *testing.T) {
	store := newFakeStore(true)
	id := managedTokenSecretID("hook")
	secrets := newFakeSecretStore(&model.SecretM{
		SecretID: id, SecretKey: "someone-else", Kind: model.SecretKindAKSK,
	})
	svc := New(store, secrets, nil)

	hook := validHook("hook")
	hook.AuthToken = "tok"
	err := svc.Create(context.Background(), hook)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "authToken" {
		t.Fatalf("Create() error = %v, want an authToken ValidationError", err)
	}
}

func TestCreateRequiresSecretStoreForToken(t *testing.T) {
	svc := New(newFakeStore(true), nil, nil)
	hook := validHook("hook")
	hook.AuthToken = "tok"
	if err := svc.Create(context.Background(), hook); !errors.Is(err, ErrSecretStoreNotConfigured) {
		t.Fatalf("Create() error = %v, want ErrSecretStoreNotConfigured", err)
	}
}

func TestCreateRejectsOversizedToken(t *testing.T) {
	svc := New(newFakeStore(true), newFakeSecretStore(), nil)
	hook := validHook("hook")
	hook.AuthToken = strings.Repeat("x", authTokenMaxLen+1)
	err := svc.Create(context.Background(), hook)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "authToken" {
		t.Fatalf("Create() error = %v, want an authToken ValidationError", err)
	}
}

func TestCreateRejectsTokenAndSecretRefTogether(t *testing.T) {
	svc := New(newFakeStore(true), newFakeSecretStore(), nil)
	hook := validHook("hook")
	hook.AuthToken = "tok"
	hook.SecretRef = "some-secret"
	err := svc.Create(context.Background(), hook)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "authToken" {
		t.Fatalf("Create() error = %v, want an authToken ValidationError", err)
	}
}

func TestCreateRejectsAnotherHooksManagedToken(t *testing.T) {
	otherID := managedTokenSecretID("other")
	secrets := newFakeSecretStore(&model.SecretM{
		SecretID: otherID, SecretKey: "k", Kind: model.SecretKindWebhook,
	})
	svc := New(newFakeStore(true), secrets, nil)

	hook := validHook("hook")
	hook.SecretRef = otherID
	err := svc.Create(context.Background(), hook)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "secretRef" {
		t.Fatalf("Create() error = %v, want a secretRef ValidationError", err)
	}
}
