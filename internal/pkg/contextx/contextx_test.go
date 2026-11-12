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

package contextx

import (
	"context"
	"reflect"
	"testing"
)

// TestToolTimeoutRoundTrips pins the marker that lets a timeout be attributed
// to the tool's own deadline rather than to the run's. A bare context is not
// marked: "no marker" is the answer for every caller that did not install one,
// and treating it as marked would blame MCP servers for runs ending.
func TestToolTimeoutRoundTrips(t *testing.T) {
	if IsToolTimeout(context.Background()) {
		t.Fatal("a bare context reads as a tool timeout")
	}
	if !IsToolTimeout(WithToolTimeout(context.Background())) {
		t.Fatal("a marked context does not read as a tool timeout")
	}
}

// TestAgentNamesRoundTrips pins the carrier the MCP tool filter reads: the agents
// a call runs as travel on the context, and none is the answer for a context that
// never carried one.
func TestAgentNamesRoundTrips(t *testing.T) {
	if got := AgentNames(context.Background()); got != nil {
		t.Errorf("AgentNames on a bare context = %v, want nil", got)
	}

	ctx := WithAgentNames(context.Background(), []string{"duty-agent", "ops-agent"})
	if got := AgentNames(ctx); !reflect.DeepEqual(got, []string{"duty-agent", "ops-agent"}) {
		t.Errorf("AgentNames = %v, want [duty-agent ops-agent]", got)
	}
}

// TestWithAgentNamesEmptyIsNoOp pins that naming no agent leaves the context as it
// was: a caller with no agent context must not gain one, and a run that selected
// nothing is in this respect the same caller as one that never said.
func TestWithAgentNamesEmptyIsNoOp(t *testing.T) {
	base := context.Background()
	for _, agents := range [][]string{nil, {}} {
		if got := WithAgentNames(base, agents); got != base {
			t.Errorf("WithAgentNames(%v) returned a new context, want the one passed in", agents)
		}
	}
}

// TestAgentNamesIsACopy pins that neither the caller that set the names nor one
// that read them can reach into the stored slice: the value is the context's, not
// either caller's.
func TestAgentNamesIsACopy(t *testing.T) {
	in := []string{"a", "b"}
	ctx := WithAgentNames(context.Background(), in)
	in[0] = "mutated"

	got := AgentNames(ctx)
	if got[0] != "a" {
		t.Fatalf("AgentNames = %v; a later write to the input reached the context", got)
	}
	got[0] = "mutated-again"
	if again := AgentNames(ctx); again[0] != "a" {
		t.Fatalf("AgentNames = %v; a write to the returned slice reached the context", again)
	}
}
