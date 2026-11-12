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

package options

import (
	"github.com/spf13/pflag"
	"gorm.io/gorm"

	genericconfig "github.com/onexstack/onexai/pkg/config"
	onexoptions "github.com/onexstack/onexai/pkg/options"
)

// CoreDBOptions holds the coredb component-configuration block.
//
// It is a thin adapter: the flag set, the validation and the connection are all
// onexai/pkg/options.CoreDBOptions, so the controller manager and the runtime
// services cannot drift apart on what `--coredb.addr` means.
type CoreDBOptions struct {
	*genericconfig.CoreDBConfiguration
}

// NewCoreDBOptions wraps a component-configuration block.
func NewCoreDBOptions(cfg *genericconfig.CoreDBConfiguration) *CoreDBOptions {
	return &CoreDBOptions{CoreDBConfiguration: cfg}
}

// AddFlags adds flags related to the database connection.
func (o *CoreDBOptions) AddFlags(fs *pflag.FlagSet) {
	o.toOptions().AddFlags(fs, "coredb")
}

// ApplyTo fills up the component-configuration block from the flags.
func (o *CoreDBOptions) ApplyTo(cfg *genericconfig.CoreDBConfiguration) error {
	if o == nil || cfg == nil {
		return nil
	}

	*cfg = *o.CoreDBConfiguration
	return nil
}

// Validate checks the connection settings.
func (o *CoreDBOptions) Validate() []error {
	if o == nil {
		return nil
	}
	return o.toOptions().Validate()
}

// NewDB opens the connection. It also tells pkg/dbschema which dialect is
// active, which must happen before the first query.
func (o *CoreDBOptions) NewDB() (*gorm.DB, error) {
	return o.toOptions().NewDB()
}

func (o *CoreDBOptions) toOptions() *onexoptions.CoreDBOptions {
	return &onexoptions.CoreDBOptions{
		Dialect:               o.Dialect,
		Addr:                  o.Addr,
		Username:              o.Username,
		Password:              o.Password,
		Database:              o.Database,
		MaxIdleConnections:    o.MaxIdleConnections,
		MaxOpenConnections:    o.MaxOpenConnections,
		MaxConnectionLifeTime: o.MaxConnectionLifeTime.Duration,
		LogLevel:              o.LogLevel,
		SSLMode:               o.SSLMode,
		SearchPath:            o.SearchPath,
	}
}
