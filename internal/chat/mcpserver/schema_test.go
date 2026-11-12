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

package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/options"
)

// wantTools is the complete tool set this service publishes. It is written out
// rather than derived from the annotations on purpose: the annotation says a
// command is *allowed* to be a tool, and this list is the decision that it *is*
// one. A command that gains the annotation without being added here fails the
// test, which is the review gate for widening the surface a model can reach.
//
// `askai-mcp` is deliberately absent: it has no catalogued command, so it is
// never published (see "MCP tools with no catalogued command are never filtered
// by agent grants" in the server).
var wantTools = []string{
	"assign",
	"chat",
	"close",
	"cronjob_create",
	"cronjob_delete",
	"cronjob_get",
	"cronjob_list",
	"cronjob_update",
	"duty",
	"duty_current",
	"duty_escalation",
	"duty_holiday",
	"duty_override",
	"duty_schedule",
	"duty_show",
	"duty_timeline",
	"duty_unavailable",
	"hand",
	"invite",
	"job_create",
	"job_delete",
	"job_get",
	"job_list",
	"job_update",
	"level",
	"mark",
	"oncall",
	"open",
	"pipeline",
	"pod",
	"save",
}

// argTokenRe mirrors cobrax's own positional-argument scanner, which is
// unexported. It is duplicated rather than approximated so this test measures
// the same thing the schema builder does: if the two ever disagree, the test's
// idea of "declared an argument" would drift from the one that generates the
// schema, and the assertions would go quiet exactly when they matter.
var argTokenRe = regexp.MustCompile(`([<\[])([^>\]\s.]+)(\.{3})?[>\]]`)

// klogFlags are the process-global flags the k8s client libraries register on
// flag.CommandLine. The command tree adds that whole flag set to its persistent
// flags, so every leaf inherits them; cobrax drops help/hidden/deprecated but
// has no opinion on these. If one shows up in a tool schema, the model is being
// offered a logging flag it cannot meaningfully set.
var klogFlags = []string{
	"add_dir_header",
	"alsologtostderr",
	"log_backtrace_at",
	"log_dir",
	"log_file",
	"log_file_max_size",
	"logtostderr",
	"one_output",
	"skip_headers",
	"skip_log_headers",
	"stderrthreshold",
	"v",
	"vmodule",
}

// buildServer builds the MCP server the way New does, against a test-owned
// StaticRuntime. It reuses the real selector and the real command factory, so
// the schemas under test are the ones a client would receive.
func buildServer(t *testing.T) *Bridge {
	t.Helper()

	srv, err := New(&options.MCPOptions{
		Enabled: true,
		Addr:    ":0",
		Name:    "oai-chat-apiserver",
		Version: "test",
	}, &runtime.StaticRuntime{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned a nil server for an enabled config")
	}
	return srv
}

// schemaOf returns a tool's input schema.
//
// cobrax builds the schema as JSON and stores it in RawInputSchema, leaving the
// structured InputSchema empty — mcp-go marshals whichever of the two is set.
// Reading InputSchema directly would therefore find no properties on any tool
// and make every assertion here vacuously true.
func schemaOf(t *testing.T, tool *mcp.Tool) mcp.ToolInputSchema {
	t.Helper()

	var schema mcp.ToolInputSchema
	if err := json.Unmarshal(tool.RawInputSchema, &schema); err != nil {
		t.Fatalf("tool %q: RawInputSchema is not a JSON schema: %v", tool.Name, err)
	}
	return schema
}

// TestToolsAreExactlyTheDecidedSet pins the published surface. cobrax's default
// is opt-out rather than opt-in — a command with no annotation at all is
// included — so the selector is the only thing standing between this tree and
// roughly ninety tools, the admin-gated ones among them.
func TestToolsAreExactlyTheDecidedSet(t *testing.T) {
	var got []string
	for _, tool := range buildServer(t).Tools() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)

	want := append([]string(nil), wantTools...)
	sort.Strings(want)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("published tool set differs\n got: %v\nwant: %v", got, want)
	}
}

