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

package oncallgroup

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// stubGroupStore exercises Create without a database. The embedded interface is
// nil: Create only reaches GetByName and Create, and any other call is a bug
// the panic will make obvious.
type stubGroupStore struct {
	store.OncallGroupStore
	getByName   func(context.Context, string) (*model.OncallGroupM, error)
	createErr   error
	createCalls int
}

func (s *stubGroupStore) GetByName(ctx context.Context, name string) (*model.OncallGroupM, error) {
	return s.getByName(ctx, name)
}

func (s *stubGroupStore) Create(context.Context, *model.OncallGroupM) error {
	s.createCalls++
	return s.createErr
}

type stubStore struct {
	store.IStore
	groups store.OncallGroupStore
}

func (s *stubStore) OncallGroups() store.OncallGroupStore { return s.groups }

func TestCreateInsertsAFreshName(t *testing.T) {
	groups := &stubGroupStore{
		getByName: func(context.Context, string) (*model.OncallGroupM, error) {
			return nil, nil // GetError normalizes an absent row to not-found
		},
	}
	b := New(&stubStore{groups: groups})

	obj, err := b.Create(context.Background(), &api.OncallGroup{Name: "fresh"})

	require.NoError(t, err)
	assert.Equal(t, 1, groups.createCalls)
	assert.NotZero(t, obj.CreatedAt)
	assert.NotZero(t, obj.UpdatedAt)
}

// A duplicate must be answered from the pre-check, without an insert attempt:
// the reported failure used to be the unique-index error dressed as a 500.
func TestCreateReportsAnExistingNameWithoutInserting(t *testing.T) {
	groups := &stubGroupStore{
		getByName: func(context.Context, string) (*model.OncallGroupM, error) {
			return &model.OncallGroupM{Name: "k8s"}, nil
		},
	}
	b := New(&stubStore{groups: groups})

	_, err := b.Create(context.Background(), &api.OncallGroup{Name: "k8s"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAlreadyExists)
	assert.Equal(t, 0, groups.createCalls)
	message := errorsx.FromError(err).Message
	assert.Contains(t, message, `"k8s"`)
	assert.Contains(t, message, "未设置")
}

// The pre-check and the insert are not atomic: the unique index is the arbiter
// for a concurrent create, and the fallback still names the winner's namespace.
func TestCreateReportsAConcurrentDuplicateWithItsNamespace(t *testing.T) {
	lookups := 0
	groups := &stubGroupStore{
		getByName: func(context.Context, string) (*model.OncallGroupM, error) {
			lookups++
			if lookups == 1 {
				return nil, nil
			}
			return &model.OncallGroupM{Name: "k8s", Namespace: "helper-public"}, nil
		},
		createErr: errors.New(
			`ERROR: duplicate key value violates unique constraint "idx_chat_oncall_group_name" (SQLSTATE 23505)`,
		),
	}
	b := New(&stubStore{groups: groups})

	_, err := b.Create(context.Background(), &api.OncallGroup{Name: "k8s"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAlreadyExists)
	assert.Equal(t, 1, groups.createCalls)
	assert.Contains(t, errorsx.FromError(err).Message, "helper-public")
}

// An unreadable pre-check is an operational failure, not an invitation to
// insert and let the index report something else.
func TestCreateDoesNotInsertWhenTheLookupFails(t *testing.T) {
	groups := &stubGroupStore{
		getByName: func(context.Context, string) (*model.OncallGroupM, error) {
			return nil, errors.New("connection refused")
		},
	}
	b := New(&stubStore{groups: groups})

	_, err := b.Create(context.Background(), &api.OncallGroup{Name: "k8s"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrDBRead)
	assert.Equal(t, 0, groups.createCalls)
}

// A write failure that is neither a duplicate nor a read problem keeps the
// DBWrite mapping and its raw cause out of the response body.
func TestCreateKeepsOtherWriteFailuresAsDBWrite(t *testing.T) {
	cause := errors.New("connection reset")
	groups := &stubGroupStore{
		getByName: func(context.Context, string) (*model.OncallGroupM, error) {
			return nil, nil
		},
		createErr: cause,
	}
	b := New(&stubStore{groups: groups})

	_, err := b.Create(context.Background(), &api.OncallGroup{Name: "fresh"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrDBWrite)
	assert.ErrorIs(t, err, cause)
	assert.NotContains(t, errorsx.FromError(err).Message, cause.Error())
}
