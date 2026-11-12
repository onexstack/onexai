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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestFillTestModelFromStoredFillsOmittedFields(t *testing.T) {
	row := &agentmodel.ModelM{
		Name:     "gpt",
		Provider: "openai",
		BaseURL:  "https://api.example.test/v1",
		Model:    "gpt-4o",
	}
	b := newRevealBiz(row, nil)
	inline := &v1.Model{}

	require.NoError(t, b.fillTestModelFromStored(context.Background(), "gpt", inline))
	assert.Equal(t, "openai", inline.GetProvider())
	assert.Equal(t, "https://api.example.test/v1", inline.GetBaseUrl())
	assert.Equal(t, "gpt-4o", inline.GetModel())
}

func TestFillTestModelFromStoredReportsAMissingRow(t *testing.T) {
	b := newRevealBiz(nil, nil)

	err := b.fillTestModelFromStored(context.Background(), "missing", &v1.Model{})

	// The request named a row that does not exist: the test never ran, so the
	// caller gets 404 rather than a probe result.
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrNotFound)
}

func TestFillTestModelFromStoredKeepsAReadFailureDistinct(t *testing.T) {
	readErr := errors.New("indexer is not synced")
	b := newRevealBiz(nil, readErr)

	err := b.fillTestModelFromStored(context.Background(), "gpt", &v1.Model{})

	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, errno.ErrDBRead)
}

func TestTestKeyReportsAMissingRow(t *testing.T) {
	b := newRevealBiz(nil, nil)

	_, err := b.testKey(context.Background(), "missing", "")

	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrNotFound)
}

func TestTestKeyReportsAReadFailure(t *testing.T) {
	readErr := errors.New("connection reset")
	b := newRevealBiz(nil, readErr)

	_, err := b.testKey(context.Background(), "gpt", "")

	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, errno.ErrDBRead)
}
