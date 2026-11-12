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

package command

import (
	"strings"
	"testing"
)

// TestTopLevelCommand pins the map from an MCP tool name back to the catalogued
// command that backs it, which is the thing a rule grants.
//
// It is what lets the MCP tool filter reuse the same selection the slash-command
// menu does, so a tool name that mapped to the wrong command — or to a command
// that does not exist — would filter the tool list by a rule nobody wrote.
func TestTopLevelCommand(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want string
		ok   bool
	}{
		// A leaf whose path is one segment: the tool name is the command itself.
		{tool: "duty", want: "duty", ok: true},
		// A subcommand: the tool name is the path, the grant is the top level.
		{tool: "duty_list", want: "duty", ok: true},
		{tool: "job_create", want: "job", ok: true},
		// The root's own additions are in no rule, so nothing backs them and the
		// caller leaves them ungated — mirroring ValidateGranted.
		{tool: "askai-mcp", ok: false},
		{tool: "definitely-not-a-command", ok: false},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			got, ok := TopLevelCommand(tc.tool)
			if ok != tc.ok || got != tc.want {
				t.Errorf("TopLevelCommand(%q) = (%q, %v), want (%q, %v)", tc.tool, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestTopLevelCommandFeedsTheSelection closes the loop the filter relies on: the
// name TopLevelCommand answers with is one CommandSelection.Allows understands.
func TestTopLevelCommandFeedsTheSelection(t *testing.T) {
	sel := granted("duty")

	top, ok := TopLevelCommand("duty_list")
	if !ok {
		t.Fatalf("TopLevelCommand(%q) did not resolve a catalogued command", "duty_list")
	}
	if !sel.Allows(top) {
		t.Errorf("a granted command's tool was not allowed: top=%q", top)
	}

	top, ok = TopLevelCommand("job_create")
	if !ok {
		t.Fatalf("TopLevelCommand(%q) did not resolve a catalogued command", "job_create")
	}
	if sel.Allows(top) {
		t.Errorf("an ungranted command's tool was allowed: top=%q", top)
	}
}

// TestCatalog_NamesHaveNoUnderscore guards TopLevelCommand's mapping. A tool name
// is a command path with the root dropped and spaces turned into underscores, and
// the mapping recovers the top-level command by cutting at the first underscore.
// That is exact only while no catalogued command name holds an underscore; if one
// ever does, this fails and the mapping must be replaced with a precise one from
// the bridge rather than guessed at.
func TestCatalog_NamesHaveNoUnderscore(t *testing.T) {
	for name := range CommandNames() {
		if strings.Contains(name, "_") {
			t.Errorf("catalogued command %q holds an underscore; TopLevelCommand's mapping is no longer exact", name)
		}
	}
}
