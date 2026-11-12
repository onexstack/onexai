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
	"fmt"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// Store is the persistence surface the Service writes through. It is the
// standard authz-rule table store and nothing else: the rule table is read by
// the OPA rule store alone, so this is intentionally not the cached resource
// surface (see the package doc).
type Store interface {
	Create(ctx context.Context, obj *model.AuthzRuleM) error
	Update(ctx context.Context, obj *model.AuthzRuleM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AuthzRuleM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AuthzRuleM, error)
}

// Reloader rebuilds the in-memory OPA rule set from the store. The Service
// calls it after every successful write so the change is visible to the next
// request rather than at the next periodic refresh. A nil Reloader (no OPA
// authorization configured) makes the call a no-op.
type Reloader func(ctx context.Context) error

// ErrNotFound reports that the named rule does not exist.
var ErrNotFound = errors.New("authz rule not found")

// ErrNotConfigured reports that no rule store was wired (the deployment is not
// SQL-backed), so the API cannot serve writes.
var ErrNotConfigured = errors.New("authz rule store is not configured")

// Service is the authorization-rule administration object assembled at startup
// from the SQL loader and the shared OPA reloader, and injected into the REST
// handler.
type Service struct {
	store  Store
	reload Reloader
	// now is injectable so tests can assert stamps without sleeping.
	now func() time.Time
}

// New builds a Service over store. reload may be nil when OPA is not enabled;
// writes then still persist, they just have no in-memory policy to refresh.
func New(store Store, reload Reloader) *Service {
	return &Service{store: store, reload: reload, now: time.Now}
}

// Create validates and inserts a new rule, then reloads the in-memory set. The
// server stamps CreatedAt/UpdatedAt; a client-supplied timestamp is ignored.
func (s *Service) Create(ctx context.Context, rule *model.AuthzRuleM) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	if err := Validate(rule); err != nil {
		return err
	}
	if _, err := s.store.Get(ctx, where.F("name", rule.Name)); err == nil {
		return &ValidationError{Field: "name", Message: fmt.Sprintf("rule %q already exists", rule.Name)}
	} else if !errors.Is(err, agentstore.ErrNotFound) {
		return err
	}
	now := s.now().UnixMilli()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	if err := s.store.Create(ctx, rule); err != nil {
		return err
	}
	return s.reloadNow(ctx)
}

// Get returns the named rule and whether it exists.
func (s *Service) Get(ctx context.Context, name string) (*model.AuthzRuleM, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, ErrNotConfigured
	}
	row, err := s.store.Get(ctx, where.F("name", name))
	if errors.Is(err, agentstore.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row, true, nil
}

// List returns every rule, enabled or not: the API is an administration
// surface, so a disabled rule is still something an operator must be able to
// see and re-enable.
func (s *Service) List(ctx context.Context) ([]*model.AuthzRuleM, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	_, rows, err := s.store.List(ctx, where.L(-1))
	return rows, err
}

// Update validates and overwrites the named rule, preserving its CreatedAt,
// then reloads the in-memory set. A missing rule is ErrNotFound.
func (s *Service) Update(ctx context.Context, rule *model.AuthzRuleM) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	if err := Validate(rule); err != nil {
		return err
	}
	existing, err := s.store.Get(ctx, where.F("name", rule.Name))
	if errors.Is(err, agentstore.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, rule.Name)
	}
	if err != nil {
		return err
	}
	rule.CreatedAt = existing.CreatedAt
	rule.UpdatedAt = s.now().UnixMilli()
	if err := s.store.Update(ctx, rule); err != nil {
		return err
	}
	return s.reloadNow(ctx)
}

// Delete removes the named rule and reloads the in-memory set. A missing rule
// is ErrNotFound.
func (s *Service) Delete(ctx context.Context, name string) error {
	if s == nil || s.store == nil {
		return ErrNotConfigured
	}
	if _, err := s.store.Get(ctx, where.F("name", name)); errors.Is(err, agentstore.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	} else if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, where.F("name", name)); err != nil {
		return err
	}
	return s.reloadNow(ctx)
}

// reloadNow refreshes the rule set and reports a failure to do so. The write has
// already been persisted at this point, so the error says so explicitly: a
// caller that only saw "reload failed" might retry the write and hit a
// duplicate-name error, or might believe nothing was written when the row is in
// fact there. The periodic refresher remains as the backstop.
func (s *Service) reloadNow(ctx context.Context) error {
	if s.reload == nil {
		return nil
	}
	if err := s.reload(ctx); err != nil {
		return fmt.Errorf("rule persisted but the in-memory rule set could not be reloaded: %w", err)
	}
	return nil
}
