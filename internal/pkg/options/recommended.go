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
	"fmt"

	"github.com/onexstack/onexstack/pkg/ptr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var configScheme = runtime.NewScheme()

// RecommendedOptions contains the recommended options for running an API server.
// If you add something to this list, it should be in a logical grouping.
// Each of them can be nil to leave the feature unconfigured on ApplyTo.
type RecommendedOptions struct {
	*genericoptions.RecommendedOptions

	// Custom options for onex project.
	ExternalAdmissionInitializers func(c *genericapiserver.RecommendedConfig) ([]admission.PluginInitializer, error)
}

func NewRecommendedOptions(prefix string, codec runtime.Codec) *RecommendedOptions {
	return &RecommendedOptions{RecommendedOptions: genericoptions.NewRecommendedOptions(prefix, codec)}
}

// ApplyTo adds RecommendedOptions to the server configuration.
// pluginInitializers can be empty, it is only need for additional initializers.
func (o *RecommendedOptions) ApplyTo(config *genericapiserver.RecommendedConfig) error {
	// NOTICE: The etcd health endpoints are wired separately in
	// controlplane/apiserver.BuildGenericConfig via Etcd.ApplyWithStorageFactoryTo,
	// so skip them here to avoid double-registering /healthz/etcd and /livez/etcd.
	etcdOptions := ptr.Clone(o.Etcd)
	etcdOptions.SkipHealthEndpoints = true
	if err := etcdOptions.ApplyTo(&config.Config); err != nil {
		return err
	}
	if err := o.EgressSelector.ApplyTo(&config.Config); err != nil {
		return err
	}
	if err := o.Traces.ApplyTo(config.Config.EgressSelector, &config.Config); err != nil {
		return err
	}
	if err := o.SecureServing.ApplyTo(&config.Config.SecureServing, &config.Config.LoopbackClientConfig); err != nil {
		return err
	}
	// Authentication is applied by controlplane/apiserver.BuildGenericConfig, once
	// the loopback client and its informers exist: kube-apiserver's authenticator
	// config needs both, and SecureServing (which it also needs) is applied above.
	if err := o.Authorization.ApplyTo(&config.Config.Authorization); err != nil {
		return err
	}
	if err := o.Audit.ApplyTo(&config.Config); err != nil {
		return err
	}
	if err := o.CoreAPI.ApplyTo(config); err != nil {
		return err
	}

	// Admission is applied by ApplyAdmission, once authentication and
	// authorization are in place: the generic initializer hands
	// config.Authorization.Authorizer to every plugin that wants one, and the
	// CEL admission policy plugins authorize reads of their param objects.
	return nil
}

// ApplyAdmission builds the admission chain and registers it on the config.
//
// It must run after authentication and authorization have been applied, since
// AdmissionOptions.ApplyTo captures config.Authorization.Authorizer into the
// generic plugin initializer. Feature gates are applied by
// controlplane/apiserver.BuildGenericConfig once the loopback client and its
// shared informer factory exist, so they are not touched here either.
func (o *RecommendedOptions) ApplyAdmission(config *genericapiserver.RecommendedConfig, kubeClient kubernetes.Interface, dynamicClient dynamic.Interface) error {
	if o.Admission == nil {
		return nil
	}
	if config.SharedInformerFactory == nil {
		return fmt.Errorf("admission depends on a Kubernetes core API shared informer, it cannot be nil")
	}

	var initializers []admission.PluginInitializer
	if o.ExtraAdmissionInitializers != nil {
		extra, err := o.ExtraAdmissionInitializers(config)
		if err != nil {
			return err
		}
		initializers = append(initializers, extra...)
	}
	if o.ExternalAdmissionInitializers != nil {
		external, err := o.ExternalAdmissionInitializers(config)
		if err != nil {
			return err
		}
		initializers = append(initializers, external...)
	}

	return o.Admission.ApplyTo(&config.Config, config.SharedInformerFactory, kubeClient, dynamicClient, o.FeatureGate,
		config.Config.EffectiveVersion, initializers...)
}
