// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package system

import "github.com/onexstack/onexai/internal/mcpserver/pkg/registry"

func init() {
	registry.Register(registry.Tool{
		Namespace:   Namespace,
		Name:        "time.now",
		Description: "Returns the current server time in RFC3339 and Unix formats.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler:     TimeNow,
	})

	registry.Register(registry.Tool{
		Namespace:   Namespace,
		Name:        "env.get",
		Description: "Returns the value of a named environment variable on the oai-mcpserver host.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Environment variable name to look up.",
				},
			},
			"required": []string{"name"},
		},
		Handler: EnvGet,
	})
}
