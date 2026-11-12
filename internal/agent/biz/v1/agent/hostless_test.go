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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// hostlessBiz wires the smallest business layer a composed hostless run needs.
// The engine records RunOptions, so each test asserts what admission resolved
// rather than what a model would have answered.
func hostlessBiz(engine agentapi.Engine, resources *fakeAIStore, coord *authz.Coordinator) *AgentBiz {
	return New(Deps{
		Engine:               engine,
		Sessions:             mem.NewSessionStore(nil, "test", 0),
		Stops:                runstop.NewLocal(),
		Resources:            resources,
		Authz:                coord,
		MaxConcurrentPerConv: 1,
	})
}

func hostlessRequest(composition *agentapi.AgentComposition) *agentapi.AskRequest {
	return &agentapi.AskRequest{
		SessionID:   "usr:compose:test",
		Input:       agentapi.Input{Text: "hi"},
		Selection:   agentapi.Selection{},
		Subject:     agentapi.Principal{ID: "u1", Name: "u1"},
		Actor:       &agentapi.Principal{ID: "u1", Name: "u1"},
		RunID:       "run-1",
		Source:      "test",
		Composition: composition,
	}
}

// TestAskHostlessCompositionResolvesWithoutHost is the core admission contract:
// zero hosts is legal for a composition, the composition is the whole
// instruction, and the run reaches the engine with an empty agent selection.
func TestAskHostlessCompositionResolvesWithoutHost(t *testing.T) {
	resources := newFakeResources()
	resources.prompts["persona"] = &model.PromptM{Name: "persona", Content: "You are an SRE."}
	engine := &recordingEngine{}
	b := hostlessBiz(engine, resources, nil)

	events, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		Prompts: []string{"persona"},
	}))
	require.NoError(t, err)
	drain(t, events)

	assert.Empty(t, engine.got.Agents, "a hostless run must not name an agent")
	assert.True(t, engine.got.InstructionSet, "the composition is authoritative even when empty")
	assert.Contains(t, engine.got.Instruction, "You are an SRE.")
	assert.Empty(t, engine.got.Model, "an unspecified model means the deployment default")
	assert.False(t, engine.got.MemoryEnabled, "no host means no long-term memory tool")
}

// TestAskHostlessKeepsApprovalFloor pins the safety floor: the deployment-wide
// patterns survive and each composed MCP adds its own.
func TestAskHostlessKeepsApprovalFloor(t *testing.T) {
	resources := newFakeResources()
	resources.mcps["incident"] = &model.MCPM{
		Name:              "incident",
		Status:            model.StatusEnabled,
		RequireApproval:   model.StringList{"pipeline"},
	}
	engine := &recordingEngine{}
	b := New(Deps{
		Engine:               engine,
		Sessions:             mem.NewSessionStore(nil, "test", 0),
		Stops:                runstop.NewLocal(),
		Resources:            resources,
		MaxConcurrentPerConv: 1,
		RequireApproval:      []string{"deploy"},
	})

	events, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		MCPs: []string{"incident"},
	}))
	require.NoError(t, err)
	drain(t, events)

	assert.Contains(t, engine.got.Approval, "deploy")
	assert.Contains(t, engine.got.Approval, "pipeline")
}

// TestAskHostlessSkipsHostGate proves the run does not acquire an agent gate:
// the authorizer denies every agent-kind request and allows resources, and the
// run still reaches the engine.
func TestAskHostlessSkipsHostGate(t *testing.T) {
	coord := authz.NewCoordinator(
		authz.AuthorizerFunc(func(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
			if req.Resource.Kind == authz.ResourceKindAgent {
				return authz.DecisionDeny, "agents are denied", nil
			}
			return authz.DecisionAllow, "resources are allowed", nil
		}),
		staticAuthorizer("resource", authz.DecisionAllow, "allowed"),
		true,
	)
	resources := newFakeResources()
	resources.mcps["incident"] = &model.MCPM{Name: "incident", Status: model.StatusEnabled}
	engine := &recordingEngine{}
	b := hostlessBiz(engine, resources, coord)

	events, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		MCPs: []string{"incident"},
	}))
	require.NoError(t, err)
	drain(t, events)
	assert.Empty(t, engine.got.Agents)
}

