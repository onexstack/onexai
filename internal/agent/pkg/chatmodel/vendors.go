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
	"sort"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino-ext/components/model/gemini"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qianfan"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
	"google.golang.org/genai"

	"github.com/onexstack/onexai/pkg/options"
)

// Client implementation choices for ChatModelConfig.Client.
const (
	// ClientOpenAICompatible is the default: this package's own client over the
	// OpenAI wire format, with retries, model fallback, multimodal content and
	// reasoning propagation.
	ClientOpenAICompatible = "openai-compatible"
	// ClientNative selects the provider's own SDK. It is an explicit choice
	// rather than something inferred from the model name: the two clients differ
	// in what they support — the native one speaks its vendor's full protocol,
	// this package's one carries retry and fallback — and a deployment should
	// pick rather than be switched by a naming convention.
	ClientNative = "native"
)

// vendorFactory builds a provider's native adapter.
type vendorFactory func(ctx context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error)

// NativeProviders lists the providers that have an SDK adapter, sorted. A
// startup log prints it so an operator can see which names are usable with
// client: native.
func NativeProviders() []string {
	out := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		if spec.native != nil {
			out = append(out, spec.name)
		}
	}
	sort.Strings(out)
	return out
}

// NewVendorChatModel builds the provider's SDK adapter.
//
// It returns (nil, nil) when the provider has no adapter, which the caller
// reads as "fall back to the OpenAI-compatible client" rather than as an error:
// a provider without an SDK is served by the shared client, which is what every
// provider did before this file existed.
func NewVendorChatModel(ctx context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	spec, ok := specFor(cfg.Provider)
	if !ok || spec.native == nil {
		return nil, nil
	}
	m, err := spec.native(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("chatmodel: %s native adapter: %w", cfg.Provider, err)
	}
	return m, nil
}

// The adapters below translate this service's configuration into each SDK's own
// config type. They set what a deployment configures and leave the rest at the
// SDK's defaults: an adapter that invented values for unset fields would make
// two deployments of the same image behave differently for reasons no operator
// wrote down.

func newOpenAIVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: cfg.Timeout,
	})
}

func newDeepSeekVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return deepseek.NewChatModel(context.Background(), &deepseek.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: cfg.Timeout,
	})
}

func newClaudeVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	claudeCfg := &claude.Config{
		APIKey: cfg.APIKey,
		Model:  cfg.Model,
	}
	// BaseURL is a pointer because the SDK distinguishes "unset, use the
	// vendor's endpoint" from "set to empty".
	if cfg.BaseURL != "" {
		base := cfg.BaseURL
		claudeCfg.BaseURL = &base
	}
	return claude.NewChatModel(context.Background(), claudeCfg)
}

func newGeminiVendorModel(ctx context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	clientCfg := &genai.ClientConfig{APIKey: cfg.APIKey}
	if cfg.BaseURL != "" {
		clientCfg.HTTPOptions = genai.HTTPOptions{BaseURL: cfg.BaseURL}
	}
	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("genai client: %w", err)
	}
	return gemini.NewChatModel(ctx, &gemini.Config{Client: client, Model: cfg.Model})
}

func newArkVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return ark.NewChatModel(context.Background(), &ark.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	})
}

func newQwenVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return qwen.NewChatModel(context.Background(), &qwen.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: cfg.Timeout,
	})
}

func newOllamaVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return ollama.NewChatModel(context.Background(), &ollama.ChatModelConfig{
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: cfg.Timeout,
	})
}

// newQianfanVendorModel builds the Qianfan adapter.
//
// Its configuration type carries no endpoint or credential: the SDK reads both
// from its own environment (QIANFAN_AK / QIANFAN_SK). That is a property of the
// adapter, not an omission here — a deployment using this provider must supply
// them the way the SDK expects, and a base-url or api-key in this service's
// configuration is ignored rather than silently passed nowhere.
func newQianfanVendorModel(_ context.Context, cfg options.ChatModelConfig) (model.ToolCallingChatModel, error) {
	return qianfan.NewChatModel(context.Background(), &qianfan.ChatModelConfig{Model: cfg.Model})
}
