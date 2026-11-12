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

package echo

import "github.com/onexstack/onexai/internal/mcpserver/pkg/registry"

func init() {
	registry.Register(registry.Tool{
		Namespace:   Namespace,
		Name:        "ping",
		Description: "Echoes back the given message, prefixed with 'pong: '. Useful for smoke-testing the server.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Message to echo back. Defaults to \"pong\" if omitted.",
				},
			},
		},
		Handler: Ping,
	})
}
