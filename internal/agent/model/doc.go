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

// Package model holds the agent service's persistent types: one file per
// database table, named after it, each carrying the GORM tags that describe its
// columns and a TableName method that resolves them per dialect.
//
// A type here is both the row and the value the business layer reads. That is
// deliberate and, until recently, was not the case: the package used to split
// the two into a domain type (internal/agent/types) and a GORM record
// (internal/agent/store/sqlstore), with a toDomain/fromDomain pair per entity
// between them. That split existed to let two storage backends — the database
// and a Feishu multi-dimensional table — share one domain type. With the
// Bitable backend gone there is one backend, so there is one type.
//
// Columns whose text carries a JSON array are typed as StringList, which
// encodes and decodes the format the columns already hold. Nothing else here
// knows how a column is spelled in SQL.
package model
