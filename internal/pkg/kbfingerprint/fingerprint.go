// NOTE: this package is a mirror of onex/internal/pkg/kbfingerprint. The
// algorithm, the carrier runes, and SubkeyLabel must stay identical in both
// copies, or cmd/ox-kbtrace cannot decode what this one writes. Change both or
// neither; the round-trip tests here are the copy that runs in this module.
//
// Package kbfingerprint marks a knowledge base chapter with the identity of the reader
// it was rendered for, so that a copy which leaves the platform can be traced back to
// the session it left from.
//
// # What this is for, and what it is not
//
// The adversary here holds a valid credential — they are a reader who paid, or somebody
// using their account. No control on the page stops them from reading it slowly, and
// none stops them from republishing what they read. What is left is attribution: make
// every copy carry the identity of whoever received it, so that when one turns up
// somewhere it names a person rather than a course.
//
// **This is deterrence and attribution, not prevention.** An adversary who knows the
// technique strips the carriers — they are invisible format characters in the text, and
// a script that deletes a range of Unicode code points defeats them in one line. What
// they do defeat is the lazy copy: select-all, copy, paste somewhere else. That is the
// overwhelmingly common case, and it is the case nothing else on the platform covers —
// a rate limiter bounds a rate, a `noindex` header keeps search engines out, and neither
// says anything about a reader who pastes a chapter into a document.
//
// # The shape of a mark
//
// One mark is 32 bits — `fingerprintBytes` bytes — enough to name a reader uniquely
// against any plausible population, and short enough that the carrier run does not
// become a target in its own right. The bits are written as a run of invisible
// characters:
//
//	runOpen   opens a run
//	bitZero   a zero bit
//	bitOne    a one bit
//	runClose  closes a run
//
// The delimiters are what make a mark position-independent: a decoder handed an excerpt
// finds a run wherever it happens to be, without knowing which chapter it came from or
// where in that chapter the run was placed. That matters because the thing being decoded
// is by definition a fragment somebody else chose.
//
// Each run carries the whole 32 bits, and a chapter carries several runs at different
// points, because the copy that leaks is usually a few paragraphs rather than the whole
// document. Redundancy across the document is what gives an excerpt a good chance of
// carrying at least one complete mark.
//
// # Why the carriers are written as code points
//
// Not as the characters themselves, and not as `\u` escapes: a source file that contains
// an invisible character is a file where a diff can delete a carrier without showing
// anything, and where an editor's whitespace cleanup can change what the package means.
// Writing the code point keeps the whole scheme legible in ASCII and makes the numbers
// the documentation.
//
// # Why the marks do not collide with the code a reader copies
//
// Carriers are never placed inside `<pre>` or `<code>`. This is a programming course: a
// reader copying a command out of it must get the bytes the author wrote, and a
// fingerprint woven into a shell command would be exactly the bug that makes an
// anti-piracy measure cost more than it is worth.
package kbfingerprint

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

// The four invisible format characters a mark is written with, as code points.
//
// All four are zero-width and are preserved by copy and paste, which is the whole
// requirement: a carrier that a text selection drops would only travel with the HTML,
// and the HTML is not what gets pasted into a document.
const (
	runOpenRune  = 0x200C // ZERO WIDTH NON-JOINER
	runCloseRune = 0x200D // ZERO WIDTH JOINER
	bitZeroRune  = 0x200B // ZERO WIDTH SPACE
	bitOneRune   = 0x2060 // WORD JOINER
)

// SubkeyLabel is the domain separation the platform derives this package's key with,
// through capability.Verifier.DeriveSubkey.
//
// It lives here rather than at the call sites because there are two of them and they are
// two programs: the service that writes a mark and cmd/ox-kbtrace, which reads one back.
// A label spelled out in both places is a label that eventually differs, and the symptom
// would be a tool that decodes every document to nobody — after the copy has already
// leaked, which is the one moment the answer was needed.
const SubkeyLabel = "kb-fingerprint-v1"

// The labels that separate this key from every other use of it.
const (
	markLabel = "reader-mark/v1"
	// positionLabel separates the key used to choose where marks go from the key used
	// to write what they say. Two purposes, two labels; sharing one would let a decoder
	// predict positions, which is a small leak but a free one to avoid.
	positionLabel = "reader-mark-position/v1"
)

