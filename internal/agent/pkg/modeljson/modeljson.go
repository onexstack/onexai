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

// Package modeljson holds the small tolerance helpers shared by every place
// that asks a model for JSON.
//
// Asking for "JSON only" gets JSON most of the time; the rest of the time it
// arrives inside a fence. Every caller that parses a model's reply needs the
// same handful of lines, and three copies of them is three places for the
// tolerance to differ.
package modeljson

import "strings"

// StripFence unwraps a ```json (or bare ```) block and trims surrounding space.
// Text without a fence is returned trimmed and otherwise unchanged.
func StripFence(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}
