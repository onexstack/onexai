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
	"sync"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestTableNamesResolvePerDialect pins both physical names of every agent
// table: the flat onexai_agent_* names on MySQL, and the agent.<name> form
// on PostgreSQL. A drifted name means AutoMigrate creates an empty table and
// the real data becomes unreachable.
//
// It lives beside the TableName methods it pins rather than in the store,
// which is where those methods used to be.
func TestTableNamesResolvePerDialect(t *testing.T) {
	tables := map[string]struct {
		bare  string
		table func() string
	}{
		"agent":          {"agent", AgentM{}.TableName},
		"skill":          {"skill", SkillM{}.TableName},
		"mcp":            {"mcp", MCPM{}.TableName},
		"knowledge_base": {"knowledge_base", KnowledgeBaseM{}.TableName},
		"secret":         {"secret", SecretM{}.TableName},
		"access_key":     {"access_key", TableNameAccessKeyM},
		"prompt":         {"prompt", PromptM{}.TableName},
		"model":          {"model", ModelM{}.TableName},
		"interaction":    {"interaction", InteractionM{}.TableName},
		"authz_rule":     {"authz_rule", AuthzRuleM{}.TableName},
		"k8sgpt":         {"k8sgpt_endpoint", K8SGPTEndpointM{}.TableName},
		"webhook":        {"webhook", WebhookM{}.TableName},
	}

	t.Cleanup(func() { dbschema.Configure(dbschema.MySQL) })

	dbschema.Configure(dbschema.MySQL)
	for what, tc := range tables {
		if got, want := tc.table(), "onexai_agent_"+tc.bare; got != want {
			t.Errorf("%s table name on mysql = %q, want %q", what, got, want)
		}
	}

	dbschema.Configure(dbschema.Postgres)
	for what, tc := range tables {
		if got, want := tc.table(), "agent."+tc.bare; got != want {
			t.Errorf("%s table name on postgres = %q, want %q", what, got, want)
		}
	}
}

// TestAgentMCPsColumnName pins the physical column of AgentM.MCPs. The Go
// field is MCPs, which GORM's naming strategy derives as mc_ps by default, so
// the explicit column:mcps tag is load-bearing: a drifted name means
// AutoMigrate creates a second, empty column and the grants become invisible.
// This is the same class of failure TestTableNamesResolvePerDialect guards
// against, one level down.
func TestAgentMCPsColumnName(t *testing.T) {
	s, err := schema.Parse(&AgentM{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse AgentM schema: %v", err)
	}
	f := s.LookUpField("MCPs")
	if f == nil {
		t.Fatal("AgentM has no field MCPs")
	}
	if got, want := f.DBName, "mcps"; got != want {
		t.Errorf("AgentM.MCPs column = %q, want %q", got, want)
	}
}

// TestStringListRoundTrips pins the column encoding the type exists to
// preserve: a compact JSON array for a non-empty list, and an **empty string**
// for an empty one. Both directions matter — the empty string is what every
// existing row holds, so a Value that wrote "[]" instead would leave the column
// holding two formats at once.
func TestStringListRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   StringList
		want string
	}{
		{"empty", nil, ""},
		{"empty-but-allocated", StringList{}, ""},
		{"one", StringList{"a"}, `["a"]`},
		{"many", StringList{"a", "b"}, `["a","b"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.in.Value()
			if err != nil {
				t.Fatalf("Value() error = %v", err)
			}
			if got, ok := v.(string); !ok || got != tc.want {
				t.Fatalf("Value() = %#v, want %q", v, tc.want)
			}

			var back StringList
			if err := back.Scan(tc.want); err != nil {
				t.Fatalf("Scan(%q) error = %v", tc.want, err)
			}
			if len(back) != len(tc.in) {
				t.Fatalf("round trip = %#v, want %#v", back, tc.in)
			}
		})
	}
}

// TestStringListScanToleratesWhatTheColumnHolds covers the reads that are not
// a round trip of a write: NULL, and a value that is not JSON at all. Neither
// may fail the query that loaded the row.
func TestStringListScanToleratesWhatTheColumnHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"nil", nil},
		{"empty", ""},
		{"bytes", []byte(`["x"]`)},
		{"not-json", "a,b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var l StringList = StringList{"stale"}
			if err := l.Scan(tc.in); err != nil {
				t.Fatalf("Scan(%#v) error = %v", tc.in, err)
			}
			if tc.name == "bytes" && len(l) != 1 {
				t.Errorf("Scan([]byte) = %#v, want one element", l)
			}
		})
	}

	var l StringList
	if err := l.Scan(42); err == nil {
		t.Error("Scan(int) = nil error, want a refusal")
	}
}
