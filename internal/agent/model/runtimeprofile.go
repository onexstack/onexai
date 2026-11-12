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
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() {
	registry.Register(&RuntimeProfileM{})
	registry.Register(&RuntimeHostingM{})
}

// RuntimeProfileM is the runtime contract a control-plane RuntimeProfile
// carries. It is read from the CR's status (resolved endpoint/capabilities)
// and from its spec; the row exists so the runtime never reads a CR directly.
type RuntimeProfileM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex:idx_runtime_profile_ns_name;size:128" json:"name"`
	Namespace   string `gorm:"column:namespace;uniqueIndex:idx_runtime_profile_ns_name;size:128;index" json:"namespace"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Status      string `gorm:"size:32" json:"status"`
	Description string `gorm:"type:text" json:"description"`
	Owner       string `gorm:"size:256" json:"owner"`

	Kind     string `gorm:"size:16" json:"kind"`
	Endpoint string `gorm:"size:1024" json:"endpoint"`
	// ClientConfigJSON is the shared Kubernetes-shaped endpoint configuration,
	// serialized. Empty means the row uses Endpoint alone.
	ClientConfigJSON string `gorm:"column:client_config;type:text" json:"client_config"`
	HostingRef       string `gorm:"size:128" json:"hosting_ref"`
	ModelAccess      string `gorm:"size:16" json:"model_access"`

	AuthBearerSecret   string `gorm:"size:128" json:"auth_bearer_secret"`
	AuthBearerKey      string `gorm:"size:128" json:"auth_bearer_key"`
	AuthCASecret       string `gorm:"size:128" json:"auth_ca_secret"`
	AuthCAKey          string `gorm:"size:128" json:"auth_ca_key"`
	AuthInsecureVerify bool   `json:"auth_insecure_verify"`

	RunTimeoutNanos  int64 `json:"run_timeout_nanos"`
	MaxConcurrentRun int32 `json:"max_concurrent_run"`
	MaxToolCalls     int32 `json:"max_tool_calls"`

	// ResolvedEndpoint is the endpoint the gateway dials. It is status, not
	// spec: a hosting controller writes it after the instance is ready.
	ResolvedEndpoint string `gorm:"size:1024" json:"resolved_endpoint"`
	Capabilities     string `gorm:"type:text" json:"capabilities"`

	Labels    StringMap `gorm:"-" json:"labels"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the runtime profile table name.
func (RuntimeProfileM) TableName() string { return dbschema.Table(dbschema.Agent, "runtime_profile") }

// RuntimeHostingM is one hosting pool row.
type RuntimeHostingM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex:idx_runtime_hosting_ns_name;size:128" json:"name"`
	Namespace   string `gorm:"column:namespace;uniqueIndex:idx_runtime_hosting_ns_name;size:128;index" json:"namespace"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Status      string `gorm:"size:32" json:"status"`
	Description string `gorm:"type:text" json:"description"`
	Owner       string `gorm:"size:256" json:"owner"`

	ProfileRef   string     `gorm:"size:128" json:"profile_ref"`
	Mode         string     `gorm:"size:16" json:"mode"`
	Image        string     `gorm:"size:1024" json:"image"`
	Command      StringList `gorm:"type:text" json:"command"`
	Env          string     `gorm:"type:text" json:"env"`
	MaxInstances int32      `json:"max_instances"`
	IdleTTLNanos int64      `json:"idle_ttl_nanos"`
	Resources    string     `gorm:"type:text" json:"resources"`

	// Instances is the hosting status, JSON-encoded.
	Instances string `gorm:"type:text" json:"instances"`

	Labels    StringMap `gorm:"-" json:"labels"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the runtime hosting table name.
func (RuntimeHostingM) TableName() string { return dbschema.Table(dbschema.Agent, "runtime_hosting") }

// RunTimeout returns the configured run timeout, or zero.
func (m RuntimeProfileM) RunTimeout() time.Duration {
	return time.Duration(m.RunTimeoutNanos)
}

// IdleTTL returns the configured idle TTL, or zero.
func (m RuntimeHostingM) IdleTTL() time.Duration {
	return time.Duration(m.IdleTTLNanos)
}
