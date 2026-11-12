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

package chatmodel

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexai/pkg/options"
)

func TestInferProvider(t *testing.T) {
	cases := map[string]string{
		"gpt-4o-mini":        ProviderOpenAI,
		"o3-mini":            ProviderOpenAI,
		"chatgpt-4o-latest":  ProviderOpenAI,
		"claude-sonnet-5-5":  ProviderClaude,
		"gemini-2.5-pro":     ProviderGemini,
		"deepseek-v4-flash":  ProviderDeepSeek,
		"doubao-pro-32k":     ProviderArk,
		"seed-1.6":           ProviderArk,
		"qwen-max":           ProviderQwen,
		"llama-3.3-70b":      ProviderOllama,
		"some-unknown-model": ProviderOpenAI, // openai-compatible is the safe default
		"":                   ProviderOpenAI,
		"  GPT-4O  ":         ProviderOpenAI, // whitespace and case are normalised
	}
	for model, want := range cases {
		if got := InferProvider(model); got != want {
			t.Errorf("InferProvider(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestProviderRegistryIsConsistent pins the property that made the registry
// worth having: a fact about a provider is stated once, so it cannot be stated
// in three places and forgotten in one.
func TestProviderRegistryIsConsistent(t *testing.T) {
	if len(providerSpecs) == 0 {
		t.Fatal("the provider registry is empty")
	}
	seen := map[string]bool{}
	for _, spec := range providerSpecs {
		if seen[spec.name] {
			t.Errorf("provider %q is registered twice", spec.name)
		}
		seen[spec.name] = true

		t.Run(spec.name, func(t *testing.T) {
			// A name is reachable only if something knows how to build it.
			if spec.native == nil && spec.defaultBaseURL == "" {
				t.Errorf("provider %q has neither a native adapter nor a default endpoint, "+
					"so nothing can construct it", spec.name)
			}
			// Every inferable provider must resolve back to itself.
			for _, prefix := range spec.modelPrefixes {
				if got := InferProvider(prefix + "x"); got != spec.name {
					t.Errorf("model %q infers %q, want %q", prefix+"x", got, spec.name)
				}
			}
			if spec.defaultBaseURL != "" && DefaultBaseURL(spec.name) != spec.defaultBaseURL {
				t.Errorf("DefaultBaseURL(%q) does not agree with the registry", spec.name)
			}
		})
	}

	// Every adapter the registry advertises must actually build.
	for _, name := range NativeProviders() {
		spec, ok := specFor(name)
		if !ok || spec.native == nil {
			t.Errorf("%q is advertised as having a native adapter but does not", name)
		}
	}

	// The prefix table must not shadow a later entry: the first match wins, so
	// an earlier provider claiming a later one's prefix would make that provider
	// unreachable by inference.
	for i, a := range providerSpecs {
		for _, b := range providerSpecs[i+1:] {
			for _, pa := range a.modelPrefixes {
				for _, pb := range b.modelPrefixes {
					if strings.HasPrefix(pb, pa) {
						t.Errorf("provider %q claims prefix %q, which also matches %q's %q: "+
							"%q can never be inferred", a.name, pa, b.name, pb, b.name)
					}
				}
			}
		}
	}
}

func TestContextWindow(t *testing.T) {
	cases := map[string]int{
		"gemini-1.5-pro":        2000000,
		"gemini-2.5-pro":        2000000,
		"gemini-2.5-flash":      1000000,
		"claude-sonnet-5-5":     1000000,
		"deepseek-v4-flash":     1000000,
		"deepseek-r1":           1000000,
		"gpt-4o-mini":           128000,
		"o1-preview":            128000,
		"o3-mini":               128000,
		"o4-mini":               128000,
		"  Claude-Sonnet-5-5  ": 1000000,
		"totally-unknown":       defaultContextWindow,
		"":                      defaultContextWindow,
	}
	for model, want := range cases {
		if got := ContextWindow(model); got != want {
			t.Errorf("ContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestNewChatModel(t *testing.T) {
	t.Run("no model configured is not an error", func(t *testing.T) {
		m, err := NewChatModel(context.Background(), options.ChatModelConfig{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m != nil {
			t.Error("expected a nil model when none is configured")
		}
	})

	t.Run("the default client is this package's OpenAI-compatible one", func(t *testing.T) {
		m, err := NewChatModel(context.Background(), options.ChatModelConfig{Model: "deepseek-v4-flash", APIKey: "k"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		compatible, ok := m.(*OpenAIChatModel)
		if !ok {
			t.Fatalf("expected the OpenAI-compatible client by default, got %T", m)
		}
		if compatible.baseURL != DefaultBaseURL(ProviderDeepSeek) || compatible.model != "deepseek-v4-flash" {
			t.Errorf("built with baseURL=%q model=%q", compatible.baseURL, compatible.model)
		}
	})

	t.Run("an unknown provider with no base URL is a hard error", func(t *testing.T) {
		_, err := NewChatModel(context.Background(), options.ChatModelConfig{Provider: "typo-provider", Model: "x"})
		if err == nil {
			t.Fatal("expected an error rather than a silent fallback to openai")
		}
	})

	t.Run("an unknown provider with a base URL is accepted", func(t *testing.T) {
		if _, err := NewChatModel(context.Background(), options.ChatModelConfig{
			Provider: "self-hosted", Model: "x", BaseURL: "https://vllm.internal/v1",
		}); err != nil {
			t.Fatalf("a self-hosted OpenAI-compatible endpoint must be usable: %v", err)
		}
	})
}

func TestNewOpenAICompatibleModel(t *testing.T) {
	t.Run("an explicit base URL wins over the provider default", func(t *testing.T) {
		m, err := NewOpenAICompatibleModel(options.ChatModelConfig{
			Model: "gpt-4o", BaseURL: "https://gateway.internal/v1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.baseURL != "https://gateway.internal/v1" {
			t.Errorf("baseURL = %q, want the configured one", m.baseURL)
		}
	})
}

// TestNewChatModel_NativeClient pins that asking for a vendor SDK gets one, and
// that asking for one this package does not have falls back rather than failing.
func TestNewChatModel_NativeClient(t *testing.T) {
	t.Run("every advertised provider has an adapter that builds", func(t *testing.T) {
		for _, provider := range NativeProviders() {
			m, err := NewChatModel(context.Background(), options.ChatModelConfig{
				Provider: provider, Model: "some-model", APIKey: "k",
				BaseURL: "https://example.invalid/v1",
				Client:  ClientNative,
			})
			if err != nil {
				t.Errorf("provider %s: %v", provider, err)
				continue
			}
			if m == nil {
				t.Errorf("provider %s returned no model", provider)
				continue
			}
			// The point of the native client is that it is not this package's.
			if _, isCompatible := m.(*OpenAIChatModel); isCompatible {
				t.Errorf("provider %s fell back to the compatible client although it has an adapter", provider)
			}
		}
	})

	t.Run("a provider without an adapter falls back to the compatible client", func(t *testing.T) {
		m, err := NewChatModel(context.Background(), options.ChatModelConfig{
			Provider: "self-hosted", Model: "x", BaseURL: "https://vllm.internal/v1",
			Client: ClientNative,
		})
		if err != nil {
			t.Fatalf("an unadapted provider must still build: %v", err)
		}
		if _, ok := m.(*OpenAIChatModel); !ok {
			t.Errorf("expected the compatible client as the fallback, got %T", m)
		}
	})
}

func TestInherit(t *testing.T) {
	base := options.ChatModelConfig{
		Provider: ProviderDeepSeek, BaseURL: "https://api.deepseek.com/v1", APIKey: "base-key",
	}
	got := Inherit(options.ChatModelConfig{Model: "qwen-vl-max"}, base)

	if got.BaseURL != base.BaseURL || got.APIKey != base.APIKey {
		t.Errorf("connection fields were not inherited: %+v", got)
	}
	if got.Provider != base.Provider {
		t.Errorf("provider was not inherited: %q", got.Provider)
	}
	if got.Model != "qwen-vl-max" {
		t.Errorf("the model must not be overwritten: %q", got.Model)
	}

	// Explicit values must survive.
	own := Inherit(options.ChatModelConfig{
		Model: "qwen-vl-max", BaseURL: "https://own/v1", APIKey: "own-key", Provider: ProviderQwen,
	}, base)
	if own.BaseURL != "https://own/v1" || own.APIKey != "own-key" || own.Provider != ProviderQwen {
		t.Errorf("explicit values were overwritten: %+v", own)
	}
}

func TestIsDeepSeekFamily(t *testing.T) {
	if !IsDeepSeekFamily("", "deepseek-v4-flash") {
		t.Error("deepseek must be recognised by model name")
	}
	if !IsDeepSeekFamily("deepseek", "anything") {
		t.Error("deepseek must be recognised by explicit provider")
	}
	if IsDeepSeekFamily("", "gpt-4o") {
		t.Error("openai must not be treated as deepseek")
	}
}
