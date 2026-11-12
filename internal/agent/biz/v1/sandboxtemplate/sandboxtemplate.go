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

// Package sandboxtemplate implements the sandbox-template business logic.
package sandboxtemplate

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// SandboxTemplateBiz is the sandbox-template business logic.
type SandboxTemplateBiz interface {
	Create(ctx context.Context, req *v1.CreateSandboxTemplateRequest) (*v1.CreateSandboxTemplateResponse, error)
	Get(ctx context.Context, req *v1.GetSandboxTemplateRequest) (*v1.GetSandboxTemplateResponse, error)
	List(ctx context.Context, req *v1.ListSandboxTemplateRequest) (*v1.ListSandboxTemplateResponse, error)
	Update(ctx context.Context, req *v1.UpdateSandboxTemplateRequest) (*v1.UpdateSandboxTemplateResponse, error)
	Delete(ctx context.Context, req *v1.DeleteSandboxTemplateRequest) (*v1.DeleteSandboxTemplateResponse, error)
}

type sandboxTemplateBiz struct{ store agentstore.AIStore }

var _ SandboxTemplateBiz = (*sandboxTemplateBiz)(nil)

// New creates a SandboxTemplateBiz.
func New(store agentstore.AIStore) *sandboxTemplateBiz { return &sandboxTemplateBiz{store: store} }

// Create implements SandboxTemplateBiz.
func (b *sandboxTemplateBiz) Create(
	ctx context.Context, req *v1.CreateSandboxTemplateRequest,
) (*v1.CreateSandboxTemplateResponse, error) {
	row := conversion.SandboxTemplateV1ToSandboxTemplateModel(req.GetSandboxTemplate())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.SandboxTemplates().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateSandboxTemplateResponse{
		SandboxTemplate: conversion.SandboxTemplateModelToSandboxTemplateV1(row),
	}, nil
}

// Get implements SandboxTemplateBiz.
func (b *sandboxTemplateBiz) Get(
	ctx context.Context, req *v1.GetSandboxTemplateRequest,
) (*v1.GetSandboxTemplateResponse, error) {
	row, err := b.store.SandboxTemplates().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "sandbox template %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetSandboxTemplateResponse{
		SandboxTemplate: conversion.SandboxTemplateModelToSandboxTemplateV1(row),
	}, nil
}

// List implements SandboxTemplateBiz.
func (b *sandboxTemplateBiz) List(
	ctx context.Context, req *v1.ListSandboxTemplateRequest,
) (*v1.ListSandboxTemplateResponse, error) {
	total, list, err := b.store.SandboxTemplates().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	templates := make([]*v1.SandboxTemplate, 0, len(list))
	for _, row := range list {
		templates = append(templates, conversion.SandboxTemplateModelToSandboxTemplateV1(row))
	}
	return &v1.ListSandboxTemplateResponse{SandboxTemplates: templates, Count: total}, nil
}

// Update implements SandboxTemplateBiz.
func (b *sandboxTemplateBiz) Update(
	ctx context.Context, req *v1.UpdateSandboxTemplateRequest,
) (*v1.UpdateSandboxTemplateResponse, error) {
	row := conversion.SandboxTemplateV1ToSandboxTemplateModel(req.GetSandboxTemplate())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.SandboxTemplates().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateSandboxTemplateResponse{
		SandboxTemplate: conversion.SandboxTemplateModelToSandboxTemplateV1(row),
	}, nil
}

// Delete implements SandboxTemplateBiz.
func (b *sandboxTemplateBiz) Delete(
	ctx context.Context, req *v1.DeleteSandboxTemplateRequest,
) (*v1.DeleteSandboxTemplateResponse, error) {
	if err := b.store.SandboxTemplates().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteSandboxTemplateResponse{}, nil
}