// fingerprintBytes is the size of a mark: 4 bytes, 32 bits.
const fingerprintBytes = 4

// bitsPerRun is the number of carrier characters in one mark.
const bitsPerRun = fingerprintBytes * 8

// How many copies of the mark a chapter carries.
//
// # Why the count scales with the chapter
//
// The copy that leaks is an excerpt, so the question is how much text an excerpt has to
// contain before it carries a mark. A fixed three is the wrong answer for a long chapter
// and the right one for a short one: three marks in forty paragraphs leaves a copied
// section unmarked, which is the case the whole scheme exists for. One mark per
// `bytesPerRun` of body makes the density a property of the text rather than of the
// chapter's length, so a section carries a mark roughly regardless of how much chapter
// surrounds it.
//
// # What this does not solve, and what covers it
//
// A *very* short excerpt — a paragraph, a sentence — still carries no mark, and no
// density that keeps the carriers from becoming blindingly obvious would change that.
// That case is covered by the other half of the pair: the visible watermark sits over the
// viewport, so it is in every screenshot and every photograph of a screen, at the cost of
// being removable from a copy. The two mechanisms fail in opposite directions on purpose
// — one survives copying and needs volume, the other needs nothing and survives only
// until somebody edits it out.
const (
	bytesPerRun       = 1000
	minRunsPerChapter = 3
	maxRunsPerChapter = 20
)

// runsFor returns how many marks a body of this length carries.
func runsFor(bodyLen int) int {
	runs := bodyLen / bytesPerRun
	if runs < minRunsPerChapter {
		return minRunsPerChapter
	}
	if runs > maxRunsPerChapter {
		return maxRunsPerChapter
	}
	return runs
}

// The delimiters as strings, for the scanning in Extract. Derived from the code points
// above rather than repeated, so there is one place a carrier is spelled.
var (
	runOpen  = string(rune(runOpenRune))
	runClose = string(rune(runCloseRune))
)

// Mark is the 32 bits that name one reader.
type Mark [fingerprintBytes]byte

// Of returns the mark for a reader.
//
// # Why it depends on the reader and nothing else
//
// It is tempting to fold the item and the chapter in, so that a mark also says where the
// copy came from. That would make decoding need a candidate per (reader, item, chapter):
// the tool that has to answer "who leaked this" would have to try every combination, and
// it does not know the chapter of a fragment. Keeping the mark a function of the reader
// alone means decoding is one HMAC per candidate reader, and the chapter is not a
// mystery — it is the text that leaked.
//
// An empty key is a programming mistake rather than a request fault, and it is answered
// as one at the call site: a mark computed from no key would be a mark that decodes to
// nobody while looking entirely normal.
func Of(key []byte, readerID string) Mark {
	var mark Mark
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(markLabel))
	mac.Write([]byte{0})
	mac.Write([]byte(readerID))
	copy(mark[:], mac.Sum(nil)[:fingerprintBytes])
	return mark
}

// WatermarkText is the sentence a visible watermark repeats over the chapter.
//
// It names the reader and the item, in that order, because the two questions asked of a
// photograph of a page are "whose account was this" and "which course"; the item is the
// one that can be answered without the platform's help.
//
// It is deliberately not a timestamp. A timestamp would make the page differ between two
// renders of the same chapter, which would cost the browser cache that makes paging back
// and forth free, and would suggest a precision the mark does not have: the visible
// watermark identifies an account, and the invisible mark is what survives a copy.
func WatermarkText(readerID, itemKey string) string {
	return readerID + " · " + itemKey
}

