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

// Package prompttmpl renders an opt-in Go text/template prompt with the
// agent's own context. Rendering is opt-in: a prompt with no data sources is
// returned verbatim, so existing prompts that happen to contain "{{" are not
// reinterpreted.
package prompttmpl

import (
	"bytes"
	"fmt"
	"slices"
	"text/template"
)

// Data is the context a prompt template can address.
type Data struct {
	AgentName      string
	AgentNamespace string
	Description    string
	ToolNames      []string
}

// Render executes raw when sources is non-empty. `include "key"` inserts the
// source value verbatim; included text is not rendered again.
func Render(raw string, data Data, sources map[string]string) (string, error) {
	if len(sources) == 0 {
		return raw, nil
	}
	parsed, err := template.New("prompt").Option("missingkey=error").Funcs(template.FuncMap{
		"include": func(key string) (string, error) {
			value, ok := sources[key]
			if !ok {
				available := make([]string, 0, len(sources))
				for name := range sources {
					available = append(available, name)
				}
				slices.Sort(available)
				return "", fmt.Errorf("prompt include %q not found, available: %v", key, available)
			}
			return value, nil
		},
	}).Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}
	return out.String(), nil
}
