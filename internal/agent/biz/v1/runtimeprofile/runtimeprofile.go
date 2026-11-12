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

// Package runtimeprofile implements the runtime-profile business logic.
package runtimeprofile

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

// RuntimeProfileBiz is the runtime-profile business logic.
type RuntimeProfileBiz interface {
	Create(ctx context.Context, req *v1.CreateRuntimeProfileRequest) (*v1.CreateRuntimeProfileResponse, error)
	Get(ctx context.Context, req *v1.GetRuntimeProfileRequest) (*v1.GetRuntimeProfileResponse, error)
	List(ctx context.Context, req *v1.ListRuntimeProfileRequest) (*v1.ListRuntimeProfileResponse, error)
	Update(ctx context.Context, req *v1.UpdateRuntimeProfileRequest) (*v1.UpdateRuntimeProfileResponse, error)
	Delete(ctx context.Context, req *v1.DeleteRuntimeProfileRequest) (*v1.DeleteRuntimeProfileResponse, error)
}

type runtimeProfileBiz struct{ store agentstore.AIStore }

var _ RuntimeProfileBiz = (*runtimeProfileBiz)(nil)

// New creates a RuntimeProfileBiz.
func New(store agentstore.AIStore) *runtimeProfileBiz { return &runtimeProfileBiz{store: store} }

// Create implements RuntimeProfileBiz.
func (b *runtimeProfileBiz) Create(
	ctx context.Context, req *v1.CreateRuntimeProfileRequest,
) (*v1.CreateRuntimeProfileResponse, error) {
	row := conversion.RuntimeProfileV1ToRuntimeProfileModel(req.GetRuntimeProfile())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.RuntimeProfiles().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateRuntimeProfileResponse{
		RuntimeProfile: conversion.RuntimeProfileModelToRuntimeProfileV1(row),
	}, nil
}

// Get implements RuntimeProfileBiz.
func (b *runtimeProfileBiz) Get(
	ctx context.Context, req *v1.GetRuntimeProfileRequest,
) (*v1.GetRuntimeProfileResponse, error) {
	row, err := b.store.RuntimeProfiles().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "runtime profile %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetRuntimeProfileResponse{
		RuntimeProfile: conversion.RuntimeProfileModelToRuntimeProfileV1(row),
	}, nil
}

// List implements RuntimeProfileBiz.
func (b *runtimeProfileBiz) List(
	ctx context.Context, req *v1.ListRuntimeProfileRequest,
) (*v1.ListRuntimeProfileResponse, error) {
	total, list, err := b.store.RuntimeProfiles().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	profiles := make([]*v1.RuntimeProfile, 0, len(list))
	for _, row := range list {
		profiles = append(profiles, conversion.RuntimeProfileModelToRuntimeProfileV1(row))
	}
	return &v1.ListRuntimeProfileResponse{RuntimeProfiles: profiles, Count: total}, nil
}

// Update implements RuntimeProfileBiz.
func (b *runtimeProfileBiz) Update(
	ctx context.Context, req *v1.UpdateRuntimeProfileRequest,
) (*v1.UpdateRuntimeProfileResponse, error) {
	row := conversion.RuntimeProfileV1ToRuntimeProfileModel(req.GetRuntimeProfile())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.RuntimeProfiles().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateRuntimeProfileResponse{
		RuntimeProfile: conversion.RuntimeProfileModelToRuntimeProfileV1(row),
	}, nil
}

// Delete implements RuntimeProfileBiz.
func (b *runtimeProfileBiz) Delete(
	ctx context.Context, req *v1.DeleteRuntimeProfileRequest,
) (*v1.DeleteRuntimeProfileResponse, error) {
	if err := b.store.RuntimeProfiles().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteRuntimeProfileResponse{}, nil
}
