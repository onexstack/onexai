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

// Package clusteragentbinding implements the CRUD surface for cluster-wide
// bindings. It is the platform-level counterpart of the namespaced binding: the
// object lives at cluster scope because its audience spans namespaces.
package clusteragentbinding

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Biz is the cluster-binding business logic.
type Biz interface {
	Create(ctx context.Context, req *v1.CreateClusterAgentBindingRequest) (*v1.CreateClusterAgentBindingResponse, error)
	Get(ctx context.Context, req *v1.GetClusterAgentBindingRequest) (*v1.GetClusterAgentBindingResponse, error)
	List(ctx context.Context, req *v1.ListClusterAgentBindingRequest) (*v1.ListClusterAgentBindingResponse, error)
	Update(ctx context.Context, req *v1.UpdateClusterAgentBindingRequest) (*v1.UpdateClusterAgentBindingResponse, error)
	Delete(ctx context.Context, req *v1.DeleteClusterAgentBindingRequest) (*v1.DeleteClusterAgentBindingResponse, error)
}

type biz struct {
	store       agentstore.AIStore
	invalidator mcpclient.Invalidator
}

var _ Biz = (*biz)(nil)

// New creates a cluster-binding Biz. invalidator may be nil when the
// deployment has no MCP runtime.
func New(store agentstore.AIStore, invalidator mcpclient.Invalidator) *biz {
	return &biz{store: store, invalidator: invalidator}
}

func (b *biz) invalidateTools() {
	if b.invalidator != nil {
		b.invalidator.Invalidate()
	}
}

// Create implements Biz.
func (b *biz) Create(
	ctx context.Context, req *v1.CreateClusterAgentBindingRequest,
) (*v1.CreateClusterAgentBindingResponse, error) {
	created, err := b.store.ClusterAgentBindings().Create(
		ctx, conversion.ClusterAgentBindingV1ToCR(req.GetClusterAgentBinding()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.CreateClusterAgentBindingResponse{
		ClusterAgentBinding: conversion.ClusterAgentBindingCRToV1(created),
	}, nil
}

// Get implements Biz.
func (b *biz) Get(
	ctx context.Context, req *v1.GetClusterAgentBindingRequest,
) (*v1.GetClusterAgentBindingResponse, error) {
	item, err := b.store.ClusterAgentBindings().Get(ctx, req.GetName())
	if err := storeerr.GetError(item, err, "cluster agent binding %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetClusterAgentBindingResponse{
		ClusterAgentBinding: conversion.ClusterAgentBindingCRToV1(item),
	}, nil
}

// List implements Biz.
func (b *biz) List(
	ctx context.Context, req *v1.ListClusterAgentBindingRequest,
) (*v1.ListClusterAgentBindingResponse, error) {
	total, list, err := b.store.ClusterAgentBindings().List(ctx, int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	bindings := make([]*v1.ClusterAgentBinding, 0, len(list))
	for _, item := range list {
		bindings = append(bindings, conversion.ClusterAgentBindingCRToV1(item))
	}
	return &v1.ListClusterAgentBindingResponse{ClusterAgentBindings: bindings, Count: total}, nil
}

// Update implements Biz.
func (b *biz) Update(
	ctx context.Context, req *v1.UpdateClusterAgentBindingRequest,
) (*v1.UpdateClusterAgentBindingResponse, error) {
	updated, err := b.store.ClusterAgentBindings().Update(
		ctx, conversion.ClusterAgentBindingV1ToCR(req.GetClusterAgentBinding()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.UpdateClusterAgentBindingResponse{
		ClusterAgentBinding: conversion.ClusterAgentBindingCRToV1(updated),
	}, nil
}

// Delete implements Biz.
func (b *biz) Delete(
	ctx context.Context, req *v1.DeleteClusterAgentBindingRequest,
) (*v1.DeleteClusterAgentBindingResponse, error) {
	if err := b.store.ClusterAgentBindings().Delete(ctx, req.GetName()); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.DeleteClusterAgentBindingResponse{}, nil
}
