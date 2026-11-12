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

package model

import (
	"encoding/json"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TableNameMetadataM returns the physical table name under the configured
// dialect: chat.metadata on PostgreSQL, onexai_chat_metadata on MySQL.
func TableNameMetadataM() string { return dbschema.Table(dbschema.Chat, "metadata") }

// MetadataM represents a row from the metadata table. The Data column
// holds an arbitrary JSON document whose fields vary by Domain (resource type).
type MetadataM struct {
	ID     int             `gorm:"column:id;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`
	Domain string          `gorm:"column:domain;not null;default:'';comment:资源类型" json:"domain"`
	Data   json.RawMessage `gorm:"column:data;type:json;comment:资源元数据(JSON)" json:"data"`
	// ClusterID mirrors the opsassist cluster column. It is read-only here
	// ("->"), which makes it Creatable/Updatable=false but does NOT exclude it
	// from AutoMigrate: only "-:all"/"-:migration" set IgnoreMigration. So
	// AutoMigrate does create and check this column, and it is a plain nullable
	// text column — not a generated one. Do not turn it into
	// GENERATED ALWAYS AS (...): a generated column has no ordinary default, so
	// AutoMigrate would issue a DROP DEFAULT and PostgreSQL would reject it.
	ClusterID string `gorm:"column:cluster_id;->" json:"cluster_id"`
}

// TableName returns the table name.
func (*MetadataM) TableName() string {
	return TableNameMetadataM()
}

func init() {
	registry.Register(&MetadataM{})
}
