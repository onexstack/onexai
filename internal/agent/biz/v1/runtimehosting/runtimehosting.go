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

// Package runtimehosting implements the runtime-hosting business logic.
package runtimehosting

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

// RuntimeHostingBiz is the runtime-hosting business logic.
type RuntimeHostingBiz interface {
	Create(ctx context.Context, req *v1.CreateRuntimeHostingRequest) (*v1.CreateRuntimeHostingResponse, error)
	Get(ctx context.Context, req *v1.GetRuntimeHostingRequest) (*v1.GetRuntimeHostingResponse, error)
	List(ctx context.Context, req *v1.ListRuntimeHostingRequest) (*v1.ListRuntimeHostingResponse, error)
	Update(ctx context.Context, req *v1.UpdateRuntimeHostingRequest) (*v1.UpdateRuntimeHostingResponse, error)
	Delete(ctx context.Context, req *v1.DeleteRuntimeHostingRequest) (*v1.DeleteRuntimeHostingResponse, error)
}

type runtimeHostingBiz struct{ store agentstore.AIStore }

var _ RuntimeHostingBiz = (*runtimeHostingBiz)(nil)

// New creates a RuntimeHostingBiz.
func New(store agentstore.AIStore) *runtimeHostingBiz { return &runtimeHostingBiz{store: store} }

// Create implements RuntimeHostingBiz.
func (b *runtimeHostingBiz) Create(
	ctx context.Context, req *v1.CreateRuntimeHostingRequest,
) (*v1.CreateRuntimeHostingResponse, error) {
	row := conversion.RuntimeHostingV1ToRuntimeHostingModel(req.GetRuntimeHosting())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.RuntimeHostings().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateRuntimeHostingResponse{
		RuntimeHosting: conversion.RuntimeHostingModelToRuntimeHostingV1(row),
	}, nil
}

// Get implements RuntimeHostingBiz.
func (b *runtimeHostingBiz) Get(
	ctx context.Context, req *v1.GetRuntimeHostingRequest,
) (*v1.GetRuntimeHostingResponse, error) {
	row, err := b.store.RuntimeHostings().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "runtime hosting %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetRuntimeHostingResponse{
		RuntimeHosting: conversion.RuntimeHostingModelToRuntimeHostingV1(row),
	}, nil
}

// List implements RuntimeHostingBiz.
func (b *runtimeHostingBiz) List(
	ctx context.Context, req *v1.ListRuntimeHostingRequest,
) (*v1.ListRuntimeHostingResponse, error) {
	total, list, err := b.store.RuntimeHostings().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	hostings := make([]*v1.RuntimeHosting, 0, len(list))
	for _, row := range list {
		hostings = append(hostings, conversion.RuntimeHostingModelToRuntimeHostingV1(row))
	}
	return &v1.ListRuntimeHostingResponse{RuntimeHostings: hostings, Count: total}, nil
}

// Update implements RuntimeHostingBiz.
func (b *runtimeHostingBiz) Update(
	ctx context.Context, req *v1.UpdateRuntimeHostingRequest,
) (*v1.UpdateRuntimeHostingResponse, error) {
	row := conversion.RuntimeHostingV1ToRuntimeHostingModel(req.GetRuntimeHosting())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.RuntimeHostings().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateRuntimeHostingResponse{
		RuntimeHosting: conversion.RuntimeHostingModelToRuntimeHostingV1(row),
	}, nil
}

// Delete implements RuntimeHostingBiz.
func (b *runtimeHostingBiz) Delete(
	ctx context.Context, req *v1.DeleteRuntimeHostingRequest,
) (*v1.DeleteRuntimeHostingResponse, error) {
	if err := b.store.RuntimeHostings().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteRuntimeHostingResponse{}, nil
}
