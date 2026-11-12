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

package larkcard

import (
	"strings"
	"testing"
)

// TestArgumentsDigest pins what a decision line says about the call it is
// asking about. The reader is deciding whether to let the call run, so the test
// is written from that side: the values that would change the decision have to
// be visible, and the things that would make the line unreadable must not be.
func TestArgumentsDigest(t *testing.T) {
	t.Run("top-level scalars are shown as key=value", func(t *testing.T) {
		got := ArgumentsDigest(`{"env":"edu-onex","service":"content"}`)
		if got != "（env=edu-onex service=content）" {
			t.Errorf("ArgumentsDigest = %q, want the two members in key order", got)
		}
	})

	t.Run("the same call always reads the same way", func(t *testing.T) {
		// JSON objects have no order, but a log does: the same call rendered twice
		// must produce the same line, or two pauses over one call look like two
		// different calls.
		a := ArgumentsDigest(`{"service":"content","env":"edu-onex","confirm":true}`)
		b := ArgumentsDigest(`{"confirm":true,"env":"edu-onex","service":"content"}`)
		if a != b {
			t.Errorf("key order changed the line: %q vs %q", a, b)
		}
	})

	t.Run("non-string scalars keep their JSON spelling", func(t *testing.T) {
		got := ArgumentsDigest(`{"confirm":true,"replicas":3,"ratio":1.5}`)
		if got != "（confirm=true ratio=1.5 replicas=3）" {
			t.Errorf("ArgumentsDigest = %q, want booleans and numbers printed as they were", got)
		}
	})

	t.Run("a list becomes its members", func(t *testing.T) {
		// A list of service names is the whole substance of the decision: "which
		// services will this deploy touch" has no other answer on the line.
		got := ArgumentsDigest(`{"services":["content","gateway"]}`)
		if got != "（services=content,gateway）" {
			t.Errorf("ArgumentsDigest = %q, want the members joined", got)
		}
	})

	t.Run("a list of objects contributes nothing", func(t *testing.T) {
		// Each member renders as {}, which says only that something was there, so
		// the member drops out entirely and the line keeps the value that was
		// actually readable.
		got := ArgumentsDigest(`{"steps":[{"a":1},{"b":2}],"env":"edu-onex"}`)
		if got != "（env=edu-onex）" {
			t.Errorf("ArgumentsDigest = %q, want the object list dropped", got)
		}
	})

	t.Run("a nested object is summarized, not printed", func(t *testing.T) {
		got := ArgumentsDigest(`{"spec":{"replicas":2},"env":"edu-onex"}`)
		if got != "（env=edu-onex spec={}）" {
			t.Errorf("ArgumentsDigest = %q, want the nested document reduced to {}", got)
		}
	})

	t.Run("empty members are dropped", func(t *testing.T) {
		// A tool that takes ten optional fields sets three of them. Padding the
		// line with the seven it left empty would hide the three that matter.
		got := ArgumentsDigest(`{"env":"edu-onex","namespace":"","service":null,"dry":""}`)
		if got != "（env=edu-onex）" {
			t.Errorf("ArgumentsDigest = %q, want only the member that was set", got)
		}
	})

	t.Run("nothing to say is said as nothing", func(t *testing.T) {
		// Every caller appends the result to a tool name, so the empty string is
		// the contract for "no arguments". A call with no arguments is the same
		// call however its JSON spells that — `{}`, `{ }` and `null` all mean the
		// tool takes none — and an object whose every member was empty is one too.
		for _, in := range []string{
			"", "   ", "{}", "{ }", "null",
			`{"namespace":"","service":null}`,
		} {
			if got := ArgumentsDigest(in); got != "" {
				t.Errorf("ArgumentsDigest(%q) = %q, want empty", in, got)
			}
		}
	})

	t.Run("a credential in the arguments is redacted", func(t *testing.T) {
		// The digest is flattened before it is redacted, so the JSON spelling the
		// redactor's other pattern covers is gone by then: the `key=value` form is
		// the one that has to match here.
		got := ArgumentsDigest(`{"api_token":"tok_live_9f3a","env":"edu-onex"}`)
		if strings.Contains(got, "tok_live_9f3a") {
			t.Errorf("secret not redacted: %q", got)
		}
		if !strings.Contains(got, "redacted") {
			t.Errorf("nothing was redacted: %q", got)
		}
	})

	t.Run("a long list of arguments is truncated", func(t *testing.T) {
		// The line is one line of a log. Past the limit it is cut rather than
		// wrapped, and the ellipsis is ASCII so the rune count stays honest on a
		// card that measures width in characters.
		got := ArgumentsDigest(`{"a":"` + strings.Repeat("x", 200) + `"}`)
		if !strings.HasSuffix(got, "...）") {
			t.Errorf("ArgumentsDigest = %q, want it truncated", got)
		}
		if max := argumentsDigestLimit + len("（...）"); len([]rune(got)) > max {
			t.Errorf("digest is %d runes, want at most %d", len([]rune(got)), max)
		}
	})

	t.Run("a value that is not an object is still shown", func(t *testing.T) {
		// An approval that shows no arguments invites approving a call nobody has
		// read, so text that cannot be parsed as an object is printed as it stands
		// rather than dropped.
		got := ArgumentsDigest("  rm -rf   /tmp/x  ")
		if got != "（rm -rf /tmp/x）" {
			t.Errorf("ArgumentsDigest = %q, want the text collapsed to one line", got)
		}
		if got := ArgumentsDigest("not json {"); got != "（not json {）" {
			t.Errorf("ArgumentsDigest = %q, want unparseable text passed through", got)
		}
	})
}
