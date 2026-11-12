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

import "github.com/onexstack/onexai/pkg/dbschema"

// ClusterEndpointM is a declarative k8sgpt/MCP endpoint. It is read from the
// control plane and never stored in a table.
type ClusterEndpointM struct {
	Name          string    `gorm:"-" json:"name"`
	Namespace     string    `gorm:"-" json:"namespace"`
	DisplayName   string    `gorm:"-" json:"display_name"`
	Description   string    `gorm:"-" json:"description"`
	Status        string    `gorm:"-" json:"status"`
	ClusterName   string    `gorm:"-" json:"cluster_name"`
	AccountID     string    `gorm:"-" json:"account_id"`
	Protocol      string    `gorm:"-" json:"protocol"`
	Endpoint      string    `gorm:"-" json:"endpoint"`
	PrimaryMCP    bool      `gorm:"-" json:"primary_mcp"`
	Labels        StringMap `gorm:"-" json:"labels,omitempty"`
	CreatedAt     int64     `gorm:"-" json:"created_at"`
	UpdatedAt     int64     `gorm:"-" json:"updated_at"`
	Conditions    string    `gorm:"-" json:"conditions,omitempty"`
	LastProbeTime int64     `gorm:"-" json:"last_probe_time,omitempty"`
	LatencyMillis int32     `gorm:"-" json:"latency_millis,omitempty"`
	Message       string    `gorm:"-" json:"message,omitempty"`
}

// TableName satisfies the registry's naming contract for a row that has no
// table; it is never used to write.
func (ClusterEndpointM) TableName() string {
	return dbschema.Table(dbschema.Agent, "cluster_endpoint")
}
