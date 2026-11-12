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

package options

import (
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/kubernetes/pkg/kubeapiserver"

	controlplaneoptions "github.com/onexstack/onexai/internal/controlplane/apiserver/options"
)

// completedOptions is a private wrapper that enforces a call of Complete() before Run can be invoked.
type completedOptions struct {
	controlplaneoptions.CompletedOptions

	Extra
}

type CompletedOptions struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedOptions
}

// Complete set default ServerRunOptions.
// Should be called after oai-declserver flags parsed.
func (o *ServerRunOptions) Complete() (CompletedOptions, error) {
	if o == nil {
		return CompletedOptions{completedOptions: &completedOptions{}}, nil
	}

	controlplane, err := o.Options.Complete()
	if err != nil {
		return CompletedOptions{}, err
	}

	completed := completedOptions{
		CompletedOptions: controlplane,
		Extra:            o.Extra,
	}

	if completed.RecommendedOptions.Etcd != nil && completed.RecommendedOptions.Etcd.EnableWatchCache {
		sizes := kubeapiserver.DefaultWatchCacheSizes()
		// Ensure that overrides parse correctly.
		userSpecified, err := apiserveroptions.ParseWatchCacheSizes(completed.RecommendedOptions.Etcd.WatchCacheSizes)
		if err != nil {
			return CompletedOptions{}, err
		}
		for resource, size := range userSpecified {
			sizes[resource] = size
		}
		completed.RecommendedOptions.Etcd.WatchCacheSizes, err = apiserveroptions.WriteWatchCacheSizes(sizes)
		if err != nil {
			return CompletedOptions{}, err
		}
	}

	return CompletedOptions{&completed}, nil
}
