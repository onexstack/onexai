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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffectivePrefersTheResolvedSet(t *testing.T) {
	agent := AgentM{
		// The resolved set is only trusted once the controller has observed the
		// object; without this the declared set is the answer by design.
		Observed:                true,
		Skills:                  StringList{"declared-skill"},
		ResolvedSkills:          StringList{"declared-skill", "inherited-skill"},
		MCPs:                    StringList{"declared-mcp"},
		ResolvedMCPs:            StringList{"declared-mcp", "inherited-mcp"},
		KnowledgeBases:          StringList{"declared-kb"},
		ResolvedKnowledgeBases:  StringList{"declared-kb", "inherited-kb"},
		Webhooks:                StringList{"declared-hook"},
		ResolvedWebhooks:        StringList{"declared-hook", "inherited-hook"},
		RequireApproval:         StringList{"declared_*"},
		ResolvedRequireApproval: StringList{"declared_*", "inherited_*"},
		CustomCommands:          StringList{"declared-cmd"},
		ResolvedCustomCommands:  StringList{"declared-cmd", "inherited-cmd"},
	}

	assert.Equal(t, StringList{"declared-skill", "inherited-skill"}, agent.EffectiveSkills())
	assert.Equal(t, StringList{"declared-mcp", "inherited-mcp"}, agent.EffectiveMCPs())
	assert.Equal(t, StringList{"declared-kb", "inherited-kb"}, agent.EffectiveKnowledgeBases())
	assert.Equal(t, StringList{"declared-hook", "inherited-hook"}, agent.EffectiveWebhooks())
	assert.Equal(t, StringList{"declared_*", "inherited_*"}, agent.EffectiveRequireApproval())
	assert.Equal(t, StringList{"declared-cmd", "inherited-cmd"}, agent.EffectiveCustomCommands())
}

func TestEffectiveFallsBackWhenNoStatusWasObserved(t *testing.T) {
	agent := AgentM{
		Skills:          StringList{"declared-skill"},
		MCPs:            StringList{"declared-mcp"},
		KnowledgeBases:  StringList{"declared-kb"},
		Webhooks:        StringList{"declared-hook"},
		RequireApproval: StringList{"declared_*"},
		CustomCommands:  StringList{"declared-cmd"},
	}

	assert.Equal(t, StringList{"declared-skill"}, agent.EffectiveSkills())
	assert.Equal(t, StringList{"declared-mcp"}, agent.EffectiveMCPs())
	assert.Equal(t, StringList{"declared-kb"}, agent.EffectiveKnowledgeBases())
	assert.Equal(t, StringList{"declared-hook"}, agent.EffectiveWebhooks())
	assert.Equal(t, StringList{"declared_*"}, agent.EffectiveRequireApproval())
	assert.Equal(t, StringList{"declared-cmd"}, agent.EffectiveCustomCommands())
}
