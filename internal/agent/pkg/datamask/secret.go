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

package datamask

import (
	"fmt"
	"regexp"
	"sync"
)

// secretRule is one credential pattern. The id appears in the replacement, so
// an operator reading a masked record can tell what was removed without
// having to reproduce the match.
type secretRule struct {
	id string
	re *regexp.Regexp
	// group is the capture group holding the secret itself, for patterns whose
	// match includes surrounding syntax ("api_key = <value>"). 0 means the
	// whole match is the secret.
	group int
}

// secretRules are the credential shapes worth recognizing on sight.
//
// This is a curated set rather than a downloaded rule pack: it covers the
// shapes that actually appear in this service's traffic — cloud keys, personal
// access tokens, private keys, bearer headers, and the "name = value" form
// that configuration snippets arrive in — and it has no dependencies and no
// ambiguity about what it will match. Operators can extend it per deployment
// through ExtraSecretPatterns.
var secretRules = []secretRule{
	{id: "private-key", re: regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`)},
	{id: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{id: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{id: "github-token", re: regexp.MustCompile(`\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}\b`)},
	{id: "gitlab-token", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{id: "slack-token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{id: "openai-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)},
	{id: "bearer", re: regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	// Vendor key shapes that are recognizable on sight. These come before the
	// "name = value" rule so the marker names the vendor rather than the field.
	{id: "stripe-key", re: regexp.MustCompile(`\b(?:sk|pk)_live_[A-Za-z0-9]{24,}\b`)},
	{id: "google-api-key", re: regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}\b`)},
	{
		id:    "aws-secret-key",
		re:    regexp.MustCompile(`(?i)aws[_-]?secret[_-]?access[_-]?key["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`),
		group: 1,
	},
	// A password embedded in a URL. Matched whole because both halves are the
	// secret; the host is not worth keeping separately.
	{id: "credentials-in-url", re: regexp.MustCompile(`\bhttps?://[^:/@\s]+:[^/@\s]+@`)},
	// A database DSN: the credentials and the host are one unit.
	{id: "db-connection-string", re: regexp.MustCompile(`\b(?:mongodb|mysql|postgres|postgresql|mssql|oracle|redis)://[^:@\s]+:[^@\s]+@[^/\s]+`)},
	{id: "basic-auth", re: regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=]{20,}`)},
	{
		id:    "authorization-header",
		re:    regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?([^"'\s]{20,})`),
		group: 1,
	},
	// One-time codes are short-lived, which is why they are worth keeping out of
	// a transcript: the window in which one is useful outlives the conversation.
	{
		id:    "one-time-code",
		re:    regexp.MustCompile(`(?i)["']?(?:sms|mfa|2fa|otp|verification|verify)[_-]?(?:code|token|otp)["']?\s*[:=]\s*["']?([0-9A-Za-z]{4,12})`),
		group: 1,
	},
	{
		id:    "authenticator-seed",
		re:    regexp.MustCompile(`(?i)["']?(?:ga|google[_-]?auth)[_-]?(?:seed|secret)["']?\s*[:=]\s*["']?([A-Z2-7]{16,64})`),
		group: 1,
	},
	{
		id:    "session-id",
		re:    regexp.MustCompile(`(?i)(?:session[_-]?id|jsessionid)["']?\s*[:=]\s*["']?([A-Za-z0-9_-]{16,})`),
		group: 1,
	},

	// The "name = value" form. The value is the capture group so the key name
	// survives — knowing that a password field existed is useful, knowing the
	// password is not.
	//
	// It is deliberately last: it is the broadest rule, and running it first
	// would make every more specific marker above unreachable.
	{
		// The optional quote before the separator is what makes the JSON form
		// ("api_key":"…") match; without it only the bare "key: value" form
		// would, which is the less common one in captured traffic.
		id:    "assigned-secret",
		re:    regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret)\b["']?\s*[:=]\s*["']?([^\s"',;]{6,})`),
		group: 1,
	},
}

var (
	extraRulesMu sync.RWMutex
	extraRules   []secretRule
)

// ExtraSecretPatterns registers additional credential patterns for this
// process, each replaced wholesale with "[REDACTED:<name>]".
//
// It exists because a deployment knows secrets this package cannot: an internal
// token format, a vendor key. Registering a pattern that will not compile is a
// programming error and panics, which is better than a rule that silently never
// fires — the failure mode of a masking rule that does not match is a secret in
// the database.
func ExtraSecretPatterns(patterns map[string]string) {
	compiled := make([]secretRule, 0, len(patterns))
	for name, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			panic(fmt.Sprintf("datamask: invalid extra secret pattern %q: %v", name, err))
		}
		compiled = append(compiled, secretRule{id: name, re: re})
	}
	extraRulesMu.Lock()
	extraRules = append(extraRules, compiled...)
	extraRulesMu.Unlock()
}

// RedactSecrets replaces every recognized credential with a marker naming the
// rule that matched.
//
// The replacement is whole rather than partial: a partially masked credential
// is still a credential to anyone who knows its format, and the formats are
// public.
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}
	for _, rule := range allSecretRules() {
		text = applySecretRule(text, rule)
	}
	return text
}

// RedactSensitive removes both credentials and personal identifiers, each
// replaced wholesale.
//
// It is the form for text on its way *out* of the service — an answer about to
// be shown, a line about to be logged — where the question is not "is this
// recognizable" but "should this be here at all". Mask (the storage form) is
// deliberately different: an audit record keeps partially masked PII, because
// answering "who asked what" needs the value to stay legible.
func RedactSensitive(text string) string {
	if text == "" {
		return text
	}
	return RedactSecrets(MaskPIIFully(text))
}

// allSecretRules returns the built-in rules followed by any registered ones.
func allSecretRules() []secretRule {
	extraRulesMu.RLock()
	defer extraRulesMu.RUnlock()
	if len(extraRules) == 0 {
		return secretRules
	}
	out := make([]secretRule, 0, len(secretRules)+len(extraRules))
	out = append(out, secretRules...)
	out = append(out, extraRules...)
	return out
}

// applySecretRule replaces each match of rule, keeping the parts of the match
// that are not the secret itself.
func applySecretRule(text string, rule secretRule) string {
	marker := fmt.Sprintf(redactedFormat, rule.id)
	if rule.group == 0 {
		return rule.re.ReplaceAllLiteralString(text, marker)
	}
	return rule.re.ReplaceAllStringFunc(text, func(match string) string {
		// The submatch offsets are relative to match, which is what makes the
		// splice exact: looking the group up by value would replace the wrong
		// occurrence whenever the secret's text also appears in the prefix.
		idx := rule.re.FindStringSubmatchIndex(match)
		if rule.group*2+1 >= len(idx) || idx[rule.group*2] < 0 {
			return match
		}
		start, end := idx[rule.group*2], idx[rule.group*2+1]
		// Splice the marker over just the secret, leaving the surrounding
		// syntax ("password = ") intact so the text still reads as prose.
		return match[:start] + marker + match[end:]
	})
}
