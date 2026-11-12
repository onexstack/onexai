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
	"errors"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The AIStore interface is far wider than this test needs. Embedding it (nil)
// and overriding only Models() keeps the fake honest for the one accessor the
// reveal path touches: any accidental use of another accessor panics instead of
// silently testing something else.
type stubAIStore struct {
	agentstore.AIStore
	models agentstore.ModelStore
}

func (s stubAIStore) Models() agentstore.ModelStore { return s.models }

type stubModelStore struct {
	agentstore.ModelStore
	row *agentmodel.ModelM
	err error
}

func (s stubModelStore) Get(context.Context, *where.Options) (*agentmodel.ModelM, error) {
	return s.row, s.err
}

// AES-256 needs exactly 32 bytes; the deployment's key is 32 as well.
const revealTestKey = "0123456789abcdef0123456789abcdef"

func newRevealBiz(row *agentmodel.ModelM, readErr error) *modelBiz {
	return New(
		stubAIStore{models: stubModelStore{row: row, err: readErr}},
		&Config{EncryptionKey: revealTestKey},
	)
}

func sealedKey(t *testing.T, plaintext string) string {
	t.Helper()
	sealed, err := agentmodel.SealAPIKey(plaintext, revealTestKey)
	require.NoError(t, err)
	return sealed
}

func TestRevealReturnsTheKeyToTheOwner(t *testing.T) {
	biz := newRevealBiz(&agentmodel.ModelM{
		Name:   "m",
		Owner:  "user-1",
		APIKey: sealedKey(t, "sk-live"),
	}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1"})

	require.NoError(t, err)
	assert.Equal(t, "sk-live", got)
}

func TestRevealAcceptsTheOwnerDisplayName(t *testing.T) {
	// Rows written before the ownership rule stored the display name.
	biz := newRevealBiz(&agentmodel.ModelM{
		Name:   "m",
		Owner:  "colin",
		APIKey: sealedKey(t, "sk-live"),
	}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1", Name: "colin"})

	require.NoError(t, err)
	assert.Equal(t, "sk-live", got)
}

func TestRevealLetsAnAdministratorReadAnyEndpoint(t *testing.T) {
	biz := newRevealBiz(&agentmodel.ModelM{
		Name:   "m",
		Owner:  "someone-else",
		APIKey: sealedKey(t, "sk-live"),
	}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "admin", IsAdmin: true})

	require.NoError(t, err)
	assert.Equal(t, "sk-live", got)
}

func TestRevealRefusesANonOwner(t *testing.T) {
	biz := newRevealBiz(&agentmodel.ModelM{
		Name:   "m",
		Owner:  "user-1",
		APIKey: sealedKey(t, "sk-live"),
	}, nil)

	_, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-2"})

	assert.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestRevealTreatsAPlatformEndpointAsAdministratorOnly(t *testing.T) {
	row := &agentmodel.ModelM{Name: "m", APIKey: sealedKey(t, "sk-live")}
	biz := newRevealBiz(row, nil)

	_, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1"})
	assert.ErrorIs(t, err, errno.ErrPermissionDenied)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "admin", IsAdmin: true})
	require.NoError(t, err)
	assert.Equal(t, "sk-live", got)
}

func TestRevealReportsAMissingModel(t *testing.T) {
	// The control-plane reader answers an absent row as (nil, nil).
	biz := newRevealBiz(nil, nil)

	_, err := biz.Reveal(context.Background(), "gone", Caller{UserID: "admin", IsAdmin: true})

	assert.ErrorIs(t, err, errno.ErrNotFound)
}

func TestRevealKeepsAReadFailureDistinctFromAbsence(t *testing.T) {
	readErr := errors.New("indexer is not synced")
	biz := newRevealBiz(nil, readErr)

	_, err := biz.Reveal(context.Background(), "m", Caller{UserID: "admin", IsAdmin: true})

	assert.ErrorIs(t, err, readErr)
	assert.NotErrorIs(t, err, errno.ErrNotFound)
}

func TestRevealAnswersEmptyWhenThereIsNoKey(t *testing.T) {
	biz := newRevealBiz(&agentmodel.ModelM{Name: "m", Owner: "user-1"}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1"})

	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestRevealAnswersEmptyWhenTheStoredKeyCannotBeOpened(t *testing.T) {
	// Sealed by a different deployment key: the credential still works for the
	// model, it just cannot be displayed. That is not a server failure.
	sealed, err := agentmodel.SealAPIKey("sk-live", "fedcba9876543210fedcba9876543210")
	require.NoError(t, err)
	biz := newRevealBiz(&agentmodel.ModelM{Name: "m", Owner: "user-1", APIKey: sealed}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1"})

	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestRevealReturnsALegacyPlaintextKeyUnchanged(t *testing.T) {
	biz := newRevealBiz(&agentmodel.ModelM{Name: "m", Owner: "user-1", APIKey: "sk-legacy"}, nil)

	got, err := biz.Reveal(context.Background(), "m", Caller{UserID: "user-1"})

	require.NoError(t, err)
	assert.Equal(t, "sk-legacy", got)
}

func TestGetStillOmitsTheKey(t *testing.T) {
	// Regression guard: adding the reveal endpoint must not leak the key back
	// into the ordinary read.
	biz := newRevealBiz(&agentmodel.ModelM{
		Name:   "m",
		Owner:  "user-1",
		APIKey: sealedKey(t, "sk-live"),
	}, nil)

	resp, err := biz.Get(context.Background(), &v1.GetModelRequest{Name: "m"})

	require.NoError(t, err)
	assert.Equal(t, "", resp.GetModel().GetApiKey())
	assert.True(t, resp.GetModel().GetHasAPIKey())
}
