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

package app

import (
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiserver/pkg/util/webhook"
	aggregatorapiserver "k8s.io/kube-aggregator/pkg/apiserver"

	"github.com/onexstack/onexai/cmd/oai-declserver/app/options"
	"github.com/onexstack/onexai/internal/controlplane"
	"github.com/onexstack/onexai/internal/controlplane/apiserver"
)

type Config struct {
	Options options.CompletedOptions

	Aggregator    *aggregatorapiserver.Config
	KubeAPIs      *controlplane.Config
	ApiExtensions *apiextensionsapiserver.Config

	ExtraConfig
}

type ExtraConfig struct{}

type completedConfig struct {
	Options options.CompletedOptions

	Aggregator    aggregatorapiserver.CompletedConfig
	KubeAPIs      controlplane.CompletedConfig
	ApiExtensions apiextensionsapiserver.CompletedConfig

	ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

func (c *Config) Complete() (CompletedConfig, error) {
	return CompletedConfig{&completedConfig{
		Options: c.Options,

		Aggregator:    c.Aggregator.Complete(),
		KubeAPIs:      c.KubeAPIs.Complete(),
		ApiExtensions: c.ApiExtensions.Complete(),

		ExtraConfig: c.ExtraConfig,
	}}, nil
}

// NewConfig creates all the resources for running kube-apiserver, but runs none of them.
func NewConfig(opts options.CompletedOptions) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	controlPlane, serviceResolver, err := CreateOneXAPIServerConfig(opts)
	if err != nil {
		return nil, err
	}
	c.KubeAPIs = controlPlane

	apiExtensions, err := apiserver.CreateAPIExtensionsConfig(
		controlPlane.Generic.Config,
		controlPlane.Extra.InternalVersionedInformers,
		nil,
		opts.CompletedOptions,
		3,
		serviceResolver,
		webhook.NewDefaultAuthenticationInfoResolverWrapper(
			controlPlane.Extra.ProxyTransport,
			controlPlane.Generic.EgressSelector,
			controlPlane.Generic.LoopbackClientConfig,
			controlPlane.Generic.TracerProvider,
		),
	)
	if err != nil {
		return nil, err
	}
	c.ApiExtensions = apiExtensions

	aggregator, err := createAggregatorConfig(
		controlPlane.Generic.Config,
		opts.CompletedOptions,
		controlPlane.Extra.InternalVersionedInformers,
		serviceResolver,
		controlPlane.Extra.ProxyTransport,
		controlPlane.Extra.PeerProxy,
		nil,
	)
	if err != nil {
		return nil, err
	}
	c.Aggregator = aggregator

	return c, nil
}
