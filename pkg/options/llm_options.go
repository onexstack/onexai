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

package options

import (
	"time"

	"github.com/spf13/pflag"
)

// LLMOptions holds configuration for the OpenAI-compatible LLM backend.
type LLMOptions struct {
	// BaseURL is the OpenAI-compatible API base URL.
	BaseURL string `json:"base-url" mapstructure:"base-url"`
	// APIKey is the API key for authentication.
	APIKey string `json:"api-key" mapstructure:"api-key"`
	// Model is the model name to use.
	Model string `json:"model" mapstructure:"model"`
	// MaxTokens caps how many tokens one completion may generate. Zero omits the
	// field, which leaves the ceiling to the model's own default — usually its
	// whole context window, and on DeepSeek that is unbounded output at the
	// most expensive rate there is (output bills at 4 CNY/M against 0.02 CNY/M for
	// cached input, so a runaway answer costs two hundred times what the same
	// tokens would as cached input).
	//
	// It is a deployment default, not the last word: `agent.max-output-tokens`
	// overrides it per caller surface, and a model call that passes
	// model.WithMaxTokens overrides both. See chatmodel's outputCeiling.
	MaxTokens int `json:"max-tokens" mapstructure:"max-tokens"`
	// AllowPrivateEndpoints permits a user-configured model endpoint to reach
	// loopback/private/link-local addresses. Default false: the runtime will
	// otherwise refuse that endpoint, because the URL is written by a user and
	// fetched by this process (SSRF). Set it only for a deployment whose
	// providers live on an internal network (e.g. a self-hosted Ollama).
	AllowPrivateEndpoints bool `json:"allow-private-endpoints" mapstructure:"allow-private-endpoints"`
	// TestTimeout bounds one connectivity test of a user-configured endpoint.
	TestTimeout time.Duration `json:"test-timeout" mapstructure:"test-timeout"`
}

// NewLLMOptions creates a LLMOptions with default values.
func NewLLMOptions() *LLMOptions {
	return &LLMOptions{
		Model:       "gpt-4o-mini",
		TestTimeout: 15 * time.Second,
	}
}

// Validate checks the LLMOptions fields.
func (o *LLMOptions) Validate() []error {
	return nil
}

// AddFlags adds LLM-related flags to the given FlagSet.
func (o *LLMOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.BaseURL, fullPrefix+".base-url", o.BaseURL, "OpenAI-compatible API base URL.")
	fs.StringVar(&o.APIKey, fullPrefix+".api-key", o.APIKey, "API key for LLM authentication.")
	fs.StringVar(&o.Model, fullPrefix+".model", o.Model, "LLM model name.")
	fs.IntVar(&o.MaxTokens, fullPrefix+".max-tokens", o.MaxTokens, "Cap on tokens one completion may generate; 0 leaves it to the model.")
	fs.BoolVar(&o.AllowPrivateEndpoints, fullPrefix+".allow-private-endpoints", o.AllowPrivateEndpoints,
		"Allow user-configured model endpoints to reach private/loopback addresses.")
	fs.DurationVar(&o.TestTimeout, fullPrefix+".test-timeout", o.TestTimeout,
		"Timeout for one connectivity test of a model endpoint.")
}
