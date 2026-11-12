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

package mcpclient

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/model"
)

// TestProbe_ReportsTheHandshakeAndHangsUp pins what a probe is for: the tools a
// server publishes, and a connection that does not outlive the check.
func TestProbe_ReportsTheHandshakeAndHangsUp(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["up"] = []tool.BaseTool{
		&recordedTool{wire: "up.ping"},
		&recordedTool{wire: "up.list"},
	}
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("up")}},
		Options{Enabled: true, Dialer: dialer})

	tools, err := m.Probe(context.Background(), server("up"))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("Probe returned %d tools, want 2", len(tools))
	}
	if tools[0].Name != "up.ping" || tools[0].Description != "desc of up.ping" {
		t.Errorf("first tool = %+v, want up.ping with its description", tools[0])
	}

	if len(dialer.conns) != 1 {
		t.Fatalf("the probe dialed %d times, want 1", len(dialer.conns))
	}
	if !dialer.conns[0].isClosed() {
		t.Error("the probe left its connection open")
	}
	if got := dialer.agents[0]; len(got) != 0 {
		t.Errorf("the probe dialed with agents %v, want none", got)
	}
}

// TestProbe_ReportsTheFailure pins the difference from Tools: a server that does
// not answer is an error here, not a silently smaller tool set.
func TestProbe_ReportsTheFailure(t *testing.T) {
	dialer := newFakeDialer()
	dialer.errors["down"] = errors.New("connection refused")
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("down")}},
		Options{Enabled: true, Dialer: dialer})

	tools, err := m.Probe(context.Background(), server("down"))
	if err == nil {
		t.Fatalf("Probe succeeded with %d tools; a refused dial must be an error", len(tools))
	}
	if !errors.Is(err, dialer.errors["down"]) {
		t.Errorf("error = %v, want the dialer's error", err)
	}
}

// TestProbe_RefusesADisabledServer pins that a probe reports the state a run
// would see: a disabled server contributes no tools, and dialing it would be
// reporting on something nothing is using.
func TestProbe_RefusesADisabledServer(t *testing.T) {
	dialer := newFakeDialer()
	disabled := server("off")
	disabled.Status = model.StatusDisabled
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{disabled}},
		Options{Enabled: true, Dialer: dialer})

	if _, err := m.Probe(context.Background(), disabled); err == nil {
		t.Fatal("probing a disabled server must report an error")
	}
	if dialer.dialCount() != 0 {
		t.Errorf("a disabled server was dialed %d times", dialer.dialCount())
	}
}

// TestProbe_LeavesTheToolSetCacheAlone pins the boundary between a check and a
// run: probing must not create, extend or evict a cached tool set.
func TestProbe_LeavesTheToolSetCacheAlone(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["up"] = []tool.BaseTool{&recordedTool{wire: "up.ping"}}
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("up")}},
		Options{Enabled: true, Dialer: dialer})

	if _, err := m.Probe(context.Background(), server("up")); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	m.mu.Lock()
	cached := len(m.entries)
	m.mu.Unlock()
	if cached != 0 {
		t.Errorf("the probe installed %d cache entries; a health check is not a run", cached)
	}
}