// Weave returns body with a reader's marks written into it.
//
// # Why this is string surgery and not an HTML tree
//
// The obvious implementation parses the body with an HTML parser, walks it, and
// serialises it back. It would also re-serialise the whole chapter — re-quoting every
// attribute, normalising every entity, and changing the bytes of every code block along
// the way. This package exists to mark a document, not to rewrite it, so it does not: it
// inserts characters into the body and leaves every other byte where it was.
//
// The scanner that makes that safe rests on one property of the input, which is worth
// stating because everything else depends on it: **goldmark escapes `<` in text**
// (`util.EscapeHTML`, and chroma's formatter does the same), so in this body a literal
// `<` only ever opens a tag. A `<` therefore ends a text run with no ambiguity, and
// looking for one is not the usual mistake of scanning HTML with a state machine that
// cannot know whether a `<` is markup.
func Weave(body string, key []byte, readerID, itemKey, nodeID string) string {
	if readerID == "" || len(key) == 0 {
		return body
	}

	run := encode(Of(key, readerID))
	positions := choosePositions(body, key, readerID, itemKey, nodeID, runsFor(len(body)))
	if len(positions) == 0 {
		return body
	}

	// Spliced from the end so that no insertion invalidates a position still to come.
	var out strings.Builder
	out.Grow(len(body) + len(positions)*len(run))
	last := 0
	for _, at := range positions {
		out.WriteString(body[last:at])
		out.WriteString(run)
		last = at
	}
	out.WriteString(body[last:])
	return out.String()
}

// Extract returns every mark the text carries, in the order they appear.
//
// It is the decoding half, used by cmd/ox-kbtrace and by the tests. A run whose body
// contains anything other than carriers is not a mark and is skipped rather than guessed
// at: a start delimiter in ordinary prose is a false positive, and a decoder that
// reported a mark for one would be worse than a decoder that reported none.
func Extract(text string) []Mark {
	var marks []Mark
	rest := text
	for {
		start := strings.Index(rest, runOpen)
		if start < 0 {
			return marks
		}
		rest = rest[start+len(runOpen):]

		end := strings.Index(rest, runClose)
		if end < 0 {
			return marks
		}
		if mark, ok := decode(rest[:end]); ok {
			marks = append(marks, mark)
		}
		rest = rest[end+len(runClose):]
	}
}

// encode writes a mark as the delimited run of carriers Extract reads back.
func encode(mark Mark) string {
	var b strings.Builder
	b.Grow(bitsPerRun)
	b.WriteRune(runOpenRune)
	for i := 0; i < bitsPerRun; i++ {
		if bitAt(mark, i) {
			b.WriteRune(bitOneRune)
		} else {
			b.WriteRune(bitZeroRune)
		}
	}
	b.WriteRune(runCloseRune)
	return b.String()
}

// decode reads a run's body back into a mark.
//
// The second result is false for anything that is not exactly one carrier per bit, which
// is what keeps a stray delimiter in the text from being reported as a mark.
func decode(run string) (Mark, bool) {
	var mark Mark
	bits := 0
	for _, r := range run {
		switch r {
		case bitZeroRune:
			bits++
		case bitOneRune:
			setBit(&mark, bits)
			bits++
		default:
			return Mark{}, false
		}
	}
	if bits != bitsPerRun {
		return Mark{}, false
	}
	return mark, true
}

// bitAt reports bit i of a mark, most significant first.
func bitAt(mark Mark, i int) bool {
	return mark[i/8]&(1<<(7-uint(i%8))) != 0
}

// setBit sets bit i of a mark.
func setBit(mark *Mark, i int) {
	mark[i/8] |= 1 << (7 - uint(i%8))
}

