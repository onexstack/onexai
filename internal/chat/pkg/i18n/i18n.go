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

// Package i18n translates the strings this service shows to users.
//
// The source language is Chinese and the source strings are the keys: a message
// with no translation is shown exactly as it is written in the code. That choice
// is what makes this package additive — every string in the service keeps
// working untouched, and a string becomes translatable when someone adds an
// entry for it, rather than when someone rewrites it.
//
// It is deliberately not a global: the language is a value handed to T, so a
// deployment can serve one language while a test proves another, and there is no
// process-wide setting to reset between tests.
package i18n

import (
	"fmt"
	"strings"
)

// Language is a supported language tag.
type Language string

const (
	// ZH is Simplified Chinese, the source language of this repository.
	ZH Language = "zh"
	// ZHTW is Traditional Chinese.
	ZHTW Language = "zh-TW"
	// EN is English.
	EN Language = "en"
)

// Default is the language used when none is configured. It is the source
// language, so the default is also the configuration that changes nothing.
const Default = ZH

// Languages lists the supported tags in the order they should be offered.
var Languages = []Language{ZH, ZHTW, EN}

// ParseLanguage reads a configured language tag.
//
// It accepts the tags in Languages, case- and space-insensitively, and treats an
// empty value as the default. Anything else is reported as unknown rather than
// silently falling back: a deployment that asked for a language this service
// cannot speak should hear about it at startup, not discover it from a user who
// cannot read the replies.
func ParseLanguage(s string) (Language, bool) {
	trimmed := normalizeTag(strings.TrimSpace(s))
	if trimmed == "" {
		return Default, true
	}
	// Language tags are conventionally lower-case with an upper-case region, and
	// a deployment that writes "zh-tw" or "zh_tw" means the same thing as "zh-TW".
	for _, lang := range Languages {
		if normalizeTag(string(lang)) == trimmed {
			return lang, true
		}
	}
	return "", false
}

// normalizeTag lower-cases a tag and unifies its separator ("ZH_TW" → "zh-tw").
func normalizeTag(tag string) string { return strings.ToLower(strings.ReplaceAll(tag, "_", "-")) }

// SupportedTags renders the known tags, for an error that has to say what was
// expected instead.
func SupportedTags() string {
	tags := make([]string, 0, len(Languages))
	for _, lang := range Languages {
		tags = append(tags, string(lang))
	}
	return strings.Join(tags, ", ")
}

// Catalog maps a source string to its translation, per language.
type Catalog map[Language]map[string]string

// Translator resolves a source string to the best translation it has.
type Translator struct {
	// catalog holds the translations. A language with no entry at all is legal:
	// it means every string is shown as written.
	catalog Catalog
	// fallback is tried in order after the requested language. It ends the chain
	// at the source language, which needs no catalog because the source strings
	// are themselves Chinese.
	fallback []Language
}

// New builds a Translator over catalog, falling back to the source language.
func New(catalog Catalog) *Translator {
	return &Translator{catalog: catalog, fallback: []Language{ZH}}
}

// T renders source in lang, substituting args when any are given.
//
// The chain is: the requested language, each fallback in order, then the source
// string itself. A nil Translator renders the source, so a caller that has no
// catalog configured still produces a correct — if untranslated — message
// instead of an empty one.
func (t *Translator) T(lang Language, source string, args ...any) string {
	if t != nil {
		for _, candidate := range append([]Language{lang}, t.fallback...) {
			if text, ok := t.catalog.lookup(candidate, source); ok {
				return format(text, args)
			}
		}
	}
	return format(source, args)
}

// T renders source using the catalog this package was built with.
func T(lang Language, source string, args ...any) string {
	return DefaultTranslator.T(lang, source, args...)
}

// DefaultTranslator is the catalog of this service's own strings.
var DefaultTranslator = New(builtinCatalog)

// lookup finds source's translation for lang.
func (c Catalog) lookup(lang Language, source string) (string, bool) {
	translations, ok := c[lang]
	if !ok {
		return "", false
	}
	text, ok := translations[source]
	if !ok || text == "" {
		return "", false
	}
	return text, true
}

// format applies args. A source with no args is returned untouched, so an
// untranslated string containing a stray percent sign is not mangled.
func format(text string, args []any) string {
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}
