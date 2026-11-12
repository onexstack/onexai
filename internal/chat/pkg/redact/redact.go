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

// Package redact provides helpers to strip sensitive values (tokens, secrets,
// keys) from strings before they are logged. It is intentionally conservative:
// it never mutates the caller's data and only ever shrinks a string.
package redact

import (
	"regexp"
)

// secretPattern matches URL query parameters and key-value pairs whose key names
// a secret, so the value can be masked without knowing it in advance.
var secretPattern = regexp.MustCompile(`(?i)(device_id|access_key|ticket|conn_id|secret|token|password|api[-_]?key|app[-_]?secret)=([^&\s"']+)`)

// Secrets masks the values of well-known secret keys in s, replacing them with
// "***". It is used to sanitize logs and error messages that may carry Lark
// credentials or signed URLs.
func Secrets(s string) string {
	return secretPattern.ReplaceAllString(s, `${1}=***`)
}

// Secret masks an entire secret value, returning "***" for a non-empty value
// and "" for an empty one. Use it when the whole string is a secret rather than
// a key=value fragment.
func Secret(v string) string {
	if v == "" {
		return ""
	}
	return "***"
}
