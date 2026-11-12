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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The discovered tool list is observed status: reads publish it in order, and a
// create/update request cannot forge it.
func TestMCPToolsConversion(t *testing.T) {
	t.Run("model to wire keeps name, description, and order", func(t *testing.T) {
		got := MCPModelToMCPV1(&model.MCPM{
			Name: "onex-courses",
			Tools: []model.MCPTool{
				{Name: "public_list_courses"},
				{Name: "public_get_course", Description: "按 productID 取课程介绍"},
			},
		})

		assert.Equal(t, []*v1.MCPProbedTool{
			{Name: "public_list_courses"},
			{Name: "public_get_course", Description: "按 productID 取课程介绍"},
		}, got.Tools)
	})

	t.Run("wire to model ignores status", func(t *testing.T) {
		got := MCPV1ToMCPModel(&v1.MCP{
			Name:  "onex-courses",
			Tools: []*v1.MCPProbedTool{{Name: "forged"}},
		})

		assert.Nil(t, got.Tools, "client-supplied tools must never reach the store")
	})
}

func TestMCPClientConfigRoundTrip(t *testing.T) {
	in := &v1.MCP{
		Name: "onex-courses",
		ClientConfig: &v1.ClientConfig{
			Url:            "https://mcp.example.com/mcp",
			TimeoutSeconds: 10,
			Headers:        map[string]string{"Authorization": "Bearer test"},
		},
	}

	row := MCPV1ToMCPModel(in)
	require.NotEmpty(t, row.ClientConfigJSON)
	assert.Equal(t, "https://mcp.example.com/mcp", row.URL)

	out := MCPModelToMCPV1(row)
	require.NotNil(t, out.ClientConfig)
	assert.Equal(t, "https://mcp.example.com/mcp", out.ClientConfig.Url)
	assert.Equal(t, int32(10), out.ClientConfig.TimeoutSeconds)
	assert.Equal(t, "Bearer test", out.ClientConfig.Headers["Authorization"])
}
