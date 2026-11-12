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
	"sort"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// dbColumns is the column set of every agent-schema table, read from the live
// PostgreSQL database (`information_schema.columns`, schema `agent`) on
// 2026-10-14, before and after the record/model merge.
//
// The webhook table is the exception: it does not exist in that database yet, so
// its list is the model's own definition rather than a reading. It becomes a
// reading the first time the service runs against a database with the table —
// which is what AutoMigrate creates it for, and why being wrong here would show
// up as a missing column rather than a second one.
//
// agent.webhooks is the same kind of entry: the column is new, AutoMigrate adds
// it to the existing table on the next start.
//
// It is written down here rather than queried at test time so the test needs no
// database. What it guards is the failure AutoMigrate leaves behind: GORM never
// drops or renames a column, so a field whose gorm tag derives a *different*
// name than the column that holds the data adds a second, empty column and the
// original becomes unreachable — silently, because both inserts and the reads
// that name the column keep working.
//
// `mcp`'s list fields sit next to `column:mcps` for exactly that reason: the Go
// field is MCPs, GORM's naming strategy would derive mc_ps, and the explicit tag
// is what keeps it pointed at the column that exists.
//
// The comparison is by set, not by order: AutoMigrate asks whether a column
// exists, not where it sits, and several columns were added to the live tables
// after the fact (agent_names, platform, capabilities) so their position differs
// from the struct's.
//
// workspace was renamed to namespace on 2026-11-06
// (hack/rename-workspace-to-namespace.sql) so the column matches the Go field
// name and GORM's derivation; the lists below use the new name.
var dbColumns = map[string][]string{
	// sys_prompt and tags are history: the runtime derives the prompt binding
	// list from the Agent spec, so the model no longer maps either column.
	// They were dropped from the local database on 2026-11-06 by
	// hack/drop-agent-legacy-columns.sql (backed up first); AutoMigrate cannot
	// recreate them because no model maps them.
	"agent": {"id", "name", "display_name", "status", "description", "owner", "model",
		"skills", "mcps", "knowledge_bases", "require_approval", "authz_backends",
		"namespace", "webhooks", "authn_webhooks", "authz_webhooks", "runtime_profile",
		"output_schema", "output_schema_strict", "memory_enabled", "memory_embedding_model",
		"memory_ttl_days", "memory_auto_save_every", "created_at", "updated_at"},
	// mode/contract/entrypoint are new: AutoMigrate adds them to an existing
	// table on the next start (the same situation as webhooks above).
	"skill": {"id", "name", "display_name", "status", "description", "provider", "params",
		"cache_ttl_seconds", "as_cmd", "namespace", "created_at", "updated_at",
		"mode", "contract", "entrypoint"},
	"mcp": {"id", "name", "display_name", "description", "status", "mode", "protocol", "url", "client_config",
		"headers", "allowed_tools", "require_approval", "resource_mappings", "namespace",
		"created_at", "updated_at"},
	// project_id is history: the knowledge-question platform backend was removed
	// on 2026-11-06, so the model no longer maps the column. The column stays
	// in the legacy table (AutoMigrate never drops one) but is intentionally
	// absent from the model's derived set.
	"knowledge_base": {"id", "name", "display_name", "description", "status", "dataset_id",
		"score_threshold", "top_k", "namespace", "created_at",
		"updated_at", "platform"},
	"secret": {"id", "secret_id", "secret_key", "description", "created_at", "updated_at"},
	"prompt": {"id", "name", "display_name", "status", "description", "content", "data_sources",
		"namespace", "created_at", "updated_at"},
	"model": {"id", "name", "display_name", "status", "description", "owner", "provider", "base_url", "api_key", "model", "created_at", "updated_at", "capabilities"},
	"asset": {"id", "owner_id", "kind", "object_key", "url", "mime_type", "name", "size", "width", "height", "sha256", "source", "expires_at", "created_at"},
	"authz_rule": {"id", "name", "subject_type", "subject_id", "resource_kind", "resource_name",
		"resource_attributes", "verbs", "effect", "status", "description", "owner",
		"created_at", "updated_at", "agent_names"},
	"interaction": {"id", "interaction_id", "source", "caller_key", "caller_name", "chat_type",
		"session_type", "chat_id", "sender_id", "sender_name", "is_sre", "user_input",
		"final_answer", "total_ms", "status", "error_message", "created_at", "agent_names"},
	"k8sgpt_endpoint": {"id", "cluster_name", "account_id", "protocol", "endpoint", "is_primary_mcp", "created_at", "updated_at"},
	// retrieval_audit is new: AutoMigrate creates the table on the next start.
	"retrieval_audit": {"id", "user_id", "agent", "knowledge_bases", "documents", "passages",
		"bytes", "trace_id", "created_at"},
	// custom_command is new: AutoMigrate creates the table on the next start.
	"custom_command": {"id", "name", "display_name", "description", "status", "type", "aliases",
		"usage", "parameters", "prompt", "http", "tool", "output", "timeout_seconds",
		"namespace", "created_at", "updated_at"},
	"custom_command_audit": {"id", "uid", "command", "agent", "kind", "phase", "reason",
		"user_id", "conversation_id", "surface", "duration_ms", "evaluation_error", "created_at"},
	"custom_command_approval": {"id", "approval_id", "uid", "command", "agent", "user_id",
		"conversation_id", "tool", "mcp_server", "arguments", "phase", "reason", "result",
		"created_at", "expires_at", "decided_at", "decided_by"},
	"webhook": {"id", "name", "status", "type", "url", "client_config", "secret_ref", "events",
		"audiences", "timeout_seconds", "failure_mode", "headers", "template", "owner", "created_at",
		"updated_at"},
}