// TestAskHostlessStillAuthorizesResourceRefs keeps the playground from being an
// authorization bypass: a denied composed resource refuses the run before the
// engine is reached.
func TestAskHostlessStillAuthorizesResourceRefs(t *testing.T) {
	coord := authz.NewCoordinator(
		authz.AuthorizerFunc(func(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
			if req.Resource.Kind == authz.ResourceKindMCP {
				return authz.DecisionDeny, "mcp is denied", nil
			}
			return authz.DecisionAllow, "allowed", nil
		}),
		staticAuthorizer("resource", authz.DecisionAllow, "allowed"),
		true,
	)
	resources := newFakeResources()
	resources.mcps["incident"] = &model.MCPM{Name: "incident", Status: model.StatusEnabled}
	engine := &recordingEngine{}
	b := hostlessBiz(engine, resources, coord)

	_, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		MCPs: []string{"incident"},
	}))
	require.Error(t, err)
	var denied *authz.ErrAgentAccessDenied
	assert.True(t, errors.As(err, &denied), "want an authorization refusal, got %v", err)
	assert.Empty(t, engine.got.Agents, "the engine must not have run")
}

// TestHostlessRejectsRestrictedNamespaceResource closes the gap left by
// checkTargetPolicies' empty-selection early return.
func TestHostlessRejectsRestrictedNamespaceResource(t *testing.T) {
	resources := newFakeResources()
	resources.mcps["incident"] = &model.MCPM{
		Name:      "incident",
		Status:    model.StatusEnabled,
		Namespace: "team-a",
	}
	engine := &recordingEngine{}
	b := hostlessBiz(engine, resources, nil)

	// The referrer is helper-public, so the server in team-a is a
	// cross-namespace reference and needs a grant. A bare name would resolve
	// against the referrer and name a different object; the test names the
	// namespace explicitly to make the boundary the subject.
	_, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		Namespace: "helper-public",
		MCPs:      []string{"team-a/incident"},
	}))
	require.Error(t, err)
	var compErr *CompositionError
	assert.True(t, errors.As(err, &compErr), "want a CompositionError, got %v", err)
	assert.Empty(t, engine.got.Agents, "the engine must not have run")
}

// TestHostlessResolvesBareNamesAgainstTheReferrer is the regression test for
// the reported failure: two namespaces held an MCP called "onexai", the console
// sent the bare name, and the run resolved it by scanning the cluster instead of
// the namespace the person was working in.
func TestHostlessResolvesBareNamesAgainstTheReferrer(t *testing.T) {
	resources := newFakeResources()
	resources.mcps["onexai"] = &model.MCPM{
		Name:      "onexai",
		Namespace: "helper-public",
		Status:    model.StatusEnabled,
	}
	engine := &recordingEngine{}
	b := hostlessBiz(engine, resources, nil)

	events, err := b.Ask(context.Background(), hostlessRequest(&agentapi.AgentComposition{
		Namespace: "helper-public",
		MCPs:      []string{"onexai"},
	}))
	require.NoError(t, err)
	drain(t, events)

	require.Len(t, engine.got.MCPs, 1)
	assert.Equal(t, "helper-public/onexai", engine.got.MCPs[0],
		"a bare name must resolve to the referrer namespace, not to whichever row the store scanned first")
}

// TestHostlessStaysOffThePort pins the runtime limitation: without an agent
// name the capability token is nil, so the run must use the in-process engine.
func TestHostlessStaysOffThePort(t *testing.T) {
	b := New(Deps{
		Engine:      &recordingEngine{},
		Runtimes:    runtime.NewRegistry(nil),
		PortEnabled: true,
		Resources:   newFakeResources(),
		Sessions:    mem.NewSessionStore(nil, "test", 0),
		Stops:       runstop.NewLocal(),
	})
	usePort, err := b.shouldUsePort(resolvedSelection{set: agentSet{}})
	require.NoError(t, err)
	assert.False(t, usePort)
	assert.Equal(t, "recording", b.runtimeKindFor(resolvedSelection{set: agentSet{}}))
}

// TestComposedRunStillRejectsMultipleHosts keeps the host path's boundary: a
// composition names at most one host, never a fan-out.
func TestComposedRunStillRejectsMultipleHosts(t *testing.T) {
	engine := &recordingEngine{}
	b := hostlessBiz(engine, newFakeResources(), nil)
	req := hostlessRequest(&agentapi.AgentComposition{})
	req.Selection = agentapi.Selection{Agents: []string{"a", "b"}}

	_, err := b.Ask(context.Background(), req)
	require.Error(t, err)
	var compErr *CompositionError
	assert.True(t, errors.As(err, &compErr), "want a CompositionError, got %v", err)
}
