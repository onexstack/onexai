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

package chatmodel

import "strings"

// Provider identifiers as they appear in configuration.
const (
	ProviderOpenAI   = "openai"
	ProviderDeepSeek = "deepseek"
	ProviderClaude   = "claude"
	ProviderGemini   = "gemini"
	ProviderArk      = "ark"
	ProviderQwen     = "qwen"
	ProviderOllama   = "ollama"
	ProviderQianfan  = "qianfan"
)

// providerSpec is everything this package knows about one vendor, in one place.
//
// The four facts below used to live in four places — the constants here, a
// prefix switch in InferProvider, a base-URL map in the factory and a factory
// map in vendors.go — so adding a provider meant editing three files and
// remembering four sites. Forgetting one produced a provider that infers but
// has no endpoint, or that builds natively but not from configuration.
type providerSpec struct {
	// name is the canonical identifier used in configuration.
	name string
	// modelPrefixes are the model-name prefixes that identify this provider to
	// InferProvider, lowercased.
	modelPrefixes []string
	// defaultBaseURL is the vendor's public OpenAI-compatible endpoint. Empty
	// means there is none of note and the deployment must set base-url.
	defaultBaseURL string
	// native builds the vendor's own SDK adapter. Nil means this provider is
	// always served by the OpenAI-compatible client.
	native vendorFactory
}

// providerSpecs is the registry. Order matters only for prefix matching, where
// the first match wins.
var providerSpecs = []providerSpec{
	{
		name:           ProviderOpenAI,
		modelPrefixes:  []string{"gpt-", "o1-", "o3-", "o4-", "chatgpt-"},
		defaultBaseURL: "https://api.openai.com/v1",
		native:         newOpenAIVendorModel,
	},
	{
		name:           ProviderDeepSeek,
		modelPrefixes:  []string{"deepseek-"},
		defaultBaseURL: "https://api.deepseek.com/v1",
		native:         newDeepSeekVendorModel,
	},
	{
		name:           ProviderClaude,
		modelPrefixes:  []string{"claude-"},
		defaultBaseURL: "https://api.anthropic.com/v1",
		native:         newClaudeVendorModel,
	},
	{
		name:           ProviderGemini,
		modelPrefixes:  []string{"gemini-"},
		defaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		native:         newGeminiVendorModel,
	},
	{
		name:           ProviderArk,
		modelPrefixes:  []string{"doubao-", "seed-"},
		defaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		native:         newArkVendorModel,
	},
	{
		name:           ProviderQwen,
		modelPrefixes:  []string{"qwen-", "qwen2", "qwen3"},
		defaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		native:         newQwenVendorModel,
	},
	{
		name:          ProviderOllama,
		modelPrefixes: []string{"llama", "mistral", "phi"},
		native:        newOllamaVendorModel,
	},
	{
		name:   ProviderQianfan,
		native: newQianfanVendorModel,
	},
}

// specFor returns the spec of a canonical provider name, and whether it is
// known.
func specFor(name string) (providerSpec, bool) {
	for _, spec := range providerSpecs {
		if spec.name == name {
			return spec, true
		}
	}
	return providerSpec{}, false
}

// DefaultBaseURL returns a provider's OpenAI-compatible endpoint, or empty when
// it has none and the deployment must supply one.
func DefaultBaseURL(provider string) string {
	spec, ok := specFor(provider)
	if !ok {
		return ""
	}
	return spec.defaultBaseURL
}

// defaultContextWindow is returned for a model this package does not recognise.
// It is the conservative figure: assuming more room than a model has produces a
// request the backend rejects, while assuming less only costs a summarization
// call.
const defaultContextWindow = 128000

// InferProvider infers the provider from a model name, for configurations that
// name only the model. An unrecognised name falls back to OpenAI, which every
// provider in this service speaks.
//
// The match is a prefix match on the lowercased name, so it stays correct when
// a vendor appends a date or a size suffix ("claude-sonnet-5-5",
// "deepseek-v4-flash").
func InferProvider(modelName string) string {
	lower := strings.ToLower(modelName)
	for _, spec := range providerSpecs {
		if hasAnyPrefix(lower, spec.modelPrefixes...) {
			return spec.name
		}
	}
	// Unrecognised names get the OpenAI-compatible client, which every provider
	// in this service speaks. The registry itself must stay usable for that
	// fallback, so it is looked up rather than assumed.
	if _, ok := specFor(ProviderOpenAI); ok {
		return ProviderOpenAI
	}
	return ProviderOpenAI
}

// hasAnyPrefix reports whether s starts with any of the given prefixes. An
// empty list matches nothing, which is how a provider with no model prefixes of
// its own stays out of inference.
func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// contextWindowSpec is one model family's prompt budget.
//
// It is a separate table from providerSpec on purpose: a window is a property
// of the model, not of the vendor — Gemini Pro and Gemini Flash share a
// provider but not a budget — so folding it into the vendor registry would
// model the wrong axis.
type contextWindowSpec struct {
	// needles are substrings that identify the family in a lowercased model
	// name. Any one match is enough.
	needles []string
	window  int
}

// contextWindowSpecs is ordered most specific first, because the first match
// wins: "gemini-2.5-pro" has to be tested before the plain "gemini-" family it
// also matches.
var contextWindowSpecs = []contextWindowSpec{
	{needles: []string{"gemini-1.5-pro", "gemini-2.5-pro"}, window: 2000000},
	{needles: []string{"gemini-", "claude-"}, window: 1000000},
	{needles: []string{"deepseek-v4", "deepseek-r1"}, window: 1000000},
	{needles: []string{"gpt-", "o1", "o3", "o4"}, window: 128000},
}

// ContextWindow returns the model's context window in tokens, used to set
// summarization and pruning thresholds.
//
// An unknown model gets the conservative default rather than a guess: this
// number decides how much history is kept verbatim, and overestimating it is
// what lets a single oversized turn reach the backend and fail.
func ContextWindow(modelName string) int {
	m := strings.ToLower(modelName)
	for _, spec := range contextWindowSpecs {
		if hasAnyNeedle(m, spec.needles...) {
			return spec.window
		}
	}
	return defaultContextWindow
}

// hasAnyNeedle reports whether s contains any of the given substrings. An empty
// list matches nothing, which is what keeps an unfilled table entry out of the
// match.
func hasAnyNeedle(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
