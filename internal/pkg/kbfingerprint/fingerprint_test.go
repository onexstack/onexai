package kbfingerprint

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A key of the size the platform derives. The tests only need it to be constant.
var testKey = []byte("0123456789abcdef0123456789abcdef")

// strip removes every carrier this package writes, which is both the attacker's move
// and the tool the round-trip test needs.
func strip(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case runOpenRune, runCloseRune, bitZeroRune, bitOneRune:
			return -1
		}
		return r
	}, text)
}

// TestWeaveLeavesEveryOtherByteAlone is the load-bearing test in this file.
//
// The tempting implementation parses the body and serialises it back, which also
// re-quotes attributes, normalises entities, and rewrites code blocks. That failure is
// invisible in a browser and fatal to a programming course — the code a reader copies no
// longer being the code the author wrote. Asserting that removing the carriers yields the
// original byte for byte is what makes "this package only inserts characters" a fact
// rather than a claim.
func TestWeaveLeavesEveryOtherByteAlone(t *testing.T) {
	body := `<h2 id="md-heading-0">用 <code>viper.Get&lt;Type&gt;</code> 读取</h2>` +
		`<p>使用 ` + "`kill &lt;pid&gt;`" + ` 结束进程，或 <a href="https://example.com/x?a=1&amp;b=2" title="a &gt; b">看这里</a>。</p>` +
		"<pre class=\"kb-chroma\"><code class=\"language-bash\">printf &#39;%s\\n&#39; \"$x\"   \n</code></pre>" +
		`<ul><li>第一项</li><li>第二项</li></ul>` +
		"<table><thead><tr><th>a</th></tr></thead><tbody><tr><td>b</td></tr></tbody></table>"

	woven := Weave(body, testKey, "u-reader-1", "prd-course-go-quickstart", "kbn-ed0f92")
	require.NotEqual(t, body, woven, "nothing was written, so this proves nothing")
	assert.Equal(t, body, strip(woven))
}

// TestWeaveKeepsCodeAndHeadingsClean pins the two exclusions that are not about
// aesthetics.
//
// Code: a reader copies a command and runs it, so a carrier inside a fence is the
// anti-piracy measure breaking the course. Headings: they are also collected into the
// table of contents, and a heading whose text has been altered is a heading that no
// longer matches what a reader searched for.
func TestWeaveKeepsCodeAndHeadingsClean(t *testing.T) {
	body := `<h2 id="md-heading-0">标题在里面</h2>` +
		`<p>段落 在这里 可以 放。</p>` +
		`<pre><code>echo 代码 里面 不放</code></pre>` +
		`<p>行内 <code>code span 里面 不放</code> 也是。</p>`

	// A chapter short enough that the fallback places marks in every candidate space,
	// so the exclusions are exercised rather than merely available.
	woven := Weave(body, testKey, "u-reader-1", "prd-item", "kbn-node")

	// "Clean" is expressed as "stripping the carriers changes nothing", which needs no
	// invisible literal in this file and reads as the property it is.
	for _, section := range []struct{ from, to, what string }{
		{"<h2", "</h2>", "a heading"},
		{"<pre>", "</pre>", "a code fence"},
		{"<p>行内 <code>", "</code>", "an inline code span"},
	} {
		got := sectionOf(t, woven, section.from, section.to)
		assert.Equal(t, got, strip(got), "%s carries a mark", section.what)
	}

	// And the paragraph that may carry marks does.
	carrier := sectionOf(t, woven, "<p>段落", "</p>")
	assert.NotEqual(t, carrier, strip(carrier), "no paragraph was marked")
}

// sectionOf returns the slice of text from the first occurrence of from to the first
// occurrence of to after it. Substring arithmetic rather than an HTML parse: the input
// is a string this package produced, and a parser here would be a second opinion about
// a body the test already knows the shape of.
func sectionOf(t *testing.T, text, from, to string) string {
	t.Helper()
	i := strings.Index(text, from)
	require.GreaterOrEqual(t, i, 0, "%q not found", from)
	j := strings.Index(text[i:], to)
	require.GreaterOrEqual(t, j, 0, "%q not found after %q", to, from)
	return text[i : i+j+len(to)]
}

