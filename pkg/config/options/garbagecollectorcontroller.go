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
	"github.com/spf13/pflag"

	genericconfig "github.com/onexstack/onexai/pkg/config"
)

// GarbageCollectorControllerOptions holds the GarbageCollectorController options.
type GarbageCollectorControllerOptions struct {
	*genericconfig.GarbageCollectorControllerConfiguration
}

func NewGarbageCollectorControllerOptions(cfg *genericconfig.GarbageCollectorControllerConfiguration) *GarbageCollectorControllerOptions {
	return &GarbageCollectorControllerOptions{
		GarbageCollectorControllerConfiguration: cfg,
	}
}

// AddFlags adds flags related to GarbageCollectorController for controller manager to the specified FlagSet.
func (o *GarbageCollectorControllerOptions) AddFlags(fs *pflag.FlagSet) {
	if o == nil {
		return
	}

	fs.Int32Var(&o.ConcurrentGCSyncs, "concurrent-gc-syncs", o.ConcurrentGCSyncs, "The number of garbage collector workers that are allowed to sync concurrently."+
		"This parameter is ignored if a config file is specified by --config.")
	fs.BoolVar(&o.EnableGarbageCollector, "enable-garbage-collector", o.EnableGarbageCollector, "Enables the generic garbage collector. MUST be synced with "+
		"the corresponding flag of the kube-apiserver. This parameter is ignored if a config file is specified by --config.")
}

// ApplyTo fills up GarbageCollectorController config with options.
func (o *GarbageCollectorControllerOptions) ApplyTo(cfg *genericconfig.GarbageCollectorControllerConfiguration) error {
	if o == nil || cfg == nil {
		return nil
	}

	*cfg = *o.GarbageCollectorControllerConfiguration
	return nil
}

// Validate checks validation of GarbageCollectorController.
func (o *GarbageCollectorControllerOptions) Validate() []error {
	if o == nil {
		return nil
	}

	errs := []error{}
	return errs
}
