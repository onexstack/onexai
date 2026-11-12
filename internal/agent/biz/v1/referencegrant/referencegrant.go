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

// Package referencegrant implements the ReferenceGrant business logic.
package referencegrant

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ReferenceGrantBiz is the ReferenceGrant business logic.
type ReferenceGrantBiz interface {
	Create(ctx context.Context, req *v1.CreateReferenceGrantRequest) (*v1.CreateReferenceGrantResponse, error)
	Get(ctx context.Context, req *v1.GetReferenceGrantRequest) (*v1.GetReferenceGrantResponse, error)
	List(ctx context.Context, req *v1.ListReferenceGrantRequest) (*v1.ListReferenceGrantResponse, error)
	Update(ctx context.Context, req *v1.UpdateReferenceGrantRequest) (*v1.UpdateReferenceGrantResponse, error)
	Delete(ctx context.Context, req *v1.DeleteReferenceGrantRequest) (*v1.DeleteReferenceGrantResponse, error)
}

type referenceGrantBiz struct {
	store agentstore.AIStore
}

var _ ReferenceGrantBiz = (*referenceGrantBiz)(nil)

// New creates a ReferenceGrantBiz.
func New(store agentstore.AIStore) *referenceGrantBiz {
	return &referenceGrantBiz{store: store}
}

// Create implements ReferenceGrantBiz.
func (b *referenceGrantBiz) Create(
	ctx context.Context, req *v1.CreateReferenceGrantRequest,
) (*v1.CreateReferenceGrantResponse, error) {
	created, err := b.store.ReferenceGrants().Create(ctx, conversion.ReferenceGrantV1ToCR(req.GetReferenceGrant()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateReferenceGrantResponse{
		ReferenceGrant: conversion.ReferenceGrantCRToV1(created),
	}, nil
}

// Get implements ReferenceGrantBiz.
func (b *referenceGrantBiz) Get(
	ctx context.Context, req *v1.GetReferenceGrantRequest,
) (*v1.GetReferenceGrantResponse, error) {
	item, err := b.store.ReferenceGrants().Get(ctx, req.GetName())
	if err := storeerr.GetError(item, err, "reference grant %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetReferenceGrantResponse{
		ReferenceGrant: conversion.ReferenceGrantCRToV1(item),
	}, nil
}

// List implements ReferenceGrantBiz.
func (b *referenceGrantBiz) List(
	ctx context.Context, req *v1.ListReferenceGrantRequest,
) (*v1.ListReferenceGrantResponse, error) {
	total, list, err := b.store.ReferenceGrants().List(ctx, int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	grants := make([]*v1.ReferenceGrant, 0, len(list))
	for _, item := range list {
		grants = append(grants, conversion.ReferenceGrantCRToV1(item))
	}
	return &v1.ListReferenceGrantResponse{ReferenceGrants: grants, Count: total}, nil
}

// Update implements ReferenceGrantBiz.
func (b *referenceGrantBiz) Update(
	ctx context.Context, req *v1.UpdateReferenceGrantRequest,
) (*v1.UpdateReferenceGrantResponse, error) {
	updated, err := b.store.ReferenceGrants().Update(ctx, conversion.ReferenceGrantV1ToCR(req.GetReferenceGrant()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateReferenceGrantResponse{
		ReferenceGrant: conversion.ReferenceGrantCRToV1(updated),
	}, nil
}

// Delete implements ReferenceGrantBiz.
func (b *referenceGrantBiz) Delete(
	ctx context.Context, req *v1.DeleteReferenceGrantRequest,
) (*v1.DeleteReferenceGrantResponse, error) {
	if err := b.store.ReferenceGrants().Delete(ctx, req.GetName()); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteReferenceGrantResponse{}, nil
}
