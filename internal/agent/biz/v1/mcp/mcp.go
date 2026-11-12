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

// Package mcp implements the MCP server business logic.
package mcp

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errmsg"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// testProbeTimeout bounds one on-demand handshake and tool listing.
//
// The MCP client's own discovery timeout covers the initialize call; tool
// listing runs on the parent context, so the outer bound is what keeps a
// server that accepts the connection then stalls from holding the HTTP request
// open. It is shorter than the console's 30s request timeout on purpose.
const testProbeTimeout = 20 * time.Second

// ServerProber dials one server and reports the tools it publishes. It is the
// narrow slice of *mcpclient.Manager this package needs.
type ServerProber interface {
	Probe(ctx context.Context, server *model.MCPM) ([]mcpclient.ProbedTool, error)
}

// MCPBiz is the MCP server business logic.
type MCPBiz interface {
	Create(ctx context.Context, req *v1.CreateMCPRequest) (*v1.CreateMCPResponse, error)
	Get(ctx context.Context, req *v1.GetMCPRequest) (*v1.GetMCPResponse, error)
	List(ctx context.Context, req *v1.ListMCPRequest) (*v1.ListMCPResponse, error)
	Update(ctx context.Context, req *v1.UpdateMCPRequest) (*v1.UpdateMCPResponse, error)
	Delete(ctx context.Context, req *v1.DeleteMCPRequest) (*v1.DeleteMCPResponse, error)
	Test(ctx context.Context, req *v1.TestMCPRequest) (*v1.TestMCPResponse, error)
}

type mcpBiz struct {
	store  agentstore.AIStore
	prober ServerProber
}

var _ MCPBiz = (*mcpBiz)(nil)

// New creates an MCPBiz. prober may be nil when the deployment has no MCP
// runtime; Test then answers ok=false instead of panicking.
func New(store agentstore.AIStore, prober ServerProber) *mcpBiz {
	return &mcpBiz{store: store, prober: prober}
}

// toolCacheInvalidator is the part of the production prober that drops cached
// tool sets. It is separate from ServerProber so the fakes stay small.
type toolCacheInvalidator interface {
	Invalidate()
}

// invalidateTools makes the next run re-discover this server's tools after its
// row changed.
func (b *mcpBiz) invalidateTools() {
	if inv, ok := b.prober.(toolCacheInvalidator); ok {
		inv.Invalidate()
	}
}

// Create implements MCPBiz.
func (b *mcpBiz) Create(ctx context.Context, req *v1.CreateMCPRequest) (*v1.CreateMCPResponse, error) {
	row := conversion.MCPV1ToMCPModel(req.GetMcp())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.MCPs().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.CreateMCPResponse{Mcp: conversion.MCPModelToMCPV1(row)}, nil
}

// Get implements MCPBiz.
func (b *mcpBiz) Get(ctx context.Context, req *v1.GetMCPRequest) (*v1.GetMCPResponse, error) {
	row, err := b.store.MCPs().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "mcp %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetMCPResponse{Mcp: conversion.MCPModelToMCPV1(row)}, nil
}

// List implements MCPBiz.
func (b *mcpBiz) List(ctx context.Context, req *v1.ListMCPRequest) (*v1.ListMCPResponse, error) {
	total, list, err := b.store.MCPs().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "mcp") {
		before := len(list)
		list = authzview.Filter(ctx, "mcp", list, func(row *model.MCPM) (string, string, string) {
			return row.Namespace, row.Name, ""
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	mcps := make([]*v1.MCP, 0, len(list))
	for _, row := range list {
		mcps = append(mcps, conversion.MCPModelToMCPV1(row))
	}
	return &v1.ListMCPResponse{Mcps: mcps, Count: total}, nil
}

// Update implements MCPBiz.
func (b *mcpBiz) Update(ctx context.Context, req *v1.UpdateMCPRequest) (*v1.UpdateMCPResponse, error) {
	row := conversion.MCPV1ToMCPModel(req.GetMcp())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.MCPs().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.UpdateMCPResponse{Mcp: conversion.MCPModelToMCPV1(row)}, nil
}

// Delete implements MCPBiz.
func (b *mcpBiz) Delete(ctx context.Context, req *v1.DeleteMCPRequest) (*v1.DeleteMCPResponse, error) {
	if err := b.store.MCPs().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.DeleteMCPResponse{}, nil
}

// Test implements MCPBiz.
//
// The inline server is the whole request: the form reads the row back (headers
// included, from the Secret the store owns) and submits the values the user is
// looking at. Filling gaps from the stored row would turn "the user cleared the
// URL" into a passing test against the old value, so no merge happens here.
func (b *mcpBiz) Test(ctx context.Context, req *v1.TestMCPRequest) (*v1.TestMCPResponse, error) {
	row := conversion.MCPV1ToMCPModel(req.GetMcp())
	if row.URL == "" {
		return &v1.TestMCPResponse{Ok: false, Message: "url is required"}, nil
	}
	if b.prober == nil {
		return &v1.TestMCPResponse{Ok: false, Message: "MCP runtime is not enabled"}, nil
	}

	// A connectivity test is about the endpoint, not the enable switch. Probe
	// refuses disabled servers, but hiding the answer behind the switch would
	// leave the user unable to tell a typo in the URL from the form's own state.
	row.Status = model.StatusEnabled

	probeCtx, cancel := context.WithTimeout(ctx, testProbeTimeout)
	defer cancel()

	start := time.Now()
	probed, err := b.prober.Probe(probeCtx, row)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &v1.TestMCPResponse{Ok: false, Message: errmsg.Text(err), LatencyMs: latency}, nil
	}

	tools := make([]*v1.MCPProbedTool, 0, len(probed))
	for _, tool := range probed {
		tools = append(tools, &v1.MCPProbedTool{Name: tool.Name, Description: tool.Description})
	}
	return &v1.TestMCPResponse{Ok: true, Tools: tools, LatencyMs: latency}, nil
}
