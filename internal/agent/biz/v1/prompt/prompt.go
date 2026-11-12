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

// Package prompt implements the Prompt business logic.
package prompt

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// PromptBiz is the Prompt business logic.
type PromptBiz interface {
	Create(ctx context.Context, req *v1.CreatePromptRequest) (*v1.CreatePromptResponse, error)
	Get(ctx context.Context, req *v1.GetPromptRequest) (*v1.GetPromptResponse, error)
	List(ctx context.Context, req *v1.ListPromptRequest) (*v1.ListPromptResponse, error)
	Update(ctx context.Context, req *v1.UpdatePromptRequest) (*v1.UpdatePromptResponse, error)
	Delete(ctx context.Context, req *v1.DeletePromptRequest) (*v1.DeletePromptResponse, error)
}

type promptBiz struct{ store agentstore.AIStore }

var _ PromptBiz = (*promptBiz)(nil)

// New creates a PromptBiz.
func New(store agentstore.AIStore) *promptBiz { return &promptBiz{store: store} }

// Create implements PromptBiz.
func (b *promptBiz) Create(ctx context.Context, req *v1.CreatePromptRequest) (*v1.CreatePromptResponse, error) {
	row := conversion.PromptV1ToPromptModel(req.GetPrompt())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Prompts().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreatePromptResponse{Prompt: conversion.PromptModelToPromptV1(row)}, nil
}

// Get implements PromptBiz.
func (b *promptBiz) Get(ctx context.Context, req *v1.GetPromptRequest) (*v1.GetPromptResponse, error) {
	row, err := b.store.Prompts().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "prompt %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetPromptResponse{Prompt: conversion.PromptModelToPromptV1(row)}, nil
}

// List implements PromptBiz.
func (b *promptBiz) List(ctx context.Context, req *v1.ListPromptRequest) (*v1.ListPromptResponse, error) {
	total, list, err := b.store.Prompts().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "prompt") {
		before := len(list)
		list = authzview.Filter(ctx, "prompt", list, func(row *agentmodel.PromptM) (string, string, string) {
			return row.Namespace, row.Name, ""
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	prompts := make([]*v1.Prompt, 0, len(list))
	for _, row := range list {
		prompts = append(prompts, conversion.PromptModelToPromptV1(row))
	}
	return &v1.ListPromptResponse{Prompts: prompts, Count: total}, nil
}

// Update implements PromptBiz.
func (b *promptBiz) Update(ctx context.Context, req *v1.UpdatePromptRequest) (*v1.UpdatePromptResponse, error) {
	row := conversion.PromptV1ToPromptModel(req.GetPrompt())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Prompts().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdatePromptResponse{Prompt: conversion.PromptModelToPromptV1(row)}, nil
}

// Delete implements PromptBiz.
func (b *promptBiz) Delete(ctx context.Context, req *v1.DeletePromptRequest) (*v1.DeletePromptResponse, error) {
	if err := b.store.Prompts().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeletePromptResponse{}, nil
}
