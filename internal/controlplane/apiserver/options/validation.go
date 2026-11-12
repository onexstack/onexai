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

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiserver/pkg/features"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	aggregatorscheme "k8s.io/kube-aggregator/pkg/apiserver/scheme"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
)

// Validate checks ServerRunOptions and return a slice of found errs.
func (o *Options) Validate() []error {
	errs := []error{}
	errs = append(errs, o.GenericServerRunOptions.Validate()...)
	errs = append(errs, o.RecommendedOptions.Validate()...)
	errs = append(errs, o.Features.Validate()...)
	errs = append(errs, o.Metrics.Validate()...)
	errs = append(errs, o.Traces.Validate()...)
	errs = append(errs, o.APIEnablement.Validate(legacyscheme.Scheme, apiextensionsapiserver.Scheme, aggregatorscheme.Scheme)...)
	// errs = append(errs, o.CloudOptions.Validate()...)
	// The upstream validators own every rule here: mutually exclusive flags, a
	// malformed AuthenticationConfiguration/AuthorizationConfiguration, bad
	// webhook kubeconfigs and unsupported authorizer modes.
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)
	errs = append(errs, validateUnknownVersionInteroperabilityProxyFlags(o)...)

	return errs
}

func validateUnknownVersionInteroperabilityProxyFlags(options *Options) []error {
	err := []error{}
	if !utilfeature.DefaultFeatureGate.Enabled(features.UnknownVersionInteroperabilityProxy) {
		if options.PeerCAFile != "" {
			err = append(err, fmt.Errorf("--peer-ca-file requires UnknownVersionInteroperabilityProxy feature to be turned on"))
		}
		if options.PeerAdvertiseAddress.PeerAdvertiseIP != "" {
			err = append(err, fmt.Errorf("--peer-advertise-ip requires UnknownVersionInteroperabilityProxy feature to be turned on"))
		}
		if options.PeerAdvertiseAddress.PeerAdvertisePort != "" {
			err = append(err, fmt.Errorf("--peer-advertise-port requires UnknownVersionInteroperabilityProxy feature to be turned on"))
		}
	}
	return err
}