// TestMarksRoundTrip pins the property the whole scheme rests on: a mark written into a
// document can be read back out of an excerpt of it.
func TestMarksRoundTrip(t *testing.T) {
	const reader = "u-9f2c1a"

	// A chapter the size of a real one, so that the density rule is exercised rather
	// than the minimum.
	body := strings.Repeat("<p>一段足够长的正文，里面有空格 可以 放置 标记。</p>", 200)
	woven := Weave(body, testKey, reader, "prd-item", "kbn-node")

	marks := Extract(woven)
	require.Len(t, marks, runsFor(len(body)), "every run carries a whole mark")
	for _, mark := range marks {
		assert.Equal(t, Of(testKey, reader), mark)
	}

	// A section — which is what a leak looks like — carries one. A paragraph on its own
	// does not, and cannot; that case is the visible watermark's, not this one's.
	assert.NotEmpty(t, Extract(woven[:len(woven)/2]), "half the chapter carried no mark")
}

// TestExtractIgnoresWhatIsNotAMark pins the decoder's refusal. A delimiter in ordinary
// prose is a false positive, and a decoder that guessed would be worse than one that
// returned nothing — it would name a reader for a text that had never been marked.
func TestExtractIgnoresWhatIsNotAMark(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"no delimiters at all", "普通的正文，没有任何标记。"},
		{"an opening delimiter with nothing after it", "正文" + runOpen},
		{"an opening delimiter never closed", "正文" + runOpen + string(rune(bitZeroRune))},
		{"a run holding a character that is not a carrier", "正文" + runOpen + string(rune(bitZeroRune)) + "x" + runClose},
		{"a run that is too short", "正文" + runOpen + string(rune(bitZeroRune)) + runClose},
		{"a run that is too long", "正文" + runOpen + strings.Repeat(string(rune(bitZeroRune)), bitsPerRun+1) + runClose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, Extract(tc.text))
		})
	}
}

// TestMarksArePerReaderAndPerChapter pins the two derivations.
//
// Per reader, because a mark that did not name the reader would be decoration. Per
// chapter, because the *places* must not be predictable from one chapter to the next —
// a reader who could locate them in one chapter could locate them in all of them.
func TestMarksArePerReaderAndPerChapter(t *testing.T) {
	assert.NotEqual(t, Of(testKey, "u-one"), Of(testKey, "u-two"),
		"two readers got the same mark")
	assert.NotEqual(t, Of(testKey, "u-one"), Of([]byte("another-key-another-key-another"), "u-one"),
		"the mark does not depend on the key")

	body := strings.Repeat("<p>一段足够长的正文，里面有空格 可以 放置 标记。</p>", 200)
	first := Weave(body, testKey, "u-one", "prd-item", "kbn-a")
	second := Weave(body, testKey, "u-one", "prd-item", "kbn-b")
	assert.NotEqual(t, first, second, "two chapters carry their marks in the same places")

	// The same inputs, once more: a stable place is what keeps the browser's sixty
	// seconds of cache honest and what lets a decoder work from a fragment.
	assert.Equal(t, first, Weave(body, testKey, "u-one", "prd-item", "kbn-a"))
}

// TestWeaveRefusesToGuess documents the fail-open path. A deployment with no derived key,
// or a token that named no user, gets the chapter unmarked rather than a chapter refused:
// the mark is an aid to attribution, and the page is what the reader paid for.
func TestWeaveRefusesToGuess(t *testing.T) {
	body := "<p>正文 有 空格。</p>"
	assert.Equal(t, body, Weave(body, nil, "u-one", "prd-item", "kbn-a"))
	assert.Equal(t, body, Weave(body, []byte{}, "u-one", "prd-item", "kbn-a"))
	assert.Equal(t, body, Weave(body, testKey, "", "prd-item", "kbn-a"))
}

// TestWeaveHandlesABodyWithNowhereToWrite covers the shape a one-word chapter has: a
// short body, or one whose only spaces are inside a fence. It must not panic and must not
// tack a mark onto a boundary it was told to avoid.
func TestWeaveHandlesABodyWithNowhereToWrite(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"one short word", "<p>短</p>"},
		{"only code", "<pre><code>make  build</code></pre>"},
		{"only a heading", "<h2 id=\"md-heading-0\">只有 标题</h2>"},
		{"unterminated tag", "<p>正文 有 空格 <"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			woven := Weave(tc.body, testKey, "u-one", "prd-item", "kbn-a")
			assert.Equal(t, tc.body, strip(woven))
			if len(insertionPoints(tc.body)) == 0 {
				assert.Equal(t, tc.body, woven, "a mark was written where there was no room")
			}
		})
	}
}

// TestWatermarkTextNamesTheReaderAndTheItem pins what a screenshot answers: whose account
// this was, and which course.
func TestWatermarkTextNamesTheReaderAndTheItem(t *testing.T) {
	got := WatermarkText("u-9f2c1a", "prd-course-go-quickstart")
	assert.Contains(t, got, "u-9f2c1a")
	assert.Contains(t, got, "prd-course-go-quickstart")
}
