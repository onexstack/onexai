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
	"fmt"

	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/model"
)

// ProbedTool is one tool a probed server publishes.
type ProbedTool struct {
	Name        string
	Description string
}

// Probe dials one server, lists the tools it publishes, and disconnects.
//
// Tools cannot answer this question. Discovery deliberately skips a server that
// fails so one bad row cannot break a run — which makes a dead server look
// exactly like a server with no tools. A health check has the opposite job: the
// failure is the answer, so the probe dials and returns the error.
//
// It is also deliberately outside the tool-set cache. A probe is not a run:
// sharing the cache would let a health check evict a set that runs are using, or
// extend the life of one that has gone stale.
func (m *Manager) Probe(ctx context.Context, server *model.MCPM) ([]ProbedTool, error) {
	if m == nil {
		return nil, fmt.Errorf("mcpclient: no manager")
	}
	if server == nil {
		return nil, fmt.Errorf("mcpclient: no server to probe")
	}
	if !server.Enabled() {
		return nil, fmt.Errorf("mcpclient: server %q is disabled", server.Name)
	}

	// No agents: this is the tool set a client that names no agent sees, which
	// is all a probe can honestly ask about.
	conn, tools, err := m.dialer.Dial(ctx, server, nil, m.opts.DiscoveryTimeout)
	if conn != nil {
		// A dial can open a stream and then fail discovery, so closing is not
		// conditional on err being nil.
		defer func() { _ = conn.Close() }()
	}
	if err != nil {
		return nil, err
	}
	return describeTools(ctx, tools)
}

// describeTools reads the name and description of each discovered tool.
func describeTools(ctx context.Context, tools []tool.BaseTool) ([]ProbedTool, error) {
	out := make([]ProbedTool, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("mcpclient: reading a discovered tool's info: %w", err)
		}
		if info == nil {
			continue
		}
		out = append(out, ProbedTool{Name: info.Name, Description: info.Desc})
	}
	return out, nil
}
