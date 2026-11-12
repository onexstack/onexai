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

// Package knowledgebase implements the knowledge-base business logic.
package knowledgebase

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Config is the deployment's retrieval-platform configuration.
//
// It is plain data, filled by the DI layer from the agent options, so this
// package never reads configuration itself and a test can point it at an
// httptest server.
type Config struct {
	// RAGFlow configures the only platform the retrieval test supports.
	RAGFlow kbtool.RAGFlowConfig
}

// KnowledgeBaseBiz is the knowledge-base business logic.
//
//nolint:interfacebloat // one resource's surface; splitting it would force a second accessor for a single action
type KnowledgeBaseBiz interface {
	Create(ctx context.Context, req *v1.CreateKnowledgeBaseRequest) (*v1.CreateKnowledgeBaseResponse, error)
	Get(ctx context.Context, req *v1.GetKnowledgeBaseRequest) (*v1.GetKnowledgeBaseResponse, error)
	List(ctx context.Context, req *v1.ListKnowledgeBaseRequest) (*v1.ListKnowledgeBaseResponse, error)
	Update(ctx context.Context, req *v1.UpdateKnowledgeBaseRequest) (*v1.UpdateKnowledgeBaseResponse, error)
	Delete(ctx context.Context, req *v1.DeleteKnowledgeBaseRequest) (*v1.DeleteKnowledgeBaseResponse, error)
	// TestRetrieval runs one retrieval against a stored record, with
	// per-request overrides. It is the console's 召回测试 tab.
	TestRetrieval(ctx context.Context, req *v1.TestKnowledgeBaseRetrievalRequest) (*v1.TestKnowledgeBaseRetrievalResponse, error)
}

type knowledgeBaseBiz struct {
	store  agentstore.AIStore
	tester *kbtool.RAGFlowTester
}

var _ KnowledgeBaseBiz = (*knowledgeBaseBiz)(nil)

// New creates a KnowledgeBaseBiz.
func New(store agentstore.AIStore, config *Config) *knowledgeBaseBiz {
	if config == nil {
		config = &Config{} //nolint:exhaustruct_v5 // zero value: no platform configured
	}
	return &knowledgeBaseBiz{
		store:  store,
		tester: kbtool.NewRAGFlowTester(config.RAGFlow),
	}
}

// Create implements KnowledgeBaseBiz.
func (b *knowledgeBaseBiz) Create(
	ctx context.Context, req *v1.CreateKnowledgeBaseRequest,
) (*v1.CreateKnowledgeBaseResponse, error) {
	row := conversion.KnowledgeBaseV1ToKnowledgeBaseModel(req.GetKnowledgeBase())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.KnowledgeBases().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateKnowledgeBaseResponse{
		KnowledgeBase: conversion.KnowledgeBaseModelToKnowledgeBaseV1(row),
	}, nil
}

// Get implements KnowledgeBaseBiz.
func (b *knowledgeBaseBiz) Get(
	ctx context.Context, req *v1.GetKnowledgeBaseRequest,
) (*v1.GetKnowledgeBaseResponse, error) {
	row, err := b.store.KnowledgeBases().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "knowledge base %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetKnowledgeBaseResponse{
		KnowledgeBase: conversion.KnowledgeBaseModelToKnowledgeBaseV1(row),
	}, nil
}

// List implements KnowledgeBaseBiz.
func (b *knowledgeBaseBiz) List(
	ctx context.Context, req *v1.ListKnowledgeBaseRequest,
) (*v1.ListKnowledgeBaseResponse, error) {
	total, list, err := b.store.KnowledgeBases().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "knowledgebase") {
		before := len(list)
		list = authzview.Filter(ctx, "knowledgebase", list,
			func(row *agentmodel.KnowledgeBaseM) (string, string, string) {
				return row.Namespace, row.Name, ""
			})
		total = authzview.AdjustCount(total, before-len(list))
	}
	kbs := make([]*v1.KnowledgeBase, 0, len(list))
	for _, row := range list {
		kbs = append(kbs, conversion.KnowledgeBaseModelToKnowledgeBaseV1(row))
	}
	return &v1.ListKnowledgeBaseResponse{KnowledgeBases: kbs, Count: total}, nil
}

// Update implements KnowledgeBaseBiz.
func (b *knowledgeBaseBiz) Update(
	ctx context.Context, req *v1.UpdateKnowledgeBaseRequest,
) (*v1.UpdateKnowledgeBaseResponse, error) {
	row := conversion.KnowledgeBaseV1ToKnowledgeBaseModel(req.GetKnowledgeBase())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.KnowledgeBases().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateKnowledgeBaseResponse{
		KnowledgeBase: conversion.KnowledgeBaseModelToKnowledgeBaseV1(row),
	}, nil
}

// Delete implements KnowledgeBaseBiz.
func (b *knowledgeBaseBiz) Delete(
	ctx context.Context, req *v1.DeleteKnowledgeBaseRequest,
) (*v1.DeleteKnowledgeBaseResponse, error) {
	if err := b.store.KnowledgeBases().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteKnowledgeBaseResponse{}, nil
}
