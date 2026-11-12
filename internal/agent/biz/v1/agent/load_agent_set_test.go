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

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// agentSetStore answers Agents() from one recording store and everything else
// from the empty store.
type agentSetStore struct {
	store.AIStore
	agents store.AgentStore
}

func (s agentSetStore) Agents() store.AgentStore { return s.agents }

type recordingAgentStore struct {
	store.AgentStore
	got *where.Options
	row *model.AgentM
	err error
}

func (s *recordingAgentStore) Get(_ context.Context, opts *where.Options) (*model.AgentM, error) {
	s.got = opts
	return s.row, s.err
}

func TestLoadAgentSetUsesTheQualifiedReference(t *testing.T) {
	agents := &recordingAgentStore{
		AgentStore: store.NewEmptyStore().Agents(),
		row:        &model.AgentM{Name: "helper", Namespace: "team-a"},
	}
	resources := agentSetStore{AIStore: store.NewEmptyStore(), agents: agents}

	set, err := loadAgentSet(context.Background(), resources, []string{"team-a/helper"})
	require.NoError(t, err)
	require.Len(t, set.records, 1)
	assert.Equal(t, "team-a", set.records[0].Namespace)

	require.NotNil(t, agents.got)
	assert.Equal(t, "helper", agents.got.Filters["name"])
	assert.Equal(t, "team-a", agents.got.Filters["namespace"],
		"a qualified reference must narrow the store lookup to its namespace")
}

func TestLoadAgentSetSurfacesAmbiguity(t *testing.T) {
	agents := &recordingAgentStore{
		AgentStore: store.NewEmptyStore().Agents(),
		err:        &declstore.AmbiguousKeyError{Key: "helper", Namespaces: []string{"team-a", "team-b"}},
	}
	resources := agentSetStore{AIStore: store.NewEmptyStore(), agents: agents}

	_, err := loadAgentSet(context.Background(), resources, []string{"helper"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, declstore.ErrAmbiguousKey))
}

func TestLoadAgentSetSkipsAMissingAgent(t *testing.T) {
	agents := &recordingAgentStore{
		AgentStore: store.NewEmptyStore().Agents(),
		err:        store.ErrNotFound,
	}
	resources := agentSetStore{AIStore: store.NewEmptyStore(), agents: agents}

	set, err := loadAgentSet(context.Background(), resources, []string{"missing"})
	require.NoError(t, err, "a missing reference is reported by the controller, not fatal here")
	assert.Empty(t, set.records)
}
