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

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// fakeAgentStore answers the two reads the effective view and the preview make.
type fakeAgentStore struct {
	agentstore.AgentStore
	row  *model.AgentM
	list []*model.AgentM
}

func (f fakeAgentStore) Get(context.Context, *where.Options) (*model.AgentM, error) {
	return f.row, nil
}

func (f fakeAgentStore) List(context.Context, *where.Options) (int64, []*model.AgentM, error) {
	return int64(len(f.list)), f.list, nil
}

// missingPromptStore reports every reference as absent, which is the preview's
// "missing" path.
type missingPromptStore struct{ agentstore.PromptStore }

func (missingPromptStore) Get(context.Context, *where.Options) (*model.PromptM, error) {
	return nil, agentstore.ErrNotFound
}

type missingSkillStore struct{ agentstore.SkillStore }

func (missingSkillStore) Get(context.Context, *where.Options) (*model.SkillM, error) {
	return nil, agentstore.ErrNotFound
}

type missingKnowledgeBaseStore struct{ agentstore.KnowledgeBaseStore }

func (missingKnowledgeBaseStore) Get(context.Context, *where.Options) (*model.KnowledgeBaseM, error) {
	return nil, agentstore.ErrNotFound
}

type missingMCPStore struct{ agentstore.MCPStore }

func (missingMCPStore) Get(context.Context, *where.Options) (*model.MCPM, error) {
	return nil, agentstore.ErrNotFound
}

type missingCustomCommandStore struct{ agentstore.CustomCommandStore }

func (missingCustomCommandStore) Get(context.Context, *where.Options) (*model.CustomCommandM, error) {
	return nil, agentstore.ErrNotFound
}

// agentViewResources serves the agent reads the two endpoints need.
type agentViewResources struct {
	bindingResources
	agents *fakeAgentStore
}

func (r agentViewResources) Agents() agentstore.AgentStore { return r.agents }

func (r agentViewResources) Prompts() agentstore.PromptStore { return missingPromptStore{} }

func (r agentViewResources) Skills() agentstore.SkillStore { return missingSkillStore{} }

func (r agentViewResources) KnowledgeBases() agentstore.KnowledgeBaseStore {
	return missingKnowledgeBaseStore{}
}

func (r agentViewResources) MCPs() agentstore.MCPStore { return missingMCPStore{} }

func (r agentViewResources) CustomCommands() agentstore.CustomCommandStore {
	return missingCustomCommandStore{}
}

func newAgentViewTestEngine(t *testing.T, agents *fakeAgentStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resources := agentViewResources{
		bindingResources: bindingResources{bindings: newMemAgentBindings()},
		agents:           agents,
	}
	h := NewHandler(Deps{
		Biz:       newTestBiz(resources),
		Resources: resources,
	})
	engine := gin.New()
	h.ApplyUnifiedTo(engine.Group("/v1"))
	return engine
}

// TestAgentBindingPreviewResolvesAgainstStores checks the dry run still asks the
// stores the questions the controllers ask: who matches, and what each
// reference resolves to.
func TestAgentBindingPreviewResolvesAgainstStores(t *testing.T) {
	engine := newAgentViewTestEngine(t, &fakeAgentStore{
		list: []*model.AgentM{
			{Name: "alpha", Namespace: "default"},
			{Name: "off", Namespace: "default", Status: model.StatusDisabled},
		},
	})

	body := `{"namespace":"default","spec":{"agents":["alpha"],"prompts":[{"name":"p"}]}}`
	rec := do(t, engine, http.MethodPost, "/v1/agent/agentbindings/preview", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got v1.PreviewAgentBindingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []string{"alpha"}, got.MatchedAgents)
	assert.Equal(t, []string{"Prompt/default/p"}, got.Missing,
		"a missing reference is reported with the namespace it was looked up in")
	assert.Empty(t, got.Resolved.Prompts)
}

// TestGetEffectiveAgentReturnsDeclaredCapabilities checks the effective view
// falls back to the declared set before the controller has observed the agent.
func TestGetEffectiveAgentReturnsDeclaredCapabilities(t *testing.T) {
	engine := newAgentViewTestEngine(t, &fakeAgentStore{
		row: &model.AgentM{
			Name:           "alpha",
			Skills:         model.StringList{"s1"},
			MCPs:           model.StringList{"m1"},
			KnowledgeBases: model.StringList{"k1"},
		},
	})

	rec := do(t, engine, http.MethodGet, "/v1/agent/agents/alpha/effective", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got v1.GetEffectiveAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "alpha", got.Agent)
	assert.False(t, got.Observed)
	assert.Equal(t, []string{"s1"}, got.Capabilities.Skills)
	assert.Equal(t, []string{"m1"}, got.Capabilities.MCPs)
	assert.Equal(t, []string{"k1"}, got.Capabilities.KnowledgeBases)
}
