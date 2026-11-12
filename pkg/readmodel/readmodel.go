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

// Package readmodel names the PostgreSQL read model the control plane is
// projected into.
//
// It exists so the writer (oai-controller-manager) and the readers (the runtime
// services) agree on one spelling of the schema, the tables and the watermark
// without either importing the other: the writer must not drag SQL into a
// controller, and a runtime service must not link the controller machinery just
// to read a row.
package readmodel

import (
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultSchema is the PostgreSQL schema every projection table lives in.
const DefaultSchema = "mirror"

// Tables in DefaultSchema.
const (
	ObjectTable = "object"
	StateTable  = "state"
)

// schemaName rejects anything that is not a plain identifier. The schema is
// configuration, and it is the one identifier interpolated into SQL rather than
// bound as a parameter (PostgreSQL has no placeholder for a schema name), so it
// is validated once at construction instead of trusted.
var schemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ValidateSchema reports whether name may be used as a schema name.
func ValidateSchema(name string) error {
	if name == "" {
		return fmt.Errorf("readmodel: schema name is empty")
	}
	if !schemaName.MatchString(name) {
		return fmt.Errorf("readmodel: schema %q is not a plain lower-case identifier", name)
	}
	return nil
}

// API groups the projected kinds belong to. Core resources have an empty group.
const (
	GroupCore  = ""
	GroupAgent = "agent.onexai.io"
	GroupChat  = "chat.onexai.io"
)

// KindRef names one projected resource type.
type KindRef struct {
	Group string
	Kind  string
}

// Kinds is every kind the projection knows about.
//
// It lives here rather than next to the controller so a configuration file can
// be validated without linking the controller machinery: a misspelt kind that is
// silently ignored leaves an operator believing a resource is projected when it
// is not.
var Kinds = []KindRef{
	{Group: GroupAgent, Kind: "Agent"},
	{Group: GroupAgent, Kind: "AgentBinding"},
	{Group: GroupAgent, Kind: "ClusterAgentBinding"},
	{Group: GroupAgent, Kind: "AuthorizationRule"},
	{Group: GroupAgent, Kind: "CustomCommand"},
	{Group: GroupAgent, Kind: "KnowledgeBase"},
	{Group: GroupAgent, Kind: "Memory"},
	{Group: GroupAgent, Kind: "MCPServer"},
	{Group: GroupAgent, Kind: "Model"},
	{Group: GroupAgent, Kind: "Prompt"},
	{Group: GroupAgent, Kind: "Skill"},
	{Group: GroupAgent, Kind: "Webhook"},
	{Group: GroupChat, Kind: "BotApp"},
	{Group: GroupChat, Kind: "Command"},
	{Group: GroupChat, Kind: "PeerBinding"},
}

// KindNames returns the known kind names.
func KindNames() []string {
	names := make([]string, 0, len(Kinds))
	for _, kind := range Kinds {
		names = append(names, kind.Kind)
	}
	return names
}

// IsKnownKind reports whether name is a projected kind.
func IsKnownKind(name string) bool {
	for _, kind := range Kinds {
		if kind.Kind == name {
			return true
		}
	}
	return false
}

// SystemNamespace reports a namespace the control plane creates for itself.
//
// They are not business lines: the business-line sync keeps them out of
// agent.namespace, and the projection keeps them out of mirror.object so the two
// lists agree. Sharing the predicate is what keeps the two copies from drifting.
func SystemNamespace(name string) bool {
	switch name {
	case metav1.NamespaceDefault, metav1.NamespaceSystem, metav1.NamespacePublic,
		corev1.NamespaceNodeLease:
		return true
	default:
		return false
	}
}

// Object returns the qualified object table name for a schema.
func Object(schema string) string { return schema + "." + ObjectTable }

// State returns the qualified state table name for a schema.
func State(schema string) string { return schema + "." + StateTable }

// Watermark is one kind's projection state.
//
// UpdatedAt is the only health signal the read side has: a watermark older than
// the configured budget means the projection is not keeping up, and a List
// served from it would be answering from a stale picture.
type Watermark struct {
	Group       string
	Kind        string
	ObjectCount int64
	LastSweepAt time.Time
	LastRV      int64
	UpdatedAt   time.Time
}

// Fresh reports whether the watermark is within budget at the given time.
func (w Watermark) Fresh(now time.Time, budget time.Duration) bool {
	if w.UpdatedAt.IsZero() {
		return false
	}
	return now.Sub(w.UpdatedAt) <= budget
}
