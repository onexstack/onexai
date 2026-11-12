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

package chat

import "strings"

// SummaryBrief is what the model answers, in place of a summary, when a ticket is
// not worth one.
//
// # Why a marker rather than an empty answer
//
// A ticket's summary is written every time a ticket closes, so "is this worth
// recording" has to be answered by something. Three options, and this is the one
// that keeps the ticket readable:
//
//   - an empty summary — indistinguishable, in the row, from a generation that
//     failed or has not run, so an operator asking "did we look at this one" gets
//     no answer;
//   - a state column — a second carrier of a fact the text already carries;
//   - **this**: the answer says what it is. A reader who opens the ticket sees
//     that somebody looked and that there was nothing to write up.
//
// It lives here, in the repository's shared-vocabulary package, because four
// places have to agree on the exact string and none of them may be the definition:
// the prompt asks for it, the store's candidate query excludes it, the read
// surface's eligibility check excludes it, and a test pins it. A literal in each
// of those is four things that agree until one is edited.
const SummaryBrief = "记录简略，无需复盘"

// CanonicalSummary folds the model's answer into the value the column is allowed to
// hold: the marker itself, or the write-up unchanged.
//
// # Why this exists at all
//
// `IsBriefSummary` tolerates trailing punctuation, because a model asked for one
// sentence sometimes ends it with one. The **query** cannot: it runs in SQL, where a
// tolerant comparison would have to strip a character *set* from the end, and
// PostgreSQL, MySQL and SQLite spell that three different ways. Two comparisons that
// mean different things are the failure the constants in this file exist to prevent —
// the query excluding one spelling while the model answers another publishes exactly
// the ticket the model declined, and publishes it quietly.
//
// So the tolerance lives here, at the single point where a model's answer becomes a
// stored value. Everything downstream compares exact strings: the query excludes the
// marker, the eligibility check compares against it, and the marker the prompt asks
// for is the marker they all mean.
//
// The cost, stated: a value written by hand — an UPDATE from a shell — that carries
// the punctuation is not recognised as brief. Nothing but the model writes this
// column, and it goes through here.
func CanonicalSummary(text string) string {
	if IsBriefSummary(text) {
		return SummaryBrief
	}
	return text
}

// IsBriefSummary reports whether a ticket's summary is the marker rather than a
// write-up.
//
// Trailing punctuation is tolerated because a model asked to output one sentence
// sometimes ends it with one: 「记录简略，无需复盘。」 is the same answer, and a
// check that missed it would publish a ticket the model had just declined to
// write up. Nothing else is tolerated — an answer that carries the marker and
// more is a write-up, and only an answer that IS the marker is brief.
func IsBriefSummary(summary string) bool {
	trimmed := strings.TrimSpace(summary)
	trimmed = strings.TrimRight(trimmed, "。.！!~～ ")
	return trimmed == SummaryBrief
}