// TestPositionalArgsReachTheSchema is the regression test for a silently broken
// tool: cobrax reads positional arguments out of cmd.Use with a bracket regex
// and treats a bare upper-case word like "JOB_ID" as prose, not an argument. The
// command still registers, still appears in tools/list, and then fails every
// call with "accepts 1 arg(s), received 0" because the schema offers the model
// no way to pass an ID. Nothing but a schema assertion catches that.
func TestPositionalArgsReachTheSchema(t *testing.T) {
	srv := buildServer(t)
	byName := map[string]*mcp.Tool{}
	for _, tool := range srv.Tools() {
		byName[tool.Name] = tool
	}

	// Walk the same tree cobrax walked, so the Use string checked is the one
	// the schema was built from. A fixture tree that is not this factory's
	// would change every tool name, since the root's name is stripped from
	// each one. It is the catalogue tree, because that is the tree the factory
	// registers tools from: a granted one would be empty here and this walk
	// would pass by finding nothing.
	root := command.NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{},
		newResponder(func() *cobra.Command { return nil }))

	var checked int
	walk(root, func(cmd *cobra.Command) {
		if cmd.Annotations[chat.MCPTool] != chat.True {
			return
		}
		name := toolNameOf(cmd)
		tool, ok := byName[name]
		if !ok {
			t.Errorf("command %q is annotated for MCP but no tool %q was registered", cmd.CommandPath(), name)
			return
		}

		schema := schemaOf(t, tool)
		for _, token := range declaredArgs(cmd.Use) {
			checked++
			if _, ok := schema.Properties[token]; !ok {
				t.Errorf("tool %q: cmd.Use %q declares <%s> but the schema has no %q property (properties: %v)",
					name, cmd.Use, token, token, propertyNames(schema.Properties))
				continue
			}
			if !contains(schema.Required, token) {
				t.Errorf("tool %q: %q should be required (declared as <%s> in cmd.Use %q), required is %v",
					name, token, token, cmd.Use, schema.Required)
			}
		}
	})

	// The walk above asserts only about commands it reaches, so a tree that
	// shrank to nothing — a filter that hid the catalogued commands, say —
	// would take every assertion with it and leave this test green. The count is
	// what makes the walk's silence mean something.
	if checked == 0 {
		t.Fatal("the walk found no command declaring an argument; the tree under test is not the one this factory registers tools from")
	}

	// The six ID-taking commands are named explicitly as well. The scan above
	// covers them only because they declare an argument now; this states the
	// intent, so the failure message says which tool lost its ID rather than
	// only which Use string looks wrong.
	for name, arg := range map[string]string{
		"job_get":        "job_id",
		"job_delete":     "job_id",
		"job_update":     "job_id",
		"cronjob_get":    "cronjob_id",
		"cronjob_delete": "cronjob_id",
		"cronjob_update": "cronjob_id",
	} {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %q is missing entirely", name)
			continue
		}
		if !contains(schemaOf(t, tool).Required, arg) {
			t.Errorf("tool %q does not require %q; a model cannot address a record (required: %v)",
				name, arg, schemaOf(t, tool).Required)
		}
	}

	// Required *flags* are a second, separate declaration path: cobrax reads
	// them from cobra's own required-flag annotation (MarkFlagRequired), not from
	// cmd.Use, so the token scan above cannot see one go missing. They are named
	// explicitly for the same reason the IDs are: a required flag that quietly
	// became optional is a requirement the model never learns, and it will then
	// invent a value for it rather than ask.
	for name, flag := range map[string]string{
		"pipeline": "env",
	} {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %q is missing entirely", name)
			continue
		}
		if !contains(schemaOf(t, tool).Required, flag) {
			t.Errorf("tool %q does not declare --%s required; a model has no way to learn the call needs it (required: %v)",
				name, flag, schemaOf(t, tool).Required)
		}
	}

	// A tree that stopped producing positional arguments would make the scan
	// above vacuous.
	if checked == 0 {
		t.Fatal("no command in the tree declares a positional argument; the Use-token scan is not testing anything")
	}
}

// TestNoGlobalLoggingFlagsInSchemas guards the InheritedFlagSelector. Without
// cobrax.NoFlags each leaf inherits flag.CommandLine, whose klog flags would be
// published as settable parameters on every tool.
func TestNoGlobalLoggingFlagsInSchemas(t *testing.T) {
	for _, tool := range buildServer(t).Tools() {
		schema := schemaOf(t, tool)
		for _, flag := range klogFlags {
			if _, ok := schema.Properties[flag]; ok {
				t.Errorf("tool %q exposes the global logging flag --%s (properties: %v)",
					tool.Name, flag, propertyNames(schema.Properties))
			}
		}
	}
}

// walk visits cmd and every descendant, depth first.
func walk(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, child := range cmd.Commands() {
		walk(child, fn)
	}
}

// toolNameOf mirrors cobrax's naming: the root's name is stripped, and the
// remaining words are joined with underscores.
func toolNameOf(cmd *cobra.Command) string {
	path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	return strings.ReplaceAll(path, " ", "_")
}

// declaredArgs returns the required positional argument names a Use string
// declares, normalised the way cobrax normalises them.
func declaredArgs(use string) []string {
	if i := strings.IndexByte(use, ' '); i != -1 {
		use = use[i+1:]
	} else {
		return nil
	}
	use = strings.ReplaceAll(use, "[flags]", "")

	var out []string
	for _, m := range argTokenRe.FindAllStringSubmatch(use, -1) {
		if m[1] != "<" {
			continue
		}
		out = append(out, strings.ToLower(m[2]))
	}
	return out
}

func propertyNames(props map[string]any) []string {
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
