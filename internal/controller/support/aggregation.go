// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package support

import (
	"sync"
	"time"
)

// PromptAggregationOptions is the process-wide configuration of the agent
// controller's prompt aggregation. It is set once by the controller-manager
// bootstrap before any controller starts, then read-only.
type PromptAggregationOptions struct {
	// Model names a Model object in the agent's namespace. Empty means only an
	// agent's modelRef can select the aggregation endpoint.
	Model string
	// MaxTokens is the target size of the merged prompt.
	MaxTokens int
	// MaxInputTokens caps one model call; a larger input is folded.
	MaxInputTokens int
	// Timeout bounds one aggregation call.
	Timeout time.Duration
	// ModelAPIKeyEncryptionKey opens a model credential sealed at rest. Empty
	// means only legacy plaintext credentials can be used.
	ModelAPIKeyEncryptionKey string
}

// DefaultPromptAggregationOptions returns the defaults applied when a field is
// zero.
func DefaultPromptAggregationOptions() PromptAggregationOptions {
	return PromptAggregationOptions{
		MaxTokens:      4000,
		MaxInputTokens: 60000,
		Timeout:        60 * time.Second,
	}
}

var (
	promptAggregationMu      sync.RWMutex
	promptAggregationOptions = DefaultPromptAggregationOptions()
)

// SetPromptAggregation installs the aggregation configuration. Zero fields
// fall back to the defaults. It is called once before controllers start.
func SetPromptAggregation(opts PromptAggregationOptions) {
	defaults := DefaultPromptAggregationOptions()
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaults.MaxTokens
	}
	if opts.MaxInputTokens <= 0 {
		opts.MaxInputTokens = defaults.MaxInputTokens
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaults.Timeout
	}

	promptAggregationMu.Lock()
	promptAggregationOptions = opts
	promptAggregationMu.Unlock()
}

// PromptAggregation returns the installed aggregation configuration.
func PromptAggregation() PromptAggregationOptions {
	promptAggregationMu.RLock()
	defer promptAggregationMu.RUnlock()
	return promptAggregationOptions
}
