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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// TestResolver_ResolveCarriesTheAgentNames pins that the spec keeps the
// selection it was resolved for.
//
// It is not decorative: the runner cache rebuilds a graph from the spec alone,
// and the names are what name the runner and isolate its skills on disk. A spec
// without them makes every cached agent set share one default key — and
// therefore one skills directory, which is the cross-agent clobbering that the
// isolation exists to prevent.
func TestResolver_ResolveCarriesTheAgentNames(t *testing.T) {
	r := Resolver{Resources: store.NewEmptyStore()}
	agents := []string{"bke-agent", "sre-agent"}

	spec := r.Resolve(context.Background(), agents)
	if !reflect.DeepEqual(spec.Agents, agents) {
		t.Fatalf("Resolve().Agents = %v, want %v", spec.Agents, agents)
	}
}

// An aggregated agent's capability set comes from status.resolved, not the
// spec: the controller has already expanded selectors and inherited sources.
func TestResolver_PrefersTheResolvedCapabilitySet(t *testing.T) {
	resources := refStore(&model.AgentM{
		Name: "aggregate",
		// The resolved sets are trusted only once the controller has observed
		// the object; without this the declared set is the answer by design.
		Observed:               true,
		Skills:                 model.StringList{"own-skill"},
		ResolvedSkills:         model.StringList{"own-skill", "inherited-skill"},
		MCPs:                   model.StringList{"own-mcp"},
		ResolvedMCPs:           model.StringList{"own-mcp", "inherited-mcp"},
		KnowledgeBases:         model.StringList{"own-kb"},
		ResolvedKnowledgeBases: model.StringList{"own-kb", "inherited-kb"},
		Webhooks:               model.StringList{"own-hook"},
		ResolvedWebhooks:       model.StringList{"own-hook", "inherited-hook"},
	})

	spec := Resolver{Resources: resources}.Resolve(context.Background(), []string{"aggregate"})

	assert.Equal(t, []string{"own-skill", "inherited-skill"}, spec.Skills)
	assert.Equal(t, []string{"own-mcp", "inherited-mcp"}, spec.MCPs)
	assert.Equal(t, []string{"own-kb", "inherited-kb"}, spec.KnowledgeBases)
	assert.Equal(t, []string{"own-hook", "inherited-hook"}, spec.Webhooks)
}

func TestResolver_ResolveIsUsableWithNoStore(t *testing.T) {
	// A deployment with no resource backend must still resolve: every name
	// resolves to nothing, which yields an empty spec rather than a panic.
	r := Resolver{}
	spec := r.Resolve(context.Background(), []string{"some-agent"})
	if len(spec.MCPs) != 0 || len(spec.Skills) != 0 || len(spec.KnowledgeBases) != 0 {
		t.Errorf("with no store every resource list must be empty, got %+v", spec)
	}
	if !reflect.DeepEqual(spec.Agents, []string{"some-agent"}) {
		t.Errorf("the selection must survive even with no store, got %v", spec.Agents)
	}
}
