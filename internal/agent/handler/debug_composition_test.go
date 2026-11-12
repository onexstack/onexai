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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestAgentCompositionMapsEveryField(t *testing.T) {
	got := agentComposition(&v1.AgentComposition{
		Prompts:        []string{" persona "},
		Skills:         []string{"sre", "sre"},
		Mcps:           []string{"incident"},
		KnowledgeBases: []string{"kb"},
		CustomCommands: []string{"diag"},
		Model:          " fast ",
	})

	require.NotNil(t, got)
	assert.Equal(t, []string{"persona"}, got.Prompts)
	assert.Equal(t, []string{"sre"}, got.Skills)
	assert.Equal(t, []string{"incident"}, got.MCPs)
	assert.Equal(t, []string{"kb"}, got.KnowledgeBases)
	assert.Equal(t, []string{"diag"}, got.CustomCommands)
	assert.Equal(t, "fast", got.Model)
	assert.Nil(t, agentComposition(nil))
}

func TestComposeSessionIDIsScopedAndBounded(t *testing.T) {
	comp := &v1.AgentComposition{Skills: []string{"sre"}}
	base := composeSessionID("usr", "helper", "u1", "u1", comp, "n1")

	assert.True(t, strings.HasPrefix(base, "usr:compose:"))
	assert.LessOrEqual(t, len(base), 128)
	assert.Equal(t, base, composeSessionID("usr", "helper", "u1", "u1", comp, "n1"))
	assert.NotEqual(t, base, composeSessionID("usr", "helper", "u1", "u1", comp, "n2"))
	assert.True(t, strings.HasPrefix(composeSessionID("dbg", "helper", "u1", "admin", comp, "n1"), "dbg:compose:"))
}
