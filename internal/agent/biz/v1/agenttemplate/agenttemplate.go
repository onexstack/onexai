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

// Package agenttemplate implements the AgentTemplate business logic.
package agenttemplate

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// AgentTemplateBiz is the AgentTemplate business logic.
type AgentTemplateBiz interface {
	Create(ctx context.Context, req *v1.CreateAgentTemplateRequest) (*v1.CreateAgentTemplateResponse, error)
	Get(ctx context.Context, req *v1.GetAgentTemplateRequest) (*v1.GetAgentTemplateResponse, error)
	List(ctx context.Context, req *v1.ListAgentTemplateRequest) (*v1.ListAgentTemplateResponse, error)
	Update(ctx context.Context, req *v1.UpdateAgentTemplateRequest) (*v1.UpdateAgentTemplateResponse, error)
	Delete(ctx context.Context, req *v1.DeleteAgentTemplateRequest) (*v1.DeleteAgentTemplateResponse, error)
}

type agentTemplateBiz struct {
	store       agentstore.AIStore
	invalidator mcpclient.Invalidator
}

var _ AgentTemplateBiz = (*agentTemplateBiz)(nil)

// New creates an AgentTemplateBiz. invalidator may be nil.
func New(store agentstore.AIStore, invalidator mcpclient.Invalidator) *agentTemplateBiz {
	return &agentTemplateBiz{store: store, invalidator: invalidator}
}

func (b *agentTemplateBiz) invalidateTools() {
	if b.invalidator != nil {
		b.invalidator.Invalidate()
	}
}

// Create implements AgentTemplateBiz.
func (b *agentTemplateBiz) Create(
	ctx context.Context, req *v1.CreateAgentTemplateRequest,
) (*v1.CreateAgentTemplateResponse, error) {
	row, err := conversion.AgentTemplateV1ToAgentTemplateModel(req.GetAgentTemplate())
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("invalid agent template: %s", err.Error())
	}
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.AgentTemplates().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.CreateAgentTemplateResponse{
		AgentTemplate: conversion.AgentTemplateModelToAgentTemplateV1(row),
	}, nil
}

// Get implements AgentTemplateBiz.
func (b *agentTemplateBiz) Get(
	ctx context.Context, req *v1.GetAgentTemplateRequest,
) (*v1.GetAgentTemplateResponse, error) {
	row, err := b.store.AgentTemplates().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "agent template %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetAgentTemplateResponse{
		AgentTemplate: conversion.AgentTemplateModelToAgentTemplateV1(row),
	}, nil
}

// List implements AgentTemplateBiz.
func (b *agentTemplateBiz) List(
	ctx context.Context, req *v1.ListAgentTemplateRequest,
) (*v1.ListAgentTemplateResponse, error) {
	total, list, err := b.store.AgentTemplates().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	templates := make([]*v1.AgentTemplate, 0, len(list))
	for _, row := range list {
		templates = append(templates, conversion.AgentTemplateModelToAgentTemplateV1(row))
	}
	return &v1.ListAgentTemplateResponse{AgentTemplates: templates, Count: total}, nil
}

// Update implements AgentTemplateBiz.
func (b *agentTemplateBiz) Update(
	ctx context.Context, req *v1.UpdateAgentTemplateRequest,
) (*v1.UpdateAgentTemplateResponse, error) {
	row, err := conversion.AgentTemplateV1ToAgentTemplateModel(req.GetAgentTemplate())
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("invalid agent template: %s", err.Error())
	}
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.AgentTemplates().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.UpdateAgentTemplateResponse{
		AgentTemplate: conversion.AgentTemplateModelToAgentTemplateV1(row),
	}, nil
}

// Delete implements AgentTemplateBiz.
func (b *agentTemplateBiz) Delete(
	ctx context.Context, req *v1.DeleteAgentTemplateRequest,
) (*v1.DeleteAgentTemplateResponse, error) {
	if err := b.store.AgentTemplates().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.DeleteAgentTemplateResponse{}, nil
}
