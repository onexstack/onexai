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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

func fullSpec() RunSpec {
	return RunSpec{
		Skills:         []string{"skill-a", "skill-b", "skill-c"},
		MCPs:           []string{"mcp-a", "mcp-b"},
		KnowledgeBases: []string{"kb-a"},
		CustomCommands: []string{"cmd-a", "cmd-b"},
		// Policy and authority are not capabilities and must survive a
		// narrowing untouched.
		Approval:     []string{"bash", "write"},
		Delegates:    []string{"helper"},
		SessionTools: true,
		Instruction:  "you are a helper",
	}
}

func TestApplyCapabilitySelectionKeepsRequestedNames(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	err := applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{
		// Deliberately out of spec order: the result follows the resolved set,
		// so two callers naming the same set produce the same revision.
		Skills:         []string{"skill-c", "skill-a"},
		MCPs:           []string{"mcp-b"},
		KnowledgeBases: []string{},
		CustomCommands: []string{"cmd-a"},
	}, true)

	require.NoError(t, err)
	assert.Equal(t, []string{"skill-a", "skill-c"}, sel.spec.Skills)
	assert.Equal(t, []string{"mcp-b"}, sel.spec.MCPs)
	assert.Empty(t, sel.spec.KnowledgeBases)
	assert.Equal(t, []string{"cmd-a"}, sel.spec.CustomCommands)
	assert.True(t, sel.narrowed)

	// Not capabilities: unchanged.
	assert.Equal(t, []string{"bash", "write"}, sel.spec.Approval)
	assert.Equal(t, []string{"helper"}, sel.spec.Delegates)
	assert.True(t, sel.spec.SessionTools)
	assert.Equal(t, "you are a helper", sel.spec.Instruction)
}

func TestApplyCapabilitySelectionAbsentKeepsFullSet(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	require.NoError(t, applyCapabilitySelection(&sel, nil, true))

	assert.Equal(t, fullSpec(), sel.spec)
	assert.False(t, sel.narrowed)
}

func TestApplyCapabilitySelectionEmptyMeansNone(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	require.NoError(t, applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{}, true))

	assert.Empty(t, sel.spec.Skills)
	assert.Empty(t, sel.spec.MCPs)
	assert.Empty(t, sel.spec.KnowledgeBases)
	assert.Empty(t, sel.spec.CustomCommands)
	assert.True(t, sel.narrowed)
}

func TestApplyCapabilitySelectionRefusesOutsideName(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	err := applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{
		Skills: []string{"skill-a", "not-a-skill"},
		MCPs:   []string{"not-an-mcp"},
	}, true)

	var capErr *CapabilitySelectionError
	require.True(t, errors.As(err, &capErr))
	assert.Contains(t, capErr.Unknown, `skill "not-a-skill"`)
	assert.Contains(t, capErr.Unknown, `mcp "not-an-mcp"`)
	assert.Contains(t, capErr.Error(), `skill "not-a-skill"`)

	// A refused selection changes nothing.
	assert.Equal(t, fullSpec(), sel.spec)
	assert.False(t, sel.narrowed)
}

func TestApplyCapabilitySelectionReportsDuplicateUnknownOnce(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	err := applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{
		Skills: []string{"nope", "nope"},
	}, true)

	var capErr *CapabilitySelectionError
	require.True(t, errors.As(err, &capErr))
	assert.Equal(t, []string{`skill "nope"`}, capErr.Unknown)
}

func TestApplyCapabilitySelectionRecordedIntersectsWithoutRefusing(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	// A capability revoked while the run was paused is dropped, not an error:
	// refusing would leave the human a card they cannot answer.
	err := applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{
		Skills: []string{"skill-b", "revoked-skill"},
		MCPs:   []string{"revoked-mcp"},
	}, false)

	require.NoError(t, err)
	assert.Equal(t, []string{"skill-b"}, sel.spec.Skills)
	assert.Empty(t, sel.spec.MCPs)
	assert.True(t, sel.narrowed)
}

func TestApplyCapabilitySelectionRecordedEmptyStaysEmpty(t *testing.T) {
	sel := resolvedSelection{spec: fullSpec()}

	require.NoError(t, applyCapabilitySelection(&sel, &agentapi.CapabilitySelection{}, false))

	assert.Empty(t, sel.spec.Skills)
	assert.Empty(t, sel.spec.MCPs)
	assert.True(t, sel.narrowed)
}
