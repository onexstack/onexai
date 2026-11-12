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

package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// fakeGrant is a stand-in for the store-backed resolver: it covers one named
// agent, grants nothing to anybody else, and records what it was asked.
//
// The "grants nothing" default is the contract every implementation owes, and
// it is the one these two tests are about: a conversation whose agents nobody
// wrote a rule for has no commands.
type fakeGrant struct {
	agent     string
	selection runtime.CommandSelection
	asked     [][]string
}

func (f *fakeGrant) Resolve(_ context.Context, agents []string) runtime.CommandSelection {
	f.asked = append(f.asked, agents)
	for _, a := range agents {
		if a == f.agent {
			return f.selection
		}
	}
	return runtime.CommandSelection{}
}

// grantedOnly builds a selection admitting exactly the named commands.
func grantedOnly(names ...string) runtime.CommandSelection {
	sel := runtime.CommandSelection{Names: make(map[string]struct{}, len(names))}
	for _, n := range names {
		sel.Names[n] = struct{}{}
	}
	return sel
}

// declaring builds a bot whose session store answers every lookup with a
// conversation that declares the given reference list.
//
// The resolved agents are no longer a column, so a test that wants a
// conversation "running as" something states the declaration and the configured
// default that declaration resolves to — which is the whole chain the scheduler
// runs, rather than a value the test wrote into the row.
func declaring(refs []string) *fakeStore {
	return &fakeStore{session: &fakeSessionStore{
		getFn: func(context.Context, *where.Options) (*model.SessionM, error) {
			return &model.SessionM{
				SessionID:   "oc_bound",
				SessionType: model.SessionTypePrivateChat,
				Refs:        model.EncodeNameList(refs),
			}, nil
		},
	}}
}

// TestExecuteAPICommand_WhatRunsFollowsTheConversation pins the contract the
// REST surface rests on now that there is no fail-open default: a request that
// names its conversation is answered with the grants of the agents that
// conversation resolves to for **this** caller, and a request that names none is
// answered with nothing.
//
// That second half is not hypothetical — it is POST /v1/cli/:cmdname, which
// passes an empty conversation on purpose. It used to run every command; it now
// runs none, and the refusal is an unknown command rather than a permission
// error because the command was never built.
func TestExecuteAPICommand_WhatRunsFollowsTheConversation(t *testing.T) {
	// --mock makes the command a no-op, so the dispatch is exercised without a
	// cluster behind it. The gates run before the flag is read, which is what
	// makes this a test of the tree rather than of the mock.
	run := func(b *botBiz, convID, cmd string) *runtime.CommandResult {
		t.Helper()
		return b.ExecuteAPICommand(context.Background(), convID, cmd, "user-1", true)
	}

	res := &fakeGrant{agent: "bke-agent", selection: grantedOnly("pod")}
	// A REST caller carries no platform handle, so `@peer` cannot resolve for it
	// — there is no chat identity for a binding to name. `@default` is what such
	// a conversation runs on, which is the audience default.
	bound := &botBiz{sr: &runtime.StaticRuntime{
		AgentCommands: res, InternalDefaultAgent: "bke-agent", Store: declaring([]string{"@default"}),
	}}
	nameless := &botBiz{sr: &runtime.StaticRuntime{AgentCommands: res}}

	// The conversation's agent was granted pod, so pod is a command here. The
	// positional argument is pod's own, and it is checked before the mock flag
	// is read — the flag replaces what the command does, not what it takes.
	if got := run(bound, "oc_bound", "/pod some-pod --mock success"); got.Err != "" {
		t.Errorf("a granted command was refused: %q", got.Err)
	}
	// ...and nothing else is, even though the same deployment runs both.
	if got := run(bound, "oc_bound", "/job --mock success"); !strings.Contains(got.Err, "unknown command") {
		t.Errorf("an ungranted command = %q, want the tree to lack it", got.Err)
	}
	// A caller that names no conversation names no agent, so it is granted
	// nothing — the whole point of removing the fail-open default.
	if got := run(nameless, "", "/pod some-pod --mock success"); !strings.Contains(got.Err, "unknown command") {
		t.Errorf("a nameless conversation ran a command: %q", got.Err)
	}

	// The grants follow what the conversation resolves to, and a nameless request
	// asks about no agent rather than being filtered some other way. One ask per
	// run: the tree is assembled from a selection, and the gate that runs before
	// the command checks that same selection rather than resolving it again — the
	// second answer was always the first one, and it was paid for on the path a
	// person waits on.
	want := [][]string{
		{"bke-agent"}, // the granted command, assembled (and gated from the same answer)
		{"bke-agent"}, // the ungranted one, assembled (and then not found)
		{},            // the nameless conversation, which names no agent
	}
	if len(res.asked) != len(want) {
		t.Fatalf("the resolver was asked %v, want %v", res.asked, want)
	}
	for i := range want {
		if strings.Join(res.asked[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("resolve %d asked about %v, want %v", i, res.asked[i], want[i])
		}
	}
}

// TestCommandTree_IsTheCatalogNotAMenu pins the TUI's completion tree.
//
// It is fetched once at startup and carries no conversation, so it has no agent
// to filter by: narrowing it by grants would not narrow an answer, it would
// delete one — the menu would be empty for everybody, including the agents the
// rules do cover. What a TUI request may actually run is decided where it runs,
// on the conversation's WebSocket, which does carry the conversation.
func TestCommandTree_IsTheCatalogNotAMenu(t *testing.T) {
	stingy := &fakeGrant{agent: "somebody-else", selection: grantedOnly("doctor")}
	b := &botBiz{sr: &runtime.StaticRuntime{AgentCommands: stingy}}

	nodes := b.CommandTree(context.Background())

	if !hasCommand(nodes, "pod") {
		t.Error("the TUI tree is missing a catalogued command; a menu was built where a catalog was wanted")
	}
	if len(stingy.asked) != 0 {
		t.Errorf("the catalog tree asked the resolver about %v; it has no agent to ask about", stingy.asked)
	}
}

// hasCommand reports whether the serialized tree holds a command by name.
func hasCommand(nodes []*v1.CommandNode, name string) bool {
	for _, n := range nodes {
		if n.GetName() == name {
			return true
		}
		if hasCommand(n.GetSubcommands(), name) {
			return true
		}
	}
	return false
}
