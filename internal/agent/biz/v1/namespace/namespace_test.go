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

package namespace

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

type stubNamespaceStore struct {
	agentstore.NamespaceStore
	createErr error
}

func (s *stubNamespaceStore) Create(context.Context, *model.NamespaceM) error {
	return s.createErr
}

type stubAIStore struct {
	agentstore.AIStore
	namespaces agentstore.NamespaceStore
}

func (s *stubAIStore) Namespaces() agentstore.NamespaceStore { return s.namespaces }

func newCreateBiz(createErr error) NamespaceBiz {
	return New(&stubAIStore{
		AIStore:    agentstore.NewEmptyStore(),
		namespaces: &stubNamespaceStore{createErr: createErr},
	})
}

func createRequest(name string) *v1.CreateNamespaceRequest {
	return &v1.CreateNamespaceRequest{
		Namespace: &v1.Namespace{Name: name, DisplayName: name},
	}
}

// A name that already exists in the control plane must read as a conflict, not
// as a database failure.
func TestCreateMapsAlreadyExistsToConflict(t *testing.T) {
	createErr := apierrors.NewAlreadyExists(
		schema.GroupResource{Resource: "namespaces"}, "team-a",
	)
	biz := newCreateBiz(createErr)

	_, err := biz.Create(context.Background(), createRequest("team-a"))

	require.Error(t, err)
	var apiErr *errorsx.ErrorX
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code)
	assert.Equal(t, "AlreadyExists", apiErr.Reason)
	assert.Contains(t, apiErr.Message, "team-a")
}

// Row-store failures keep their existing database-error mapping.
func TestCreateKeepsDatabaseFailureAsDBWrite(t *testing.T) {
	biz := newCreateBiz(errors.New("dial tcp: connection refused"))

	_, err := biz.Create(context.Background(), createRequest("team-a"))

	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrDBWrite), "want ErrDBWrite, got %v", err)
}
