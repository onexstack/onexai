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

package mcpclient

import (
	"strconv"
	"strings"
)

// MaxToolNameLen is the longest function name accepted by the model APIs. Both
// OpenAI and Anthropic restrict function names to ^[a-zA-Z0-9_-]{1,64}$.
const MaxToolNameLen = 64

// SanitizeName rewrites an MCP tool's wire name into a name the model APIs
// accept. MCP servers are free to use characters those APIs reject — the
// zke-kubeagent server, for instance, names its tools "kubernetes_pod.logs" —
// so every name that reaches a model must pass through here.
//
// Errors and the call itself always use the original wire name; only the name
// the model sees is rewritten (see wireTool).
//
// A name that carries no usable character at all ("" , "...", pure non-ASCII)
// yields "", which callers must treat as "not exposed to the model".
func SanitizeName(wire string) string {
	name := strings.TrimSpace(wire)
	if name == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(name))
	hasAlnum := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
			hasAlnum = true
		case c == '_' || c == '-':
			b.WriteByte(c)
		default:
			// Every byte outside the allowed set becomes a single underscore,
			// including each byte of a multi-byte rune.
			b.WriteByte('_')
		}
	}
	if !hasAlnum {
		return ""
	}

	out := b.String()
	if len(out) > MaxToolNameLen {
		out = out[:MaxToolNameLen]
	}
	return out
}

// Uniquify returns base when it is still free, otherwise base_2, base_3, … so
// two servers exposing the same tool name can coexist in one model request.
// The chosen name is recorded in taken, which callers must pre-populate with
// the built-in tool names so an MCP tool can never shadow one.
//
// taken is mutated, so callers must assign names in a deterministic order or
// the suffixes will move between runs.
func Uniquify(base string, taken map[string]bool) string {
	if base == "" {
		return ""
	}
	if !taken[base] {
		taken[base] = true
		return base
	}
	for i := 2; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		head := base
		if len(head)+len(suffix) > MaxToolNameLen {
			head = head[:MaxToolNameLen-len(suffix)]
		}
		candidate := head + suffix
		if !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
}
