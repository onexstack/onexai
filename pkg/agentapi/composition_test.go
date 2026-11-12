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

package agentapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompositionDigestIgnoresCapabilityOrder(t *testing.T) {
	a := &AgentComposition{
		Prompts:        []string{"persona"},
		Skills:         []string{"sre", "k8s"},
		MCPs:           []string{"incident"},
		KnowledgeBases: []string{"kb"},
		CustomCommands: []string{"diag"},
		Model:          "fast",
	}
	b := &AgentComposition{
		Prompts:        []string{"persona"},
		Skills:         []string{"k8s", "sre"},
		MCPs:           []string{"incident"},
		KnowledgeBases: []string{"kb"},
		CustomCommands: []string{"diag"},
		Model:          "fast",
	}
	assert.Equal(t, a.CanonicalDigest(), b.CanonicalDigest())
}

func TestCompositionDigestKeepsPromptOrder(t *testing.T) {
	a := &AgentComposition{Prompts: []string{"first", "second"}}
	b := &AgentComposition{Prompts: []string{"second", "first"}}
	assert.NotEqual(t, a.CanonicalDigest(), b.CanonicalDigest())
}

func TestCompositionNormalizedTrimsDedupesAndSorts(t *testing.T) {
	got := (&AgentComposition{
		Prompts: []string{" persona ", "persona", ""},
		Skills:  []string{"b", "a", "b", " "},
		Model:   " fast ",
	}).Normalized()

	require.NotNil(t, got)
	assert.Equal(t, []string{"persona"}, got.Prompts)
	assert.Equal(t, []string{"a", "b"}, got.Skills)
	assert.Equal(t, "fast", got.Model)
	assert.True(t, (&AgentComposition{}).Empty())
	assert.False(t, got.Empty())
}

func TestComposeSessionIDIsBoundedAndScoped(t *testing.T) {
	c := &AgentComposition{
		Prompts: []string{"persona"},
		Skills:  []string{"sre"},
	}
	base := ComposeSessionID("usr", "helper", "u1", "u1", c, "nonce-1")

	assert.True(t, strings.HasPrefix(base, "usr:compose:"))
	assert.LessOrEqual(t, len(base), 128, "session_id columns are size:128")
	assert.Equal(t, base, ComposeSessionID("usr", "helper", "u1", "u1", c, "nonce-1"))
	assert.NotEqual(t, base, ComposeSessionID("usr", "helper", "u1", "u1", c, "nonce-2"))
	assert.NotEqual(t, base, ComposeSessionID("usr", "other", "u1", "u1", c, "nonce-1"))
	assert.NotEqual(t, base, ComposeSessionID("usr", "helper", "u2", "u1", c, "nonce-1"))
	assert.NotEqual(t, base, ComposeSessionID("dbg", "helper", "u1", "u1", c, "nonce-1"))
}

// TestComposeSessionIDHostlessUsesReservedLabel pins the hostless bucket: an
// empty agent must still produce a stable, well-formed id and must differ from
// a real agent's bucket. The console's name pattern forbids a leading
// underscore, so the reserved label cannot be claimed by a real agent.
func TestComposeSessionIDHostlessUsesReservedLabel(t *testing.T) {
	c := &AgentComposition{Prompts: []string{"persona"}}
	hostless := ComposeSessionID("usr", "", "u1", "u1", c, "nonce-1")

	assert.True(t, strings.HasPrefix(hostless, "usr:compose:"))
	assert.Equal(t, hostless, ComposeSessionID("usr", "", "u1", "u1", c, "nonce-1"))
	assert.Equal(t, hostless, ComposeSessionID("usr", HostlessAgentLabel, "u1", "u1", c, "nonce-1"))
	assert.NotEqual(t, hostless, ComposeSessionID("usr", "", "u1", "u1", c, "nonce-2"))
	assert.NotEqual(t, hostless, ComposeSessionID("usr", "helper", "u1", "u1", c, "nonce-1"))
}
