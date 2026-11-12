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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestCapabilitySelectionPreservesPresence is the contract the run depends on:
// an absent message means "full effective set", a present message means
// "exactly these names", and the empty present message must not collapse into
// the absent one.
func TestCapabilitySelectionPreservesPresence(t *testing.T) {
	assert.Nil(t, capabilitySelection(nil))

	empty := capabilitySelection(&v1.CapabilitySelection{})
	require.NotNil(t, empty)
	assert.Empty(t, empty.Skills)
	assert.Empty(t, empty.MCPs)
	assert.Empty(t, empty.KnowledgeBases)
	assert.Empty(t, empty.CustomCommands)

	narrowed := capabilitySelection(&v1.CapabilitySelection{
		Skills:         []string{"skill-a"},
		Mcps:           []string{"mcp-a"},
		KnowledgeBases: []string{"kb-a"},
		CustomCommands: []string{"cmd-a"},
	})
	assert.Equal(t, []string{"skill-a"}, narrowed.Skills)
	assert.Equal(t, []string{"mcp-a"}, narrowed.MCPs)
	assert.Equal(t, []string{"kb-a"}, narrowed.KnowledgeBases)
	assert.Equal(t, []string{"cmd-a"}, narrowed.CustomCommands)
}
