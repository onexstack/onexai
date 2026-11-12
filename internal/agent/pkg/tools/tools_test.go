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

package tools

import (
	"sort"
	"testing"

	"github.com/onexstack/onexai/internal/agent/store"
)

// TestBuiltinsAreOnlyBuiltins is the guard against MCP tools leaking into runs
// that selected none: the built-in set is fixed, and there is no allowlist
// variant whose empty value would mean "everything".
func TestBuiltinsAreOnlyBuiltins(t *testing.T) {
	// A resource store is configured, so the resource-backed built-in
	// (list_agent) is registered too.
	reg := NewDefaultRegistry(store.NewEmptyStore())

	var names []string
	for _, tl := range reg.Builtins() {
		info, err := tl.Info(t.Context())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		names = append(names, info.Name)
	}
	sort.Strings(names)

	want := []string{"ask_user_question", "current_time", "list_agent"}
	if len(names) != len(want) {
		t.Fatalf("Builtins() = %v, want exactly %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Builtins() = %v, want %v", names, want)
		}
	}
}

func TestBuiltinsWithoutStore(t *testing.T) {
	var names []string
	for _, tl := range NewDefaultRegistry(nil).Builtins() {
		info, err := tl.Info(t.Context())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		names = append(names, info.Name)
	}

	// list_agent needs the resource store, so it is absent without one.
	if len(names) != 2 {
		t.Fatalf("Builtins() = %v, want the two store-independent tools", names)
	}
}

func TestNamesMatchesRegistration(t *testing.T) {
	reg := NewDefaultRegistry(store.NewEmptyStore())
	if got := len(reg.Names()); got != 3 {
		t.Fatalf("Names() returned %d names, want 3", got)
	}
	if got := len(reg.Builtins()); got != len(reg.Names()) {
		t.Fatalf("Builtins() returned %d tools but Names() %d names", len(reg.Builtins()), len(reg.Names()))
	}
}
