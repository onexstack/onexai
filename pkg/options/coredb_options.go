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
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/pkg/dbschema"
)

var _ genericoptions.IOptions = (*CoreDBOptions)(nil)

// CoreDBOptions is the `coredb` block: one database configuration that can be
// pointed at either MySQL or PostgreSQL.
//
// It exists because the dialect is a deployment choice, not a compile-time one.
// The fields are the union of onexstack's MySQLOptions and PostgreSQLOptions;
// NewDB forwards to whichever one Dialect selects, so the connection itself is
// still built by onexstack rather than by hand.
type CoreDBOptions struct {
	// Dialect selects the backend: "mysql" | "postgres". Empty means mysql,
	// which keeps configs written before the switch working unchanged.
	Dialect string `json:"dialect" mapstructure:"dialect"`
	// Addr is host:port.
	Addr string `json:"addr,omitempty" mapstructure:"addr"`
	// Username is the database user.
	Username string `json:"username,omitempty" mapstructure:"username"`
	// Password is the database password.
	Password string `json:"-" mapstructure:"password"`
	// Database is the database (schema) name.
	Database string `json:"database" mapstructure:"database"`

	// MaxIdleConnections is the size of the idle connection pool.
	MaxIdleConnections int `json:"max-idle-connections,omitempty" mapstructure:"max-idle-connections"`
	// MaxOpenConnections is the maximum number of open connections.
	MaxOpenConnections int `json:"max-open-connections,omitempty" mapstructure:"max-open-connections"`
	// MaxConnectionLifeTime is how long a connection may be reused.
	MaxConnectionLifeTime time.Duration `json:"max-connection-life-time,omitempty" mapstructure:"max-connection-life-time"`
	// LogLevel is the gorm log level.
	LogLevel int `json:"log-level" mapstructure:"log-level"`

	// SSLMode is the libpq sslmode; PostgreSQL only.
	SSLMode string `json:"sslmode,omitempty" mapstructure:"sslmode"`
	// SearchPath is the PostgreSQL schema search path; PostgreSQL only. Unused
	// by the onexai services, which qualify table names with their schema
	// instead (see pkg/dbschema), but kept so a deployment can set it.
	SearchPath string `json:"search-path,omitempty" mapstructure:"search-path"`
}

// NewCoreDBOptions creates a CoreDBOptions with default values. The default
// dialect is mysql, matching the deployment that existed before PostgreSQL was
// introduced.
func NewCoreDBOptions() *CoreDBOptions {
	return &CoreDBOptions{
		Dialect:               string(dbschema.MySQL),
		Addr:                  "127.0.0.1:3306",
		MaxIdleConnections:    100,
		MaxOpenConnections:    100,
		MaxConnectionLifeTime: 10 * time.Second,
		LogLevel:              1, // Silent
		SSLMode:               "disable",
	}
}

// mysqlOptions builds the onexstack options for the MySQL path.
func (o *CoreDBOptions) mysqlOptions() *genericoptions.MySQLOptions {
	return &genericoptions.MySQLOptions{
		Addr:                  o.Addr,
		Username:              o.Username,
		Password:              o.Password,
		Database:              o.Database,
		MaxIdleConnections:    o.MaxIdleConnections,
		MaxOpenConnections:    o.MaxOpenConnections,
		MaxConnectionLifeTime: o.MaxConnectionLifeTime,
		LogLevel:              o.LogLevel,
	}
}

// postgresOptions builds the onexstack options for the PostgreSQL path.
func (o *CoreDBOptions) postgresOptions() *genericoptions.PostgreSQLOptions {
	return &genericoptions.PostgreSQLOptions{
		Addr:                  o.Addr,
		Username:              o.Username,
		Password:              o.Password,
		Database:              o.Database,
		MaxIdleConnections:    o.MaxIdleConnections,
		MaxOpenConnections:    o.MaxOpenConnections,
		MaxConnectionLifeTime: o.MaxConnectionLifeTime,
		LogLevel:              o.LogLevel,
		SSLMode:               o.SSLMode,
		SearchPath:            o.SearchPath,
	}
}

// Validate checks the dialect and forwards to the selected dialect's own
// validation.
func (o *CoreDBOptions) Validate() []error {
	errs := []error{}

	dialect, err := dbschema.ParseDialect(o.Dialect)
	if err != nil {
		return []error{err}
	}

	switch dialect {
	case dbschema.Postgres:
		errs = append(errs, o.postgresOptions().Validate()...)
	default:
		errs = append(errs, o.mysqlOptions().Validate()...)
	}

	return errs
}

// AddFlags binds the options to command-line flags. The flag names are a
// superset of the old mysql-only set: --coredb.addr and friends keep working,
// and --coredb.sslmode / --coredb.search-path are added for PostgreSQL.
func (o *CoreDBOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Dialect, fullPrefix+".dialect", o.Dialect, ""+
		"Database dialect: mysql | postgres.")
	fs.StringVar(&o.Addr, fullPrefix+".addr", o.Addr, ""+
		"Database service address (host:port).")
	fs.StringVar(&o.Username, fullPrefix+".username", o.Username, ""+
		"Username for access to the database.")
	fs.StringVar(&o.Password, fullPrefix+".password", o.Password, ""+
		"Password for access to the database.")
	fs.StringVar(&o.Database, fullPrefix+".database", o.Database, ""+
		"Database name for the server to use.")
	fs.IntVar(&o.MaxIdleConnections, fullPrefix+".max-idle-connections", o.MaxIdleConnections, ""+
		"Maximum idle connections allowed to connect to the database.")
	fs.IntVar(&o.MaxOpenConnections, fullPrefix+".max-open-connections", o.MaxOpenConnections, ""+
		"Maximum open connections allowed to connect to the database.")
	fs.DurationVar(&o.MaxConnectionLifeTime, fullPrefix+".max-connection-life-time", o.MaxConnectionLifeTime, ""+
		"Maximum connection life time allowed to connect to the database.")
	fs.IntVar(&o.LogLevel, fullPrefix+".log-level", o.LogLevel, ""+
		"Specify gorm log level.")
	fs.StringVar(&o.SSLMode, fullPrefix+".sslmode", o.SSLMode, ""+
		"PostgreSQL SSL mode: disable, allow, prefer, require, verify-ca or verify-full.")
	fs.StringVar(&o.SearchPath, fullPrefix+".search-path", o.SearchPath, ""+
		"PostgreSQL schema search path, e.g. \"agent,public\".")
}

// NewDB opens the connection. It also tells pkg/dbschema which dialect is
// active, so table names resolve correctly — this must happen before the first
// query and before registry.Migrate, which is why the call lives here rather
// than at each call site.
func (o *CoreDBOptions) NewDB() (*gorm.DB, error) {
	dialect, err := dbschema.ParseDialect(o.Dialect)
	if err != nil {
		return nil, err
	}

	var db *gorm.DB
	switch dialect {
	case dbschema.Postgres:
		db, err = o.postgresOptions().NewDB()
	default:
		db, err = o.mysqlOptions().NewDB()
	}
	if err != nil {
		return nil, err
	}

	dbschema.Configure(dialect)

	return db, nil
}
