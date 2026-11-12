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
	"fmt"

	"github.com/spf13/pflag"

	ctrlmgrconfig "github.com/onexstack/onexai/internal/controller/apis/config"
)

// MirrorOptions holds the resource-projection settings.
type MirrorOptions struct {
	*ctrlmgrconfig.MirrorConfiguration
}

// NewMirrorOptions wraps a component-configuration block.
func NewMirrorOptions(cfg *ctrlmgrconfig.MirrorConfiguration) *MirrorOptions {
	return &MirrorOptions{MirrorConfiguration: cfg}
}

// AddFlags adds flags related to the resource projection.
func (o *MirrorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Enabled, "mirror.enabled", o.Enabled, ""+
		"Project control-plane resources into PostgreSQL. Disabled by default.")
	fs.StringVar(&o.Schema, "mirror.schema", o.Schema, ""+
		"PostgreSQL schema holding the projection tables.")
	fs.StringSliceVar(&o.Kinds, "mirror.kinds", o.Kinds, ""+
		"Kinds to project; empty means every known kind.")
	fs.Int32Var(&o.BatchSize, "mirror.batch-size", o.BatchSize, ""+
		"Maximum rows in one projection statement.")
	fs.DurationVar(&o.FlushInterval.Duration, "mirror.flush-interval", o.FlushInterval.Duration, ""+
		"How long one projection write may wait for more rows.")
	fs.DurationVar(&o.SweepInterval.Duration, "mirror.sweep-interval", o.SweepInterval.Duration, ""+
		"How often the projection is verified against the control plane.")
}

// ApplyTo fills up the projection configuration from the flags.
func (o *MirrorOptions) ApplyTo(cfg *ctrlmgrconfig.MirrorConfiguration) error {
	if o == nil || cfg == nil {
		return nil
	}

	*cfg = *o.MirrorConfiguration
	return nil
}

// Validate checks the projection settings.
func (o *MirrorOptions) Validate() []error {
	if o == nil {
		return nil
	}
	var errs []error
	if o.BatchSize < 0 {
		errs = append(errs, fmt.Errorf("--mirror.batch-size must not be negative"))
	}
	if o.FlushInterval.Duration < 0 {
		errs = append(errs, fmt.Errorf("--mirror.flush-interval must not be negative"))
	}
	if o.SweepInterval.Duration < 0 {
		errs = append(errs, fmt.Errorf("--mirror.sweep-interval must not be negative"))
	}
	return errs
}
