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

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// TestCatalog_GroupKeysAreStable pins the group keys.
//
// A key is written into the database (inside a command_selector) and carried by
// every card button ("nav:group:<key>"), so renaming one silently strands the
// cards already sent — their callbacks resolve a key that no longer exists. The
// human-readable side, Message and Label, may change freely; this is the test
// that fails when someone changes the wrong half.
func TestCatalog_GroupKeysAreStable(t *testing.T) {
	want := []string{
		"core", "duty", "ai", "msg", "job",
		"builtin-agent", "skill", "custom", "resource", "platform",
	}

	got := GroupKeys()
	if len(got) != len(want) {
		t.Fatalf("group keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group key %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCatalog_GroupKeysMatchChatConstants pins that the keys the command
// constructors set on themselves still agree with the catalog.
//
// The two cannot be shared: the catalog imports known/chat for the annotation
// keys, so known/chat cannot import the catalog back. That makes the values a
// duplicated literal, and a duplicated literal that drifts is precisely the
// failure this whole catalog exists to remove — the card group and the text
// help group had already diverged once. ChatGroupCronJob has no counterpart on
// purpose: the cronjob commands are catalogued in the job group.
func TestCatalog_GroupKeysMatchChatConstants(t *testing.T) {
	pairs := map[string]string{
		GroupCore:         chat.ChatGroupCore,
		GroupDuty:         chat.ChatGroupDuty,
		GroupAI:           chat.ChatGroupAI,
		GroupMessaging:    chat.ChatGroupMessaging,
		GroupJob:          chat.ChatGroupJob,
		GroupBuiltinAgent: chat.ChatGroupBuiltinAgent,
		GroupSkill:        chat.ChatGroupSkill,
		GroupResource:     chat.ChatGroupResource,
		GroupPlatform:     chat.ChatGroupPlatform,
	}

	for key, constant := range pairs {
		if key != constant {
			t.Errorf("catalog key %q != chat constant %q", key, constant)
		}
	}
	if _, ok := GroupByKey(chat.ChatGroupCronJob); ok {
		t.Error("cronjob is a group of its own, but its commands belong to the job group")
	}
}

// TestCatalog_EntriesMatchTheirCommands builds every entry and checks it is the
// command it claims to be.
//
// The name is declared in the catalog so a tree can be assembled without
// building commands that are not selected, which is why it can drift from the
// cobra Use line. It is also the value a binding writes, so a drift would make
// a grant silently name nothing. Building here is what a per-command test
// cannot do: a command that is catalogued nowhere passes its own tests while
// being unreachable from every surface.
func TestCatalog_EntriesMatchTheirCommands(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	sr := &runtime.StaticRuntime{}

	seen := make(map[string]string)
	for _, g := range Groups() {
		if g.Key == "" {
			t.Errorf("group %q has no key", g.Message)
		}
		if g.Message == "" || g.Label == "" {
			t.Errorf("group %q is missing its heading or label", g.Key)
		}

		for _, e := range g.Entries {
			if e.New == nil {
				t.Errorf("group %q: command %q has no constructor", g.Key, e.Name)
				continue
			}
			if prev, dup := seen[e.Name]; dup {
				t.Errorf("command %q is listed in both group %q and %q", e.Name, prev, g.Key)
			}
			seen[e.Name] = g.Key

			cmd := e.New(sr, out)
			if got := cmd.Name(); got != e.Name {
				t.Errorf("group %q: entry %q builds the command %q", g.Key, e.Name, got)
			}
			// The declared Short is what the console shows beside the name;
			// a drift here would advertise the wrong thing without failing any
			// command's own test.
			if strings.TrimSpace(e.Short) == "" {
				t.Errorf("group %q: command %q has no Short", g.Key, e.Name)
			} else if cmd.Short != e.Short {
				t.Errorf("group %q: entry %q declares Short %q, built command says %q",
					g.Key, e.Name, e.Short, cmd.Short)
			}
		}
	}
}

// TestCatalog_LabelsCarryTheGroup pins the one label every command is
// guaranteed to have.
//
// It is injected from the group the command is listed in, and it is what a
// binding selects a whole group with (command.LabelGroup+" in (duty,ai)"). A command that
// carried a group of its own choosing would make the group it is listed in and
// the group it is selectable by two different answers.
func TestCatalog_LabelsCarryTheGroup(t *testing.T) {
	all := AllLabels()

	for _, g := range Groups() {
		for _, e := range g.Entries {
			ls, ok := all[e.Name]
			if !ok {
				t.Errorf("command %q has no labels", e.Name)
				continue
			}
			if got := ls[LabelGroup]; got != g.Key {
				t.Errorf("command %q: %s = %q, want %q", e.Name, LabelGroup, got, g.Key)
			}
		}
	}

	// The two views of the catalog must agree: a name that is selectable but
	// not listed, or listed but not selectable, is a grant that resolves to
	// nothing.
	names := CommandNames()
	for name := range all {
		if _, ok := names[name]; !ok {
			t.Errorf("command %q has labels but is not a known command name", name)
		}
	}
	for name := range names {
		if _, ok := all[name]; !ok {
			t.Errorf("command %q is a known name but has no labels", name)
		}
	}
}

// TestCatalog_GroupByKey pins the lookup a binding validator uses: a key that
// is not in the catalog must not resolve, or a selector naming a group that was
// renamed would be accepted and grant nothing.
func TestCatalog_GroupByKey(t *testing.T) {
	g, ok := GroupByKey(GroupDuty)
	if !ok {
		t.Fatal("GroupByKey(duty) did not resolve")
	}
	if g.Key != GroupDuty {
		t.Errorf("GroupByKey(duty).Key = %q", g.Key)
	}

	if _, ok := GroupByKey("no-such-group"); ok {
		t.Error("GroupByKey resolved a group that does not exist")
	}
}
