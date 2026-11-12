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

package revision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseInput() Input {
	return Input{
		Namespace: "team", Agents: []string{"a"}, Model: "m",
		Instruction: "be kind", MCPs: []string{"mcp-b", "mcp-a"},
		Skills: []string{"skill-b", "skill-a"},
	}
}

func TestCompileIsStable(t *testing.T) {
	first, err := Compile(baseInput())
	require.NoError(t, err)
	second, err := Compile(baseInput())
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.Digest, second.Digest)
}

func TestCompileIgnoresListOrder(t *testing.T) {
	first, err := Compile(baseInput())
	require.NoError(t, err)
	reordered := baseInput()
	reordered.MCPs = []string{"mcp-a", "mcp-b"}
	reordered.Skills = []string{"skill-a", "skill-b"}
	second, err := Compile(reordered)
	require.NoError(t, err)
	assert.Equal(t, first.Digest, second.Digest)
}

func TestCompileChangesWithBehaviour(t *testing.T) {
	first, err := Compile(baseInput())
	require.NoError(t, err)
	changed := baseInput()
	changed.Instruction = "be brief"
	second, err := Compile(changed)
	require.NoError(t, err)
	assert.NotEqual(t, first.Digest, second.Digest)
	assert.NotEqual(t, first.ID, second.ID)

	changed = baseInput()
	changed.RuntimeKind = "a2a"
	third, err := Compile(changed)
	require.NoError(t, err)
	assert.NotEqual(t, first.Digest, third.Digest)
}
