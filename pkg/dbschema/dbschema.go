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

// Package dbschema resolves a model's physical table name against the database
// dialect the process is connected to.
//
// The three services share one logical schema, expressed two ways:
//
//   - MySQL: one flat schema, every table carrying a service prefix, e.g.
//     onexai_chat_session.
//   - PostgreSQL: one database holding one schema per service, e.g. chat.session.
//
// Models call Table() from their TableName() method, so the same struct works
// against either backend and switching dialects is a configuration change.
// Configure must run before the first query — CoreDBOptions.NewDB does it.
package dbschema

import (
	"fmt"
	"sync/atomic"
)

// Dialect identifies the SQL backend.
type Dialect string

const (
	// MySQL is the MySQL/MariaDB backend.
	MySQL Dialect = "mysql"
	// Postgres is the PostgreSQL backend.
	Postgres Dialect = "postgres"
)

// Service identifies which service owns a table. It decides both the MySQL
// table prefix and the PostgreSQL schema.
type Service string

const (
	// Agent owns the agent-side resource tables.
	Agent Service = "agent"
	// Chat owns the chat/session/ticket tables.
	Chat Service = "chat"
	// JobServer owns the job/cronjob tables.
	JobServer Service = "jobserver"
)

// schemaByService maps a service to its PostgreSQL schema and (via mysqlPrefix)
// its MySQL table prefix.
var schemaByService = map[Service]string{
	Agent:     "agent",
	Chat:      "chat",
	JobServer: "job",
}

// current holds the configured Dialect. It is written once at startup (or once
// per pass by the migration tool) and read on every query, hence atomic.
var current atomic.Value

func init() {
	current.Store(MySQL)
}

// ParseDialect validates and converts a configured dialect string. An empty
// string means MySQL: that keeps configs written before the switch working.
func ParseDialect(s string) (Dialect, error) {
	switch Dialect(s) {
	case "", MySQL:
		return MySQL, nil
	case Postgres:
		return Postgres, nil
	default:
		return "", fmt.Errorf("unknown database dialect %q (want mysql|postgres)", s)
	}
}

// Configure sets the dialect used by Table and Schema.
func Configure(d Dialect) {
	current.Store(d)
}

// Current returns the configured dialect.
func Current() Dialect {
	d, _ := current.Load().(Dialect)
	return d
}

// Schema returns the PostgreSQL schema that owns the given service's tables.
func Schema(svc Service) string {
	return schemaByService[svc]
}

// mysqlPrefix returns the table-name prefix used on MySQL for the given service.
func mysqlPrefix(svc Service) string {
	return "onexai_" + string(svc) + "_"
}

// Table resolves the physical table name of name (which is the bare, unprefixed
// name such as "session") for the service that owns it, under the configured
// dialect.
//
// On PostgreSQL the result is schema-qualified ("chat.session"). GORM's
// postgres driver understands that form end to end: the migrator's
// CurrentSchema() splits it for information_schema lookups, the dialector
// quotes it as "chat"."session", and index names are flattened to
// idx_chat_session_name.
func Table(svc Service, name string) string {
	if Current() == Postgres {
		return Schema(svc) + "." + name
	}
	return mysqlPrefix(svc) + name
}
