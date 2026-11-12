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

package di

import (
	"context"
	"fmt"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/controlplane"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
)

// ProvideHealthProbers adapts the runtime's subsystems to the reporter's probe
// interfaces.
//
// The adapters resolve a resource by name through the same store a run reads, so
// a probe exercises the identical row — the header Secret an MCP server names,
// the credential a skill's params point at. A probe that resolved resources its
// own way would be reporting on a copy.
//
// An adapter exists only when its subsystem does: a deployment with MCP off has
// nothing to say about MCP servers, and the reporter then leaves those resources'
// conditions to the controller rather than publishing a failure it invented.
func ProvideHealthProbers(
	resources store.AIStore, mcp *mcpclient.Manager, skills *skill.Manager, kbs *kbToolSource,
) controlplane.Probers {
	probers := controlplane.Probers{}
	if resources == nil {
		return probers
	}
	if mcp != nil {
		probers.MCP = &mcpProber{resources: resources, manager: mcp}
	}
	if skills != nil {
		probers.Skills = &skillProber{resources: resources, manager: skills}
	}
	if kbs != nil {
		probers.KnowledgeBases = &kbProber{resources: resources, source: kbs}
	}
	return probers
}

// mcpProber performs the handshake through the process's one MCP manager.
type mcpProber struct {
	resources store.AIStore
	manager   *mcpclient.Manager
}

func (p *mcpProber) Probe(ctx context.Context, name string) ([]controlplane.ProbeTool, error) {
	server, err := p.resources.MCPs().Get(ctx, model.EntryFilter(name))
	if err != nil {
		return nil, fmt.Errorf("reading MCP server %q: %w", name, err)
	}
	if server == nil {
		// The store answers nil, not an error, for a name it does not hold — which
		// is what a resource outside the runtime's namespace looks like.
		return nil, fmt.Errorf("MCP server %q is not in the runtime's resource store", name)
	}
	tools, err := p.manager.Probe(ctx, server)
	if err != nil {
		return nil, err
	}
	out := make([]controlplane.ProbeTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, controlplane.ProbeTool{Name: tool.Name, Description: tool.Description})
	}
	return out, nil
}

// skillProber resolves a skill's source through the process's one skill manager.
type skillProber struct {
	resources store.AIStore
	manager   *skill.Manager
}

func (p *skillProber) Probe(ctx context.Context, name string) (int, error) {
	record, err := p.resources.Skills().Get(ctx, model.EntryFilter(name))
	if err != nil {
		return 0, fmt.Errorf("reading skill %q: %w", name, err)
	}
	if record == nil {
		return 0, fmt.Errorf("skill %q is not in the runtime's resource store", name)
	}
	return p.manager.Probe(ctx, *record)
}

// kbProber asks a retrieval platform about one knowledge base.
type kbProber struct {
	resources store.AIStore
	source    *kbToolSource
}

func (p *kbProber) Probe(ctx context.Context, name string) (int, error) {
	record, err := p.resources.KnowledgeBases().Get(ctx, model.EntryFilter(name))
	if err != nil {
		return 0, fmt.Errorf("reading knowledge base %q: %w", name, err)
	}
	if record == nil {
		return 0, fmt.Errorf("knowledge base %q is not in the runtime's resource store", name)
	}
	return p.source.Probe(ctx, record)
}

// Compile-time proof that each adapter implements the interface the reporter
// asks for: a signature drift here would otherwise surface as a missing prober.
var (
	_ controlplane.MCPServerProber     = (*mcpProber)(nil)
	_ controlplane.SkillProber         = (*skillProber)(nil)
	_ controlplane.KnowledgeBaseProber = (*kbProber)(nil)
)
