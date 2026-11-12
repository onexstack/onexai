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

package bot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// TestMarkdownCardSizeCapFallsBackToText pins the decision behind
// SendMarkdownCard's fallback. The send itself cannot be driven here — the
// Lark client is a concrete SDK type with no seam to fake — so what is tested
// is the predicate the caller branches on, plus the property that makes the
// fallback safe.
func TestMarkdownCardSizeCapFallsBackToText(t *testing.T) {
	marshal := func(t *testing.T, v interface{}) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}

	t.Run("short body is sent as a card", func(t *testing.T) {
		card := buildMarkdownCard("回答", "答案是 42。", "blue")
		if !fitsAsCard(card) {
			t.Errorf("a one-line answer did not fit, want it sent as a card")
		}
	})

	t.Run("long body does not fit", func(t *testing.T) {
		card := buildMarkdownCard("", strings.Repeat("x", 64*1024), "blue")
		if fitsAsCard(card) {
			t.Errorf("a 64 KiB body fit a %d-byte card", larkcard.MaxCardJSONBytes)
		}
	})

	// The boundary has to be the documented constant, not merely "somewhere".
	// The body is appended verbatim to the card JSON, so for an ASCII body
	// with nothing to escape, one body byte is one payload byte and the
	// threshold can be located exactly.
	t.Run("boundary sits exactly at the limit", func(t *testing.T) {
		overhead := len(marshal(t, buildMarkdownCard("", "", "blue")))
		body := func(n int) string { return strings.Repeat("x", n) }

		atLimit := buildMarkdownCard("", body(larkcard.MaxCardJSONBytes-overhead), "blue")
		if got := len(marshal(t, atLimit)); got != larkcard.MaxCardJSONBytes {
			t.Fatalf("test body is miscalibrated: card is %d bytes, want exactly %d",
				got, larkcard.MaxCardJSONBytes)
		}
		if !fitsAsCard(atLimit) {
			t.Errorf("a card of exactly %d bytes did not fit", larkcard.MaxCardJSONBytes)
		}

		overLimit := buildMarkdownCard("", body(larkcard.MaxCardJSONBytes-overhead+1), "blue")
		if fitsAsCard(overLimit) {
			t.Errorf("a card one byte over %d fit", larkcard.MaxCardJSONBytes)
		}
	})

	// A title is part of the payload. If the fallback ignored it, a body that
	// fits untitled would be sent as a card and lose its title.
	t.Run("title counts against the budget", func(t *testing.T) {
		overhead := len(marshal(t, buildMarkdownCard("", "", "blue")))
		budget := larkcard.MaxCardJSONBytes - overhead
		title := strings.Repeat("t", 200)

		if !fitsAsCard(buildMarkdownCard("", strings.Repeat("x", budget), "blue")) {
			t.Fatal("untitled card at the budget did not fit")
		}
		if fitsAsCard(buildMarkdownCard(title, strings.Repeat("x", budget), "blue")) {
			t.Errorf("adding a %d-byte title did not push the card over the limit", len(title))
		}
	})

	// The fallback is only acceptable because it loses nothing. A body over
	// the card limit is far over the per-message text limit too, so if the
	// splitter dropped or duplicated bytes the substitution would trade one
	// silent failure for another.
	t.Run("fallback text loses nothing", func(t *testing.T) {
		long := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 3000)
		if fitsAsCard(buildMarkdownCard("", long, "blue")) {
			t.Fatalf("%d bytes fit a card; this case no longer exercises the fallback", len(long))
		}

		chunks := larkcard.SplitByBudget(long, larkcard.TextChunkBytes)
		if len(chunks) < 2 {
			t.Fatalf("split into %d chunk(s), want the oversized body broken up", len(chunks))
		}
		for i, c := range chunks {
			if len(c) > larkcard.TextChunkBytes {
				t.Errorf("chunk %d is %d bytes, over the %d budget", i, len(c), larkcard.TextChunkBytes)
			}
		}
		if got := strings.Join(chunks, ""); got != long {
			t.Errorf("reassembled %d bytes, want %d — the fallback would lose content",
				len(got), len(long))
		}
	})
}
