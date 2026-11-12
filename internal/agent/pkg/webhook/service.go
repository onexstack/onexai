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
	"fmt"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// Store is the persistence surface the Service writes through. It is the
// standard webhook table store and nothing else: hooks are read by the
// dispatcher and written by this API, neither of which wants the cached
// resource snapshot — a hook disabled a second ago must stop being called on
// the next run, not at the next refresh.
type Store interface {
	Create(ctx context.Context, obj *model.WebhookM) error
	Update(ctx context.Context, obj *model.WebhookM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.WebhookM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.WebhookM, error)
}

// Reloader rebuilds whatever in-memory view of the hooks a deployment keeps.
// The Service calls it after every successful write, so the change is visible
// to the next run rather than at the next periodic refresh. A nil Reloader
// (nothing caches them) makes the call a no-op.
type Reloader func(ctx context.Context) error

// ErrNotFound reports that the named hook does not exist.
var ErrNotFound = errors.New("webhook not found")

// ErrNotConfigured reports that no hook store was wired (the deployment is not
// SQL-backed), so the API cannot serve reads or writes.
var ErrNotConfigured = errors.New("webhook store is not configured")

// ErrSecretStoreNotConfigured reports that a write carried a plaintext
// credential but the deployment has nowhere to store it.
var ErrSecretStoreNotConfigured = errors.New(
	"webhook credentials need a secret store, and this deployment has none")

// SecretStore is the part of the credential store a hook write uses. It is
// narrower than the full store because a webhook only ever materializes or
// retires its own managed token.
type SecretStore interface {
	Create(ctx context.Context, obj *model.SecretM) error
	Update(ctx context.Context, obj *model.SecretM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.SecretM, error)
}

// Service is the hook administration object assembled at startup from the SQL
// loader and the shared reloader, and injected into the REST handler.
type Service struct {
	store   Store
	secrets SecretStore
	reload  Reloader
	// now is injectable so tests can assert stamps without sleeping.
	now func() time.Time
}

// New builds a Service over store. secrets may be nil when the deployment has
// no credential store: hooks that do not carry a plaintext authToken still
// work, and a write that does is refused explicitly. reload may be nil when
// nothing caches the hooks; writes then still persist, they just have nothing
// to refresh.
func New(store Store, secrets SecretStore, reload Reloader) *Service {
	return &Service{store: store, secrets: secrets, reload: reload, now: time.Now}
}

// Create validates and inserts a new hook, then reloads. The server stamps
// CreatedAt/UpdatedAt and applies the default timeout; a client-supplied
// timestamp is ignored.
func (s *Service) Create(ctx context.Context, hook *model.WebhookM) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	if err := Validate(hook); err != nil {
		return err
	}
	existing, err := s.lookup(ctx, hook.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		return &ValidationError{Field: "name", Message: fmt.Sprintf("webhook %q already exists", hook.Name)}
	}
	// Validate() has already refused authToken+secretRef together. From here on
	// the plaintext is moved into the credential store and the row keeps only
	// the reference.
	if err := s.materializeToken(ctx, hook); err != nil {
		return err
	}
	now := s.now().UnixMilli()
	hook.CreatedAt = now
	hook.UpdatedAt = now
	if err := s.store.Create(ctx, hook); err != nil {
		return err
	}
	return s.reloadNow(ctx)
}

// Get returns the named hook and whether it exists.
func (s *Service) Get(ctx context.Context, name string) (*model.WebhookM, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, ErrNotConfigured
	}
	row, err := s.lookup(ctx, name)
	if err != nil {
		return nil, false, err
	}
	return row, row != nil, nil
}

// List returns every hook, enabled or not: this is an administration surface, so
// a disabled hook is still something an operator must be able to see and
// re-enable.
func (s *Service) List(ctx context.Context) ([]*model.WebhookM, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	_, rows, err := s.store.List(ctx, where.L(-1))
	return rows, err
}

// lookup returns the named hook, or nil when it does not exist.
//
// The two stores this service can be wired to disagree on the absent answer:
// the SQL table store returns ErrNotFound, while the control-plane store returns
// (nil, nil). Normalising both here is load-bearing — treating a nil row as
// "exists" made every create answer 400 already exists on a control-plane
// deployment, and treating it as "exists" in Update dereferenced nil.
func (s *Service) lookup(ctx context.Context, name string) (*model.WebhookM, error) {
	row, err := s.store.Get(ctx, where.F("name", name))
	if errors.Is(err, agentstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// Update validates and overwrites the named hook, preserving its CreatedAt,
// then reloads. A missing hook is ErrNotFound.
func (s *Service) Update(ctx context.Context, hook *model.WebhookM) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	if err := Validate(hook); err != nil {
		return err
	}
	existing, err := s.lookup(ctx, hook.Name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, hook.Name)
	}
	if err := s.materializeToken(ctx, hook); err != nil {
		return err
	}
	hook.CreatedAt = existing.CreatedAt
	hook.UpdatedAt = s.now().UnixMilli()
	if err := s.store.Update(ctx, hook); err != nil {
		return err
	}
	// The row no longer points at the old managed token: retire it only after
	// the write succeeded, so a failed update cannot strand a live hook.
	s.retireManagedToken(ctx, existing, hook.SecretRef)
	return s.reloadNow(ctx)
}

// Delete removes the named hook and reloads. A missing hook is ErrNotFound.
func (s *Service) Delete(ctx context.Context, name string) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	existing, err := s.lookup(ctx, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err := s.store.Delete(ctx, where.F("name", name)); err != nil {
		return err
	}
	// Best-effort: the hook is gone either way, and a leftover secret is safer
	// than a failed delete the operator has to retry.
	s.retireManagedToken(ctx, existing, "")
	return s.reloadNow(ctx)
}

// reloadNow refreshes the in-memory view and reports a failure to do so. The
// write has already been persisted at this point, so the error says so
// explicitly: a caller that only saw "reload failed" might retry the write and
// hit a duplicate-name error, or might believe nothing was written when the row
// is in fact there. The periodic refresher remains as the backstop.
func (s *Service) reloadNow(ctx context.Context) error {
	if s.reload == nil {
		return nil
	}
	if err := s.reload(ctx); err != nil {
		return fmt.Errorf("webhook persisted but the in-memory hook set could not be reloaded: %w", err)
	}
	return nil
}
