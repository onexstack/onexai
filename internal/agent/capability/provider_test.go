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

package capability

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
)

// stateLookup answers with one run in a fixed state.
type stateLookup struct{ state string }

func (s stateLookup) GetRun(context.Context, string) (*model.RunM, error) {
	return &model.RunM{ID: "run-1", State: s.state}, nil
}

type missingLookup struct{}

func (missingLookup) GetRun(context.Context, string) (*model.RunM, error) { return nil, nil }

func newLivenessService(lookup RunLookup) *Service {
	registry := NewRegistry()
	registry.Register(fakeProvider{})
	return NewService(registry, NewIssuer(nil), lookup)
}

func callWithRun(t *testing.T, service *Service, runID string) error {
	t.Helper()
	_, err := service.Call(context.Background(), Claims{
		RunID: runID, Agent: "a1", Subject: "u1", Scopes: []string{"skill"},
	}, "skill_ping", json.RawMessage(`{}`))
	return err
}

// TestTokenOfAFinishedRunIsRefused pins the anti-replay rule: a one-hour token
// must not outlive the run it was minted for.
func TestTokenOfAFinishedRunIsRefused(t *testing.T) {
	service := newLivenessService(stateLookup{state: model.RunStateCompleted})
	require.ErrorIs(t, callWithRun(t, service, "run-1"), ErrRunNotActive)
}

func TestTokenOfARunningRunIsAccepted(t *testing.T) {
	service := newLivenessService(stateLookup{state: model.RunStateRunning})
	assert.NoError(t, callWithRun(t, service, "run-1"))
}

// TestTokenOfASuspendedRunIsAccepted pins that a paused run keeps its tools: the
// run resumes under the same token and would otherwise be unable to act.
func TestTokenOfASuspendedRunIsAccepted(t *testing.T) {
	service := newLivenessService(stateLookup{state: model.RunStateInterrupted})
	assert.NoError(t, callWithRun(t, service, "run-1"))
}

// TestMissingRunIsAllowed pins the deliberate asymmetry: a store that lost the
// row must not turn every tool call into a failure. The token is still signed
// and unexpired, and the alternative trades a narrow replay window for a broad
// outage.
func TestMissingRunIsAllowed(t *testing.T) {
	service := newLivenessService(missingLookup{})
	assert.NoError(t, callWithRun(t, service, "run-1"))
}

// TestNoRunStoreSkipsTheCheck pins that a deployment without a run store keeps
// working.
func TestNoRunStoreSkipsTheCheck(t *testing.T) {
	service := newLivenessService(nil)
	assert.NoError(t, callWithRun(t, service, "run-1"))
}
