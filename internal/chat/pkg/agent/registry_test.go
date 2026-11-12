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

// External test package: this file also compares the backend names against
// pkg/options, and pkg/options reaches back into internal/chat/pkg/agent through
// agentcmd → runtime. Staying in `package agent` made that a compile-time cycle.
package agent_test

import (
	"context"
	"errors"
	"testing"

	customoptions "github.com/onexstack/onexai/pkg/options"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
)

// stubAgent is a backend that does nothing, so the registry can be exercised
// without one: it exists to be found by name.
type stubAgent struct{ name string }

func (s *stubAgent) Name() string { return s.name }
func (s *stubAgent) Session(context.Context, string) (agent.Session, error) {
	return nil, errors.New("stubAgent cannot serve a conversation")
}
func (s *stubAgent) CloseSession(context.Context, string) error { return nil }
func (s *stubAgent) Close() error                               { return nil }

// TestResolveBackendCLIPrefix pins that an explicit choice wins. A user who asks
// for the CLI backend on a conversation that is already bound to the LLM one must
// get the CLI one: the binding records where the conversation has been, not a
// veto over where it goes next.
func TestResolveBackendCLIPrefix(t *testing.T) {
	if got := agent.ResolveBackend(agent.BackendCLI, agent.BackendLLM, agent.BackendLLM); got != agent.BackendCLI {
		t.Errorf("ResolveBackend(cli, llm, llm) = %q, want %q", got, agent.BackendCLI)
	}
}

// TestResolveBackendSessionState pins the middle term: a conversation that has
// already been bound keeps its backend even when the deployment default changes
// under it. Reading the default first would move a live conversation's thread —
// a mid-sentence switch is how a user loses a conversation they were having.
func TestResolveBackendSessionState(t *testing.T) {
	if got := agent.ResolveBackend("", agent.BackendCLI, agent.BackendLLM); got != agent.BackendCLI {
		t.Errorf("ResolveBackend(\"\", cli, llm) = %q, want the bound %q", got, agent.BackendCLI)
	}
}

// TestResolveBackendDefault covers what a brand-new conversation gets.
func TestResolveBackendDefault(t *testing.T) {
	t.Run("the deployment default when nothing is bound", func(t *testing.T) {
		if got := agent.ResolveBackend("", "", agent.BackendCLI); got != agent.BackendCLI {
			t.Errorf("ResolveBackend(\"\", \"\", cli) = %q, want %q", got, agent.BackendCLI)
		}
	})

	t.Run("the llm backend when nothing is configured at all", func(t *testing.T) {
		if got := agent.ResolveBackend("", "", ""); got != agent.BackendLLM {
			t.Errorf("ResolveBackend(\"\", \"\", \"\") = %q, want %q", got, agent.BackendLLM)
		}
	})

	t.Run("an unknown name is not silently replaced", func(t *testing.T) {
		// Resolving a name and checking it exists are separate steps, so what a
		// user typed comes back verbatim and the caller reports it. Quietly
		// substituting the default would answer on a backend they did not choose.
		if got := agent.ResolveBackend("gpt-from-2019", "", agent.BackendLLM); got != "gpt-from-2019" {
			t.Errorf("ResolveBackend = %q, want the requested name unchanged", got)
		}
	})
}

// TestRegistryGetAndFallback pins the lookup the turn path depends on: the
// resolved name reaches a backend, and a registry built with no fallback still
// names one.
func TestRegistryGetAndFallback(t *testing.T) {
	r := agent.NewRegistry(
		agent.BackendCLI,
		&stubAgent{name: agent.BackendCLI},
		&stubAgent{name: agent.BackendLLM},
	)

	if got := r.Fallback(); got != agent.BackendCLI {
		t.Errorf("Fallback() = %q, want %q", got, agent.BackendCLI)
	}
	if _, ok := r.Get(agent.BackendCLI); !ok {
		t.Errorf("Get(%q) found nothing", agent.BackendCLI)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(\"nope\") found a backend that was never registered")
	}
	if got := len(r.Names()); got != 2 {
		t.Errorf("Names() = %d entries, want 2", got)
	}

	t.Run("an empty fallback means llm", func(t *testing.T) {
		if got := agent.NewRegistry("").Fallback(); got != agent.BackendLLM {
			t.Errorf("Fallback() = %q, want %q", got, agent.BackendLLM)
		}
	})

	t.Run("a nil registry answers without panicking", func(t *testing.T) {
		// The runtime may be built without agents at all; every path that reads it
		// has to survive that rather than take the process down at startup.
		var nilReg *agent.Registry
		if _, ok := nilReg.Get(agent.BackendLLM); ok {
			t.Error("a nil registry found a backend")
		}
		if got := nilReg.Fallback(); got != agent.BackendLLM {
			t.Errorf("Fallback() = %q, want %q", got, agent.BackendLLM)
		}
		if got := nilReg.Names(); got != nil {
			t.Errorf("Names() = %v, want nil", got)
		}
	})

	t.Run("a nil backend is not registered", func(t *testing.T) {
		// Registering a nil Agent would make Get succeed and the turn panic on the
		// first call through it.
		r := agent.NewRegistry(agent.BackendLLM, nil)
		if _, ok := r.Get(agent.BackendLLM); ok {
			t.Error("a nil backend was registered")
		}
	})
}

// TestBackendNamesAgreeAcrossPackages is the guard against the one cost of
// duplicating these constants: values travel between the packages at runtime, so
// a copy that drifted would not fail to compile — it would resolve a
// conversation to a backend nobody registered, and the lookup would miss.
//
// The three copies are options (the canonical, configurable one), session (what
// is written into the conversation snapshot) and this package (what the port
// names its backends).
func TestBackendNamesAgreeAcrossPackages(t *testing.T) {
	pairs := []struct {
		what      string
		here      string
		session   string
		canonical string
	}{
		{"llm", agent.BackendLLM, session.BackendLLM, customoptions.AgentBackendLLM},
		{"cli", agent.BackendCLI, session.BackendCLI, customoptions.AgentBackendCLI},
	}
	for _, p := range pairs {
		if p.here != p.session {
			t.Errorf("agent.Backend%s = %q but session.Backend%s = %q",
				p.what, p.here, p.what, p.session)
		}
		if p.here != p.canonical {
			t.Errorf("agent.Backend%s = %q but options.AgentBackend%s = %q",
				p.what, p.here, p.what, p.canonical)
		}
	}
}
