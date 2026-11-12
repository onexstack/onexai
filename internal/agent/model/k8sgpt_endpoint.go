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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&K8SGPTEndpointM{}) }

// K8SGPTEndpointM maps a cluster to its k8sgpt endpoint. It mirrors the
// opsassist K8SGPTEndpointM table.
type K8SGPTEndpointM struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	ClusterName  string `gorm:"uniqueIndex;size:191" json:"cluster_name"`
	AccountID    string `gorm:"size:64" json:"account_id"` // e.g. "661633860861"
	Protocol     string `gorm:"size:32" json:"protocol"`   // "SSE" | "HTTP"
	Endpoint     string `gorm:"size:512" json:"endpoint"`
	IsPrimaryMCP string `gorm:"size:64" json:"is_primary_mcp"`
	CreatedAt    int64  `json:"created_at"` // unix millis
	UpdatedAt    int64  `json:"updated_at"`
}

// TableName returns the k8sgpt endpoint table name.
func (K8SGPTEndpointM) TableName() string {
	return dbschema.Table(dbschema.Agent, "k8sgpt_endpoint")
}
