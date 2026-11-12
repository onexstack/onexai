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
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	genericconfigv1beta1 "github.com/onexstack/onexai/pkg/config/v1beta1"
	"github.com/onexstack/onexai/pkg/readmodel"
)

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

func SetDefaults_OneXControllerManagerConfiguration(obj *OneXControllerManagerConfiguration) {
	genericconfigv1beta1.RecommendedDefaultGenericControllerManagerConfiguration(&obj.Generic)
	genericconfigv1beta1.RecommendedDefaultGarbageCollectorControllerConfiguration(&obj.GarbageCollectorController)
	genericconfigv1beta1.RecommendedDefaultCoreDBConfiguration(&obj.CoreDB)
	RecommendedDefaultChainControllerConfiguration(&obj.ChainController)
	RecommendedDefaultPromptAggregationConfiguration(&obj.PromptAggregation)
	RecommendedDefaultMirrorConfiguration(&obj.Mirror)
}

// RecommendedDefaultMirrorConfiguration fills the projection defaults. Keep
// these values in sync with the internal config's function.
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

func RecommendedDefaultChainControllerConfiguration(obj *ChainControllerConfiguration) {
	if obj.Image == "" {
		obj.Image = "ghcr.io/onexstack/onex-toyblc-amd64:v0.1.0"
	}
}

// RecommendedDefaultPromptAggregationConfiguration fills the aggregation
// defaults. Keep these values in sync with the internal config's function.
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
