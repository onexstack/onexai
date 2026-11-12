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

// Package scheduledrun implements the scheduled-run business logic.
package scheduledrun

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

// ScheduledRunBiz is the scheduled-run business logic.
type ScheduledRunBiz interface {
	Create(ctx context.Context, req *v1.CreateScheduledRunRequest) (*v1.CreateScheduledRunResponse, error)
	Get(ctx context.Context, req *v1.GetScheduledRunRequest) (*v1.GetScheduledRunResponse, error)
	List(ctx context.Context, req *v1.ListScheduledRunRequest) (*v1.ListScheduledRunResponse, error)
	Update(ctx context.Context, req *v1.UpdateScheduledRunRequest) (*v1.UpdateScheduledRunResponse, error)
	Delete(ctx context.Context, req *v1.DeleteScheduledRunRequest) (*v1.DeleteScheduledRunResponse, error)
}

type scheduledRunBiz struct{ store agentstore.AIStore }

var _ ScheduledRunBiz = (*scheduledRunBiz)(nil)

// New creates a ScheduledRunBiz.
func New(store agentstore.AIStore) *scheduledRunBiz { return &scheduledRunBiz{store: store} }

// Create implements ScheduledRunBiz.
func (b *scheduledRunBiz) Create(
	ctx context.Context, req *v1.CreateScheduledRunRequest,
) (*v1.CreateScheduledRunResponse, error) {
	row := conversion.ScheduledRunV1ToScheduledRunModel(req.GetScheduledRun())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.ScheduledRuns().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateScheduledRunResponse{
		ScheduledRun: conversion.ScheduledRunModelToScheduledRunV1(row),
	}, nil
}

// Get implements ScheduledRunBiz.
func (b *scheduledRunBiz) Get(
	ctx context.Context, req *v1.GetScheduledRunRequest,
) (*v1.GetScheduledRunResponse, error) {
	row, err := b.store.ScheduledRuns().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "scheduled run %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetScheduledRunResponse{
		ScheduledRun: conversion.ScheduledRunModelToScheduledRunV1(row),
	}, nil
}

// List implements ScheduledRunBiz.
func (b *scheduledRunBiz) List(
	ctx context.Context, req *v1.ListScheduledRunRequest,
) (*v1.ListScheduledRunResponse, error) {
	total, list, err := b.store.ScheduledRuns().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	runs := make([]*v1.ScheduledRun, 0, len(list))
	for _, row := range list {
		runs = append(runs, conversion.ScheduledRunModelToScheduledRunV1(row))
	}
	return &v1.ListScheduledRunResponse{ScheduledRuns: runs, Count: total}, nil
}

// Update implements ScheduledRunBiz.
func (b *scheduledRunBiz) Update(
	ctx context.Context, req *v1.UpdateScheduledRunRequest,
) (*v1.UpdateScheduledRunResponse, error) {
	row := conversion.ScheduledRunV1ToScheduledRunModel(req.GetScheduledRun())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.ScheduledRuns().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateScheduledRunResponse{
		ScheduledRun: conversion.ScheduledRunModelToScheduledRunV1(row),
	}, nil
}

// Delete implements ScheduledRunBiz.
func (b *scheduledRunBiz) Delete(
	ctx context.Context, req *v1.DeleteScheduledRunRequest,
) (*v1.DeleteScheduledRunResponse, error) {
	if err := b.store.ScheduledRuns().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteScheduledRunResponse{}, nil
}