// choosePositions picks the byte offsets the runs are inserted at.
//
// # Where a mark may go
//
// Only at a space that sits between two pieces of text, and never inside an element
// whose text has to survive a copy byte for byte (`<pre>`, `<code>`) or whose text is
// shown somewhere else (`<h1>`–`<h6>` are also collected into the table of contents, and
// while the table of contents is built from the body *before* this runs, keeping headings
// clean means the two can be reordered later without a silent bug).
//
// # Why the places are the same on every render
//
// They are derived from the reader, the item and the chapter — not from a per-request
// random value. Randomising them would make two renders of one chapter differ, which
// costs the browser cache that makes paging back and forth free, and buys nothing: the
// decoder finds a run by its delimiters wherever it is, so a stable place is not a weaker
// place. What a stable place does give up is stated plainly under # What this is for — an
// adversary with two accounts can diff two copies and learn where the marks sit. They
// still have to strip all of them, in both copies, which is the same operation as
// stripping blindly.
func choosePositions(body string, key []byte, readerID, itemKey, nodeID string, want int) []int {
	candidates := insertionPoints(body)
	if len(candidates) == 0 {
		return nil
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(positionLabel))
	mac.Write([]byte{0})
	mac.Write([]byte(readerID))
	mac.Write([]byte{0})
	mac.Write([]byte(itemKey))
	mac.Write([]byte{0})
	mac.Write([]byte(nodeID))
	stream := mac.Sum(nil)

	chosen := make([]int, 0, want)
	seen := make(map[int]bool, want)

	// As many marks as the stream has words for, spread over the whole candidate list
	// by taking one word per mark rather than three consecutive words.
	for word := 0; word*4+4 <= len(stream) && len(chosen) < want; word++ {
		at := int(binary.BigEndian.Uint32(stream[word*4:]) % uint32(len(candidates)))
		if seen[at] {
			continue
		}
		seen[at] = true
		chosen = append(chosen, candidates[at])
	}

	// A chapter with fewer candidate spaces than marks is a short chapter, not a
	// failure: one mark in a short chapter is still one mark. The fallback walks the
	// list in evenly spaced steps rather than from the start, so the extra marks do not
	// all land in the first paragraph — which is the one place an excerpt is least
	// likely to begin.
	step := 1
	if len(candidates) > want {
		step = len(candidates) / want
	}
	for i := 0; len(chosen) < want && i < len(candidates); i += step {
		if seen[i] {
			continue
		}
		seen[i] = true
		chosen = append(chosen, candidates[i])
	}

	// Sorted, because Weave splices from the end.
	sortInts(chosen)
	return chosen
}

// insertionPoints returns the byte offsets in body that a mark may be placed at, in
// document order.
func insertionPoints(body string) []int {
	var points []int
	depth := 0 // non-zero inside an element whose text must be left alone

	for i := 0; i < len(body); {
		if body[i] == '<' {
			end := strings.IndexByte(body[i:], '>')
			if end < 0 {
				break
			}
			name, closing, selfClosing := tagName(body[i : i+end+1])
			if namesToSkip[name] {
				switch {
				case closing:
					if depth > 0 {
						depth--
					}
				case !selfClosing:
					depth++
				}
			}
			i += end + 1
			continue
		}

		// A text run, ended by the next '<' — which, in this body, always opens a tag.
		end := strings.IndexByte(body[i:], '<')
		if end < 0 {
			end = len(body) - i
		}
		if depth == 0 {
			points = append(points, spacesIn(body[i:i+end], i)...)
		}
		i += end
	}
	return points
}

// spacesIn returns the offsets just after each space in a text run.
//
// Just after, so a mark lands between two words rather than inside one: a carrier
// inserted inside a word is invisible on the screen and breaks the word everywhere else,
// including the browser's own find-in-page. It costs one convenience of the scheme — a
// mark cannot be hidden from a search that looks for the word it sits in — and that is
// the right way round.
func spacesIn(text string, offset int) []int {
	var points []int
	for i := 0; i < len(text); i++ {
		// Staying below len-1 keeps the insertion strictly inside the run rather than
		// between a tag and the text it wraps.
		if text[i] == ' ' && i < len(text)-1 {
			points = append(points, offset+i+1)
		}
	}
	return points
}

// namesToSkip are the elements whose text a mark may not enter.
var namesToSkip = map[string]bool{
	// Copied and run. A mark in here would be the anti-piracy measure that broke the
	// course.
	"pre": true, "code": true,
	// Shown somewhere else as well: a chapter's headings are listed in the table of
	// contents, and a heading with invisible characters in it is a heading whose text
	// no longer matches what a reader searched for.
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	// Not text at all.
	"script": true, "style": true, "svg": true, "textarea": true,
}

// tagName reads an element's name out of a tag, and whether it closes or self-closes.
func tagName(tag string) (name string, closing, selfClosing bool) {
	inner := strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">")
	if strings.HasPrefix(inner, "/") {
		closing = true
		inner = inner[1:]
	}
	selfClosing = strings.HasSuffix(inner, "/")
	inner = strings.TrimSuffix(inner, "/")

	if end := strings.IndexAny(inner, " \t\n\r"); end >= 0 {
		inner = inner[:end]
	}
	return strings.ToLower(inner), closing, selfClosing
}

// sortInts is an insertion sort. The list is at most a handful of entries, and importing
// a sort package to order them is not worth the reader's attention.
func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}
