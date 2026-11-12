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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

func TestRuntimeProfileEmptyMeansDefault(t *testing.T) {
	set := agentSet{records: []*model.AgentM{{Name: "a"}}}
	profile, err := set.runtimeProfile()
	require.NoError(t, err)
	assert.Empty(t, profile)
}

func TestRuntimeProfileIsTheSameAcrossTheSelection(t *testing.T) {
	set := agentSet{records: []*model.AgentM{
		{Name: "a", RuntimeProfile: "kagent"},
		{Name: "b", RuntimeProfile: "kagent"},
	}}
	profile, err := set.runtimeProfile()
	require.NoError(t, err)
	assert.Equal(t, "kagent", profile)
}

func TestRuntimeProfileConflictIsRefused(t *testing.T) {
	set := agentSet{records: []*model.AgentM{
		{Name: "a", RuntimeProfile: "kagent"},
		{Name: "b", RuntimeProfile: "claude"},
	}}
	_, err := set.runtimeProfile()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different runtime profiles")
}

func TestWithRuntimeProfileDoesNotMutateTheStoreRecord(t *testing.T) {
	record := &model.AgentM{Name: "a", RuntimeProfile: "kagent"}
	set := agentSet{records: []*model.AgentM{record}}
	pinned := set.withRuntimeProfile("claude")
	profile, err := pinned.runtimeProfile()
	require.NoError(t, err)
	assert.Equal(t, "claude", profile)
	assert.Equal(t, "kagent", record.RuntimeProfile)
}

func TestOutputSchemaConflictIsRefused(t *testing.T) {
	set := agentSet{records: []*model.AgentM{
		{Name: "a", OutputSchema: `{"type":"object","properties":{"x":{"type":"string"}}}`},
		{Name: "b", OutputSchema: `{"type":"object","properties":{"y":{"type":"string"}}}`},
	}}
	_, _, err := set.outputSchema()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different output schemas")
}

func TestA2ASendRequestCarriesRunAndAgent(t *testing.T) {
	req := a2aSendRequest(&agentapi.AskRequest{
		RunID:     "r1",
		SessionID: "s1",
		Input:     agentapi.Input{Text: "hello"},
		Selection: agentapi.Selection{Agents: []string{"a"}},
	})
	require.NotNil(t, req.Message)
	assert.Equal(t, "r1", req.Message.ID)
	assert.Equal(t, "r1", req.Metadata[runtime.MetadataRunID])
	assert.Equal(t, "hello", runtime.UserText(req))
}
