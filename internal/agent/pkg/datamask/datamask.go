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
// The original repo for this file is https://github.com/onexstack/onex.

// Package datamask masks content on its way to storage: an audit record, a
// transcript, a log line.
//
// It deliberately depends on nothing but the standard library, so every
// boundary that persists text can share one implementation of the rules rather
// than each carrying a slightly different idea of what a secret looks like.
//
// The pipeline is two stages, in this order:
//
//  1. RedactSecrets replaces credentials wholesale. A secret is either present
//     or it is not; there is no partial form of it worth keeping.
//  2. MaskPII partially masks identifiers, keeping a fixed prefix and suffix so
//     a value stays recognizable to whoever is investigating without exposing
//     the whole of it.
//
// Order matters: masking first would rewrite the parts of a credential that a
// secret rule then fails to recognize.
//
// Names are never masked. An audit record exists to answer "who asked what",
// and a masked sender answers nothing.
package datamask

// Masking markers. They are stable strings rather than a single "***" so a
// reader can tell which rule fired, which is the difference between knowing a
// phone number was present and knowing something was.
const (
	// RedactedMarker is the format of a wholesale replacement. The rule id is
	// filled in, e.g. "[REDACTED:aws-access-key]".
	redactedFormat = "[REDACTED:%s]"
	// MaskMid is the middle of a partially masked value.
	MaskMid = "****"
	// MaskHead is the replacement for the leading part of a value whose tail is
	// what matters (a card number's last four digits).
	MaskHead = "******"
)

// Mask runs both stages over text destined for storage.
func Mask(text string) string {
	if text == "" {
		return text
	}
	return MaskPII(RedactSecrets(text))
}
