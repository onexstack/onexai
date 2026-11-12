// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	genericconfigv1beta1 "github.com/onexstack/onexai/pkg/config/v1beta1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OneXControllerManagerConfiguration contains elements describing onex-controller manager.
type OneXControllerManagerConfiguration struct {
	metav1.TypeMeta `json:",inline"`

	// FeatureGates is a map of feature names to bools that enable or disable alpha/experimental features.
	// FeatureGates map[string]bool `json:"featureGates,omitempty"`

	// CoreDB defines the database the resource mirror writes to.
	CoreDB genericconfigv1beta1.CoreDBConfiguration `json:"coredb,omitempty"`

	// Mirror controls the projection of control-plane resources into PostgreSQL.
	Mirror MirrorConfiguration `json:"mirror,omitempty"`

	// Generic holds configuration for a generic controller-manager
	Generic genericconfigv1beta1.GenericControllerManagerConfiguration `json:"generic,omitempty"`

	// GarbageCollectorControllerConfiguration holds configuration for
	// GarbageCollectorController related features.
	GarbageCollectorController genericconfigv1beta1.GarbageCollectorControllerConfiguration `json:"garbageCollectorController,omitempty"`

	// ChainControllerConfiguration holds configuration for ChainController related features.
	ChainController ChainControllerConfiguration `json:"chainController,omitempty"`

	// PromptAggregation controls how the agent controller derives an agent's
	// aggregated prompt.
	PromptAggregation PromptAggregationConfiguration `json:"promptAggregation,omitempty"`
}

type ChainControllerConfiguration struct {
	// Image specify the blockchain node image.
	Image string `json:"image,omitempty"`
}

// MirrorConfiguration controls the projection of control-plane resources into
// PostgreSQL. Disabled by default; see the internal type for the rationale.
type MirrorConfiguration struct {
	// Enabled turns the projection on.
	Enabled bool `json:"enabled,omitempty"`
	// Schema is the PostgreSQL schema holding the projection tables.
	Schema string `json:"schema,omitempty"`
	// Kinds narrows the projection; empty means every known kind.
	Kinds []string `json:"kinds,omitempty"`
	// BatchSize and FlushInterval bound one write.
	BatchSize     int32           `json:"batchSize,omitempty"`
	FlushInterval metav1.Duration `json:"flushInterval,omitempty"`
	// SweepInterval is how often the projection is verified against the control
	// plane, and the refresh period of status-only changes.
	SweepInterval metav1.Duration `json:"sweepInterval,omitempty"`
}

// PromptAggregationConfiguration configures the derived aggregated prompt.
type PromptAggregationConfiguration struct {
	// Model names a Model object in the agent's namespace. Empty means only an
	// agent's modelRef can select the aggregation endpoint.
	Model string `json:"model,omitempty"`
	// MaxTokens is the target size of the merged prompt. Zero means the
	// default (4000).
	MaxTokens int32 `json:"maxTokens,omitempty"`
	// MaxInputTokens caps one model call; a larger input is folded. Zero means
	// the default (60000).
	MaxInputTokens int32 `json:"maxInputTokens,omitempty"`
	// Timeout bounds one aggregation call. Zero means the default (60s).
	Timeout metav1.Duration `json:"timeout,omitempty"`
	// ModelAPIKeyEncryptionKey opens a Model row whose API key is stored sealed.
	// It must match the key the agent-apiserver sealed it with. Empty leaves
	// legacy plaintext credentials usable; a sealed one is reported as an
	// actionable condition instead of being sent upstream as a bearer token.
	ModelAPIKeyEncryptionKey string `json:"modelAPIKeyEncryptionKey,omitempty"`
}
