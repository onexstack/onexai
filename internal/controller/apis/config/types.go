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

package config

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	genericconfig "github.com/onexstack/onexai/pkg/config"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OneXControllerManagerConfiguration contains elements describing onex-controller manager.
type OneXControllerManagerConfiguration struct {
	metav1.TypeMeta

	// FeatureGates is a map of feature names to bools that enable or disable alpha/experimental features.
	// FeatureGates map[string]bool

	// CoreDB defines the database the resource mirror writes to. It is the same
	// block the runtime services read, so an operator keeps one spelling of
	// "where the database is" across every binary.
	CoreDB genericconfig.CoreDBConfiguration

	// Mirror controls the projection of control-plane resources into PostgreSQL.
	Mirror MirrorConfiguration

	// Generic holds configuration for a generic controller-manager
	Generic genericconfig.GenericControllerManagerConfiguration

	// GarbageCollectorControllerConfiguration holds configuration for
	// GarbageCollectorController related features.
	GarbageCollectorController genericconfig.GarbageCollectorControllerConfiguration

	// ChainControllerConfiguration holds configuration for ChainController related features.
	ChainController ChainControllerConfiguration

	// PromptAggregation controls how the agent controller derives an agent's
	// aggregated prompt.
	PromptAggregation PromptAggregationConfiguration
}

type ChainControllerConfiguration struct {
	// Image specify the blockchain node image.
	Image string
}

// MirrorConfiguration controls the projection of control-plane resources into
// PostgreSQL.
//
// Disabled by default: with it off the controller manager behaves exactly as it
// did before the projection existed, which is what makes switching it on a
// deliberate act rather than a side effect of an upgrade.
type MirrorConfiguration struct {
	// Enabled turns the projection on.
	Enabled bool
	// Schema is the PostgreSQL schema holding the projection tables.
	Schema string
	// Kinds narrows the projection to the named kinds. Empty means every kind the
	// mirror knows about.
	Kinds []string
	// BatchSize and FlushInterval bound one write. A reconciler is per-object;
	// without coalescing the first list of a large kind would be one statement per
	// object.
	BatchSize     int32
	FlushInterval metav1.Duration
	// SweepInterval is how often the projection is verified against the control
	// plane. It is also the refresh period of status-only changes, which are not
	// written by the event path.
	SweepInterval metav1.Duration
}

// RecommendedDefaultMirrorConfiguration fills the projection defaults. The same
// values are exposed by the versioned defaulting function; this one covers the
// flags-only path, where no config file is decoded.
func RecommendedDefaultMirrorConfiguration(obj *MirrorConfiguration) {
	if obj.Schema == "" {
		obj.Schema = readmodel.DefaultSchema
	}
	if obj.BatchSize == 0 {
		obj.BatchSize = 500
	}
	if obj.FlushInterval.Duration == 0 {
		obj.FlushInterval.Duration = 50 * time.Millisecond
	}
	if obj.SweepInterval.Duration == 0 {
		obj.SweepInterval.Duration = 5 * time.Minute
	}
}

// PromptAggregationConfiguration configures the derived aggregated prompt.
type PromptAggregationConfiguration struct {
	// Model names a Model object in the agent's namespace. Empty means only an
	// agent's modelRef can select the aggregation endpoint.
	Model string
	// MaxTokens is the target size of the merged prompt. Zero means the
	// default (4000).
	MaxTokens int32
	// MaxInputTokens caps one model call; a larger input is folded. Zero means
	// the default (60000).
	MaxInputTokens int32
	// Timeout bounds one aggregation call. Zero means the default (60s).
	Timeout metav1.Duration
	// ModelAPIKeyEncryptionKey opens a Model row whose API key is stored sealed.
	// It must match the key the agent-apiserver sealed it with. Empty leaves
	// legacy plaintext credentials usable; a sealed one is reported as an
	// actionable condition instead of being sent upstream as a bearer token.
	ModelAPIKeyEncryptionKey string
}

// RecommendedDefaultPromptAggregationConfiguration fills the aggregation
// defaults. The same values are exposed by the versioned defaulting function;
// this one covers the flags-only path, where no config file is decoded.
func RecommendedDefaultPromptAggregationConfiguration(obj *PromptAggregationConfiguration) {
	if obj.MaxTokens == 0 {
		obj.MaxTokens = 4000
	}
	if obj.MaxInputTokens == 0 {
		obj.MaxInputTokens = 60000
	}
	if obj.Timeout.Duration == 0 {
		obj.Timeout.Duration = 60 * time.Second
	}
}