// TestModelColumnsMatchTheDatabase is the guard on every gorm tag in this
// package: each type must derive exactly the columns its table has.
func TestModelColumnsMatchTheDatabase(t *testing.T) {
	models := []struct {
		table string
		value any
	}{
		{"agent", &AgentM{}},
		{"skill", &SkillM{}},
		{"mcp", &MCPM{}},
		{"knowledge_base", &KnowledgeBaseM{}},
		{"secret", &SecretM{}},
		{"prompt", &PromptM{}},
		{"model", &ModelM{}},
		{"asset", &AssetM{}},
		{"authz_rule", &AuthzRuleM{}},
		{"interaction", &InteractionM{}},
		{"k8sgpt_endpoint", &K8SGPTEndpointM{}},
		{"webhook", &WebhookM{}},
		{"retrieval_audit", &RetrievalAuditM{}},
		{"custom_command", &CustomCommandM{}},
		{"custom_command_audit", &CustomCommandAuditM{}},
		{"custom_command_approval", &CustomCommandApprovalM{}},
	}

	for _, tc := range models {
		t.Run(tc.table, func(t *testing.T) {
			want, ok := dbColumns[tc.table]
			if !ok {
				t.Fatalf("no recorded column set for %s: add it, read from the live database", tc.table)
			}
			got := derivedColumns(t, tc.value)
			if diff := setDiff(want, got); len(diff) > 0 {
				t.Errorf("columns in the database that the model does not derive: %v\n"+
					"(AutoMigrate would add a second column and leave the data behind)", diff)
			}
			// The `tags` column is deliberately not derived: the record's labels
			// are the object's metadata.labels now, and the column is the history
			// hack/pg2crd reads. It is excluded from dbColumns for that reason,
			// so this guard is about the columns a model still owns.
			if diff := setDiff(got, want); len(diff) > 0 {
				t.Errorf("columns the model derives that the database does not have: %v\n"+
					"(AutoMigrate would try to add them)", diff)
			}
		})
	}
}

// derivedColumns returns the column names GORM would use for a model, using the
// same default naming strategy the store runs with.
func derivedColumns(t *testing.T, value any) []string {
	t.Helper()
	s, err := schema.Parse(value, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.DBName == "" {
			continue
		}
		out = append(out, f.DBName)
	}
	sort.Strings(out)
	return out
}

// setDiff returns the elements of a that b does not contain.
func setDiff(a, b []string) []string {
	has := make(map[string]bool, len(b))
	for _, v := range b {
		has[v] = true
	}
	var out []string
	for _, v := range a {
		if !has[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
