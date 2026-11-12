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

package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"
)

// TestI18nFallbackChain pins the resolution order, one leg at a time.
//
// Each leg is what keeps a partial catalog usable. A translation exists for one
// language and not another; a language has no catalog at all; the string was
// never translated by anyone — and in every case the user gets a real sentence,
// because the source strings are themselves the Chinese the service has always
// sent.
func TestI18nFallbackChain(t *testing.T) {
	// A catalog with one string translated into three languages and another into
	// two, so each leg of the chain is reachable.
	tr := New(Catalog{
		ZH:   {"a": "zh-a", "b": "zh-b"},
		ZHTW: {"a": "zhtw-a"},
		EN:   {"a": "en-a"},
	})

	for _, tc := range []struct {
		name   string
		lang   Language
		source string
		want   string
	}{
		{name: "the requested language wins", lang: EN, source: "a", want: "en-a"},
		{name: "a language without the string falls back", lang: ZHTW, source: "b", want: "zh-b"},
		{name: "a string nobody translated is its own translation", lang: EN, source: "c", want: "c"},
		{name: "the source language is served by the catalog", lang: ZH, source: "a", want: "zh-a"},
		{name: "a language with no catalog at all falls back", lang: "ja", source: "b", want: "zh-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tr.T(tc.lang, tc.source); got != tc.want {
				t.Errorf("T(%s, %q) = %q, want %q", tc.lang, tc.source, got, tc.want)
			}
		})
	}
}

// TestNilTranslatorRendersTheSource pins that a caller with no catalog produces
// the message rather than an empty string. /doctor is reached with a nil runtime
// before it can translate, and an empty reply there would look like a command
// that silently did nothing.
func TestNilTranslatorRendersTheSource(t *testing.T) {
	var tr *Translator

	if got := tr.T(EN, "服务未初始化，无法自检"); got != "服务未初始化，无法自检" {
		t.Errorf("T() = %q, want the source string", got)
	}
	if got := tr.T(EN, "会话：%d 个", 3); got != "会话：3 个" {
		t.Errorf("T() = %q, want the source string with its arguments applied", got)
	}
}

// TestArgumentsAreOnlyAppliedWhenGiven pins the guard that keeps an untranslated
// string from being mangled: most sources have no arguments, and running one
// through fmt.Sprintf would interpret any percent sign it happens to contain.
func TestArgumentsAreOnlyAppliedWhenGiven(t *testing.T) {
	if got := T(EN, "成功率 100% 的接口"); got != "成功率 100% 的接口" {
		t.Errorf("T() = %q, want the source string untouched", got)
	}
	if got := T(EN, "%s：%d 个", "llm", 2); got != "llm：2 个" {
		t.Errorf("T() = %q, want the arguments applied", got)
	}
}

// TestParseLanguage pins what a deployment may write in the config, and that
// what it may not write is refused rather than quietly replaced.
func TestParseLanguage(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Language
		ok   bool
	}{
		{in: "", want: Default, ok: true},
		{in: "zh", want: ZH, ok: true},
		{in: " en ", want: EN, ok: true},
		{in: "zh-tw", want: ZHTW, ok: true},
		{in: "ZH_TW", want: ZHTW, ok: true},
		{in: "ja", ok: false},
		{in: "english", ok: false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseLanguage(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseLanguage(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("ParseLanguage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEveryTranslatedCallHasAnEntry is the guard on the catalog's coverage.
//
// The catalog is partial by design, which is what makes it additive: an
// untranslated string is sent as written. The failure that design invites is the
// opposite one — a new call site asking for a translation the catalog does not
// have, which is invisible at runtime because it looks exactly like a string
// nobody has got round to translating. This walks the source of the commands
// that were translated and requires an entry for each source string they ask
// about.
func TestEveryTranslatedCallHasAnEntry(t *testing.T) {
	files := []string{
		"../command/ai/new.go",
		"../command/ai/dir.go",
		"../command/ai/sessions.go",
		"../command/core/doctor.go",
	}

	seen := 0
	for _, file := range files {
		sources := translatedSources(t, file)
		if len(sources) == 0 {
			// A file that stopped calling Translate is either a file that lost its
			// translations or a path that no longer exists; both are worth failing.
			t.Errorf("%s asks for no translations", file)
			continue
		}
		for _, source := range sources {
			seen++
			if _, ok := builtinCatalog[EN][source]; !ok {
				t.Errorf("%s: no %s translation for %q", file, EN, source)
			}
		}
	}

	if seen == 0 {
		t.Fatal("no translated call sites found; the scan is not looking at the real files")
	}
}

// translatedSources returns the source strings a file passes to Translate.
//
// Only the direct string-literal form counts: a source built at runtime cannot be
// keyed in a catalog, and the AST is where that is visible.
func translatedSources(t *testing.T, file string) []string {
	t.Helper()

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, content, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var sources []string
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Translate" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("%s: Translate is called with something other than a string literal", file)
			return true
		}
		source, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Errorf("%s: unquote %s: %v", file, lit.Value, err)
			return true
		}
		sources = append(sources, source)
		return true
	})
	return sources
}
