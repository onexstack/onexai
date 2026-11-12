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

package blog

import "github.com/onexstack/onexai/internal/mcpserver/pkg/registry"

func init() {
	registry.Register(registry.Tool{
		Namespace:   Namespace,
		Name:        "post.search",
		Description: "Searches miniblog posts by a case-insensitive substring match against title and body.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Substring to search for in post titles and bodies.",
				},
			},
			"required": []string{"query"},
		},
		Handler: PostSearch,
	})

	registry.Register(registry.Tool{
		Namespace:   Namespace,
		Name:        "post.get",
		Description: "Retrieves a single miniblog post by its post ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"postID": map[string]any{
					"type":        "string",
					"description": "The post ID to retrieve, e.g. \"post-1\".",
				},
			},
			"required": []string{"postID"},
		},
		Handler: PostGet,
	})
}
