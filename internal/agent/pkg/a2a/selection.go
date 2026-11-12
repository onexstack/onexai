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

package a2a

import "strings"

// SelectedAgents reads the agent selection from A2A metadata.
//
// The request-level metadata is read first: it is the one a transport-wide
// client sets, while the message metadata belongs to the message the client
// both wrote and remembered. Both spellings are accepted because the singular
// is what a person writes and the plural is what an agent framework emits.
//
// It is exported because the authentication middleware has to know which
// Agents a request targets *before* the executor runs: an Agent may name
// authentication Webhooks that replace the platform chain.
func SelectedAgents(requestMetadata, messageMetadata map[string]any) []string {
	for _, metadata := range []map[string]any{requestMetadata, messageMetadata} {
		for _, key := range []string{"agents", "agent"} {
			if names := AgentNamesFromMetadata(metadata[key]); len(names) > 0 {
				return names
			}
		}
	}
	return nil
}

// AgentNamesFromMetadata reads one metadata value as one or more agent names.
func AgentNamesFromMetadata(value any) []string {
	switch typed := value.(type) {
	case string:
		if name := strings.TrimSpace(typed); name != "" {
			return []string{name}
		}
	case []string:
		return trimNames(typed)
	case []any:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return trimNames(names)
	}
	return nil
}

// trimNames drops blank entries, keeping the order the caller wrote.
func trimNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
