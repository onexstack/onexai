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

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/components/model"

	"github.com/onexstack/onexai/pkg/options"
)

// NewChatModel builds the chat model described by cfg.
//
// Two clients are available and the deployment picks between them with
// cfg.Client:
//
//   - the default, this package's OpenAI-compatible client, which carries
//     retries with model fallback, multimodal content and reasoning
//     propagation; or
//   - the vendor's own SDK (see NativeProviders), for a vendor whose protocol
//     the compatible endpoint does not fully expose.
//
// It returns nil (and no error) when no model is configured: an unconfigured
// optional endpoint is not a failure, it is a feature that is off.
func NewChatModel(ctx context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	if cfg.Model == "" {
		return nil, nil
	}
	provider := cfg.Provider
	if provider == "" {
		provider = InferProvider(cfg.Model)
	}
	cfg.Provider = provider

	if cfg.Client == ClientNative {
		native, err := NewVendorChatModel(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if native != nil {
			return native, nil
		}
		// A provider with no adapter falls through to the compatible client
		// rather than failing: the endpoint still exists, and refusing to build
		// a model over an unimplemented SDK choice would be a worse answer than
		// using the one that works for every provider.
		slog.Warn("chatmodel: no native adapter for this provider; using the OpenAI-compatible client",
			"provider", provider, "available", NativeProviders())
	}

	compatible, err := NewOpenAICompatibleModel(cfg)
	if err != nil {
		return nil, err
	}
	if compatible == nil {
		return nil, nil
	}
	return compatible, nil
}

// NewOpenAICompatibleModel builds this package's own client.
//
// It is spelled out separately from NewChatModel because callers that need the
// concrete type — to attach the retry policy with WithRetry, or to read usage
// back off it — must not receive an opaque interface.
func NewOpenAICompatibleModel(cfg options.ChatModelConfig) (*OpenAIChatModel, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		provider := cfg.Provider
		if provider == "" {
			provider = InferProvider(cfg.Model)
		}
		baseURL = DefaultBaseURL(provider)
		if baseURL == "" {
			return nil, fmt.Errorf("chatmodel: provider %q has no default base URL; set base-url explicitly", provider)
		}
	}
	m := NewOpenAIChatModel(baseURL, cfg.APIKey, cfg.Model)
	if m == nil {
		// Unreachable: baseURL is non-empty by construction above. Kept so the
		// nil-check in NewOpenAIChatModel stays honest for every caller.
		return nil, fmt.Errorf("chatmodel: empty base URL for model %q", cfg.Model)
	}
	return m, nil
}

// Inherit fills cfg's empty connection fields from base, so an auxiliary model
// (vision, reviewer) only has to name itself and inherits the deployment's
// endpoint and credentials. It returns the merged copy; the input is unchanged.
func Inherit(cfg, base options.ChatModelConfig) options.ChatModelConfig {
	if cfg.BaseURL == "" {
		cfg.BaseURL = base.BaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = base.APIKey
	}
	if cfg.Provider == "" {
		cfg.Provider = base.Provider
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = base.Timeout
	}
	if cfg.Client == "" {
		cfg.Client = base.Client
	}
	return cfg
}

// IsDeepSeekFamily reports whether a model belongs to the DeepSeek vendor,
// given either an explicit provider or a model name to infer one from.
//
// Membership is not an answer to "does it see images": v4 and the vl generation
// accept image input while older chat/reasoner names do not. DefaultFor owns
// that distinction.
func IsDeepSeekFamily(provider, modelName string) bool {
	if provider == "" {
		provider = InferProvider(modelName)
	}
	return strings.EqualFold(provider, ProviderDeepSeek)
}
