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

package prompttmpl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiteralWhenNoSources(t *testing.T) {
	rendered, err := Render("hello {{.AgentName}}", Data{AgentName: "a"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello {{.AgentName}}", rendered)
}

func TestRenderContextAndInclude(t *testing.T) {
	rendered, err := Render(
		"You are {{.AgentName}} in {{.AgentNamespace}}.\n{{include \"rules\"}}",
		Data{AgentName: "reviewer", AgentNamespace: "team"},
		map[string]string{"rules": "Be precise."},
	)
	require.NoError(t, err)
	assert.Contains(t, rendered, "You are reviewer in team.")
	assert.Contains(t, rendered, "Be precise.")
}

func TestUnknownIncludeFails(t *testing.T) {
	_, err := Render(`{{include "missing"}}`, Data{}, map[string]string{"present": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestUnknownVariableFails(t *testing.T) {
	_, err := Render(`{{.Nope}}`, Data{}, map[string]string{"x": "y"})
	require.Error(t, err)
}
