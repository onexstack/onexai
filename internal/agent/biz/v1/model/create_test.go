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

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// captureModelStore records the rows the biz hands to the store, so a test can
// assert on what would actually be persisted rather than on the response alone.
type captureModelStore struct {
	agentstore.ModelStore
	existing *agentmodel.ModelM
	saved    *agentmodel.ModelM
	updated  *agentmodel.ModelM
	err      error
}

func (s *captureModelStore) Get(context.Context, *where.Options) (*agentmodel.ModelM, error) {
	return s.existing, s.err
}

func (s *captureModelStore) Create(_ context.Context, obj *agentmodel.ModelM) error {
	s.saved = obj
	return s.err
}

func (s *captureModelStore) Update(_ context.Context, obj *agentmodel.ModelM) error {
	s.updated = obj
	return s.err
}

func newCreateBiz(store *captureModelStore) *modelBiz {
	return New(stubAIStore{models: store}, &Config{EncryptionKey: revealTestKey})
}

func TestCreateReturnsPlainKeyAndStoresSealedKey(t *testing.T) {
	store := &captureModelStore{}
	biz := newCreateBiz(store)

	resp, err := biz.Create(context.Background(), &v1.CreateModelRequest{Model: &v1.Model{
		Name:     "m",
		Owner:    "user-1",
		Provider: "openai",
		BaseUrl:  "https://api.openai.com/v1",
		Model:    "gpt-4o-mini",
		ApiKey:   "sk-plain",
	}})

	require.NoError(t, err)
	// The caller sees the key it submitted, once.
	assert.Equal(t, "sk-plain", resp.GetModel().GetApiKey())
	assert.True(t, resp.GetModel().GetHasAPIKey())

	// Storage only ever holds the sealed form, and it round-trips.
	require.NotNil(t, store.saved)
	assert.True(t, strings.HasPrefix(store.saved.APIKey, "enc:v1:"))
	assert.NotContains(t, store.saved.APIKey, "sk-plain")
	opened, err := agentmodel.OpenAPIKey(store.saved.APIKey, revealTestKey)
	require.NoError(t, err)
	assert.Equal(t, "sk-plain", opened)
}

func TestCreateRejectsClientSuppliedCiphertext(t *testing.T) {
	biz := newCreateBiz(&captureModelStore{})

	_, err := biz.Create(context.Background(), &v1.CreateModelRequest{Model: &v1.Model{
		Name:     "m",
		Owner:    "user-1",
		Provider: "openai",
		BaseUrl:  "https://api.openai.com/v1",
		Model:    "gpt-4o-mini",
		ApiKey:   "enc:v1:AAAA",
	}})

	assert.ErrorIs(t, err, errno.ErrInvalidArgument)
}

func TestUpdateDoesNotEchoTheStoredKey(t *testing.T) {
	store := &captureModelStore{existing: &agentmodel.ModelM{
		Name:   "m",
		Owner:  "user-1",
		APIKey: sealedKey(t, "sk-live"),
	}}
	biz := newCreateBiz(store)

	resp, err := biz.Update(context.Background(), &v1.UpdateModelRequest{Model: &v1.Model{
		Name:    "m",
		BaseUrl: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
		// No apiKey: the stored sealed value is kept, not re-encrypted.
	}})

	require.NoError(t, err)
	assert.Equal(t, "", resp.GetModel().GetApiKey())
	assert.True(t, resp.GetModel().GetHasAPIKey())
	require.NotNil(t, store.updated)
	assert.True(t, strings.HasPrefix(store.updated.APIKey, "enc:v1:"))
	opened, err := agentmodel.OpenAPIKey(store.updated.APIKey, revealTestKey)
	require.NoError(t, err)
	assert.Equal(t, "sk-live", opened)
}
