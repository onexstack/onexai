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

// Package oncallgroup implements on-call group metadata business logic.
//
// The resource moved here from oai-agent-apiserver (where it was called a "module"
// and served from /v1/modules): the duty commands that read it are chat
// commands, so the table, the store and the CRUD API now live with them. The
// REST field names are unchanged so an existing admin client only has to switch
// host and credentials.
package oncallgroup

import (
	"context"
	"errors"
	"time"

	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// ProviderSet defines Wire DI providers for the on-call group biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(OncallGroupBiz), new(*oncallGroupBiz)))

// OncallGroupBiz defines the on-call group business logic interface.
type OncallGroupBiz interface {
	Create(ctx context.Context, obj *api.OncallGroup) (*api.OncallGroup, error)
	Update(ctx context.Context, obj *api.OncallGroup) (*api.OncallGroup, error)
	Delete(ctx context.Context, req *api.DeleteOncallGroupRequest) (any, error)
	Get(ctx context.Context, req *api.GetOncallGroupRequest) (*api.OncallGroup, error)
	List(ctx context.Context, _ *api.ListOncallGroupQuery) ([]*api.OncallGroup, error)
}

// oncallGroupBiz is the concrete implementation of OncallGroupBiz.
type oncallGroupBiz struct {
	st store.IStore
}

var _ OncallGroupBiz = (*oncallGroupBiz)(nil)

// New constructs an oncallGroupBiz from the persistence layer.
func New(st store.IStore) *oncallGroupBiz {
	return &oncallGroupBiz{st: st}
}

// Create inserts a new group, stamping the timestamps the old agent handler
// used to set.
func (b *oncallGroupBiz) Create(ctx context.Context, obj *api.OncallGroup) (*api.OncallGroup, error) {
	if obj.Name == "" {
		return nil, errno.ErrBind.WithMessage("group name is required")
	}

	// A group is addressed by name alone — the REST path, /oncall commands and
	// ticket bindings all carry no namespace — so name is unique across the
	// whole table. Read it first to answer a conflict with the namespace the
	// existing row lives in; the unique index stays the authority for the race
	// this check cannot close.
	existing, lookupErr := b.st.OncallGroups().GetByName(ctx, obj.Name)
	if err := storeerr.GetError(existing, lookupErr, "on-call group %q not found", obj.Name); err == nil {
		return nil, duplicateGroupError(existing.Name, existing.Namespace)
	} else if !errors.Is(err, errno.ErrNotFound) {
		return nil, err
	}

	now := time.Now().UnixMilli()
	obj.CreatedAt = now
	obj.UpdatedAt = now
	if err := b.st.OncallGroups().Create(ctx, obj); err != nil {
		// Another create may have won between the read above and this insert.
		// Re-read so the conflict names the same namespace the check would have.
		if store.IsDuplicateError(err) {
			if row, getErr := b.st.OncallGroups().GetByName(ctx, obj.Name); getErr == nil && row != nil {
				return nil, duplicateGroupError(row.Name, row.Namespace)
			}
			return nil, errno.ErrAlreadyExists.WithMessage("值班组 %q 已存在", obj.Name)
		}
		return nil, storeerr.Write(err)
	}
	return obj, nil
}

// duplicateGroupError reports a name conflict the way the console can act on:
// the name is unique, and which namespace holds the existing row is the one
// fact the operator needs to find it (the list is filtered by namespace, so a
// row in another/empty namespace is not on screen).
func duplicateGroupError(name, namespace string) error {
	if namespace == "" {
		return errno.ErrAlreadyExists.WithMessage("值班组 %q 已存在（命名空间：未设置）", name)
	}
	return errno.ErrAlreadyExists.WithMessage("值班组 %q 已存在（命名空间：%s）", name, namespace)
}

// Update saves an existing group identified by obj.Name.
func (b *oncallGroupBiz) Update(ctx context.Context, obj *api.OncallGroup) (*api.OncallGroup, error) {
	existing, err := b.st.OncallGroups().GetByName(ctx, obj.Name)
	if err := storeerr.GetError(existing, err, "on-call group %q not found", obj.Name); err != nil {
		return nil, err
	}
	// The SQL update is a full-row Save keyed by the internal ID. A caller that
	// sends only the business fields (the common case for a PUT) must not be
	// turned into an insert, and the creation time is not the caller's to
	// rewrite.
	obj.ID = existing.ID
	if obj.CreatedAt == 0 {
		obj.CreatedAt = existing.CreatedAt
	}
	obj.UpdatedAt = time.Now().UnixMilli()
	if err := b.st.OncallGroups().Update(ctx, obj); err != nil {
		return nil, storeerr.Write(err)
	}
	return obj, nil
}

// Delete removes the group with the given name and every row of its native
// schedule. The two live in the same database, so the delete is one
// transaction: a group without a schedule is the only state either table can
// agree on after the call.
func (b *oncallGroupBiz) Delete(ctx context.Context, req *api.DeleteOncallGroupRequest) (any, error) {
	if err := b.st.TX(ctx, func(ctx context.Context) error {
		if err := b.st.OncallSchedules().DeleteForGroup(ctx, req.Name); err != nil {
			return err
		}
		return b.st.OncallGroups().Delete(ctx, where.F("name", req.Name))
	}); err != nil {
		return nil, storeerr.Write(err)
	}
	return nil, nil
}

// Get fetches one group by name.
func (b *oncallGroupBiz) Get(ctx context.Context, req *api.GetOncallGroupRequest) (*api.OncallGroup, error) {
	obj, err := b.st.OncallGroups().GetByName(ctx, req.Name)
	if err := storeerr.GetError(obj, err, "on-call group %q not found", req.Name); err != nil {
		return nil, err
	}
	return obj, nil
}

// List returns every group.
func (b *oncallGroupBiz) List(ctx context.Context, _ *api.ListOncallGroupQuery) ([]*api.OncallGroup, error) {
	groups, err := b.st.OncallGroups().ListAll(ctx)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return groups, nil
}
