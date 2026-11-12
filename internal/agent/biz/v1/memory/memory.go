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

// Package memory implements the long-term memory business logic.
package memory

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	memorysvc "github.com/onexstack/onexai/internal/agent/pkg/memory"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/memoryaudit"
	"github.com/onexstack/onexai/internal/agent/store/memoryentry"
	"github.com/onexstack/onexai/internal/pkg/errmsg"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// MemoryBiz is the long-term memory business logic.
type MemoryBiz interface {
	CreateMemory(context.Context, *v1.CreateMemoryRequest) (*v1.CreateMemoryResponse, error)
	GetMemory(context.Context, *v1.GetMemoryRequest) (*v1.GetMemoryResponse, error)
	ListMemory(context.Context, *v1.ListMemoryRequest) (*v1.ListMemoryResponse, error)
	UpdateMemory(context.Context, *v1.UpdateMemoryRequest) (*v1.UpdateMemoryResponse, error)
	DeleteMemory(context.Context, *v1.DeleteMemoryRequest) (*v1.DeleteMemoryResponse, error)

	CreateEntry(context.Context, *v1.CreateMemoryEntryRequest) (*v1.CreateMemoryEntryResponse, error)
	GetEntry(context.Context, *v1.GetMemoryEntryRequest) (*v1.GetMemoryEntryResponse, error)
	ListEntries(context.Context, *v1.ListMemoryEntriesRequest) (*v1.ListMemoryEntriesResponse, error)
	UpdateEntry(context.Context, *v1.UpdateMemoryEntryRequest) (*v1.UpdateMemoryEntryResponse, error)
	DeleteEntry(context.Context, *v1.DeleteMemoryEntryRequest) (*v1.DeleteMemoryEntryResponse, error)
	BatchDeleteEntries(context.Context, *v1.BatchDeleteMemoryEntriesRequest) (*v1.BatchDeleteMemoryEntriesResponse, error)
	SearchEntries(context.Context, *v1.SearchMemoryEntriesRequest) (*v1.SearchMemoryEntriesResponse, error)
	Stats(context.Context, *v1.GetMemoryStatsRequest) (*v1.GetMemoryStatsResponse, error)
	Subjects(context.Context, *v1.ListMemorySubjectsRequest) (*v1.ListMemorySubjectsResponse, error)
	PurgeSubject(context.Context, *v1.PurgeMemorySubjectRequest) (*v1.PurgeMemorySubjectResponse, error)
	ListAudit(context.Context, *v1.ListMemoryAuditRequest) (*v1.ListMemoryAuditResponse, error)
	Reembed(context.Context, *v1.ReembedMemoryRequest) (*v1.ReembedMemoryResponse, error)
	Eval(context.Context, *v1.EvalMemoryRequest) (*v1.EvalMemoryResponse, error)
}

type memoryBiz struct {
	store   store.AIStore
	service *memorysvc.Service
}

var _ MemoryBiz = (*memoryBiz)(nil)

// New creates a MemoryBiz.
func New(store store.AIStore, service *memorysvc.Service) *memoryBiz {
	return &memoryBiz{store: store, service: service}
}

// CreateMemory implements MemoryBiz.
func (b *memoryBiz) CreateMemory(
	ctx context.Context, req *v1.CreateMemoryRequest,
) (*v1.CreateMemoryResponse, error) {
	row := conversion.MemoryV1ToMemoryModel(req.GetMemory())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Memories().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.ensureVectorIndex(ctx, row)
	return &v1.CreateMemoryResponse{Memory: conversion.MemoryModelToMemoryV1(row)}, nil
}

// ensureVectorIndex builds the partial HNSW index for this Memory's embedding
// width. Failing to build it must not fail the write: the entries still work
// through the exact scan, and the maintenance worker retries at the next start.
func (b *memoryBiz) ensureVectorIndex(ctx context.Context, row *model.MemoryM) {
	if b.service == nil || row == nil || row.EmbeddingDimensions <= 0 {
		return
	}
	if err := b.service.EnsureVectorIndex(ctx, int(row.EmbeddingDimensions)); err != nil {
		slog.WarnContext(ctx, "memory: the vector index could not be prepared",
			"memory", row.Name, "dimensions", row.EmbeddingDimensions, "error", err)
	}
}

// GetMemory implements MemoryBiz.
func (b *memoryBiz) GetMemory(
	ctx context.Context, req *v1.GetMemoryRequest,
) (*v1.GetMemoryResponse, error) {
	row, err := b.store.Memories().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "memory %q not found", req.GetName()); err != nil {
		return nil, err
	}
	out := conversion.MemoryModelToMemoryV1(row)
	b.withCounts(ctx, row, out)
	return &v1.GetMemoryResponse{Memory: out}, nil
}

// applyContentPolicy hides another subject's content when the Memory has not
// opted into administrator content reads. The caller is put on the context by
// the browser handler, never read from the request body.
func (b *memoryBiz) applyContentPolicy(
	ctx context.Context, mem *model.MemoryM, subject string, entries ...*model.MemoryEntryM,
) {
	if mem == nil || mem.AllowAdminContentRead {
		return
	}
	caller := memorysvc.CallerFromContext(ctx)
	if caller == "" || caller == subject {
		return
	}
	memorysvc.RedactHiddenContent(entries...)
}

// withCounts fills the list/detail counters from the entry table. A failed
// count leaves the zeros rather than failing the read: the configuration is
// still useful when the entry table is temporarily unavailable.
func (b *memoryBiz) withCounts(ctx context.Context, row *model.MemoryM, out *v1.Memory) {
	if row == nil || out == nil || b.service == nil {
		return
	}
	if stats, err := b.service.Stats(ctx, row, "", ""); err == nil {
		out.EntryCount = stats.Total
		out.SubjectCount = stats.Subjects
	}
}

// ListMemory implements MemoryBiz.
func (b *memoryBiz) ListMemory(
	ctx context.Context, req *v1.ListMemoryRequest,
) (*v1.ListMemoryResponse, error) {
	total, list, err := b.store.Memories().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "memory") {
		before := len(list)
		list = authzview.Filter(ctx, "memory", list, func(row *model.MemoryM) (string, string, string) {
			return row.Namespace, row.Name, row.Owner
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	out := make([]*v1.Memory, 0, len(list))
	for _, row := range list {
		item := conversion.MemoryModelToMemoryV1(row)
		b.withCounts(ctx, row, item)
		out = append(out, item)
	}
	return &v1.ListMemoryResponse{Memories: out, Count: total}, nil
}

// UpdateMemory implements MemoryBiz.
func (b *memoryBiz) UpdateMemory(
	ctx context.Context, req *v1.UpdateMemoryRequest,
) (*v1.UpdateMemoryResponse, error) {
	row := conversion.MemoryV1ToMemoryModel(req.GetMemory())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Memories().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.ensureVectorIndex(ctx, row)
	return &v1.UpdateMemoryResponse{Memory: conversion.MemoryModelToMemoryV1(row)}, nil
}

// DeleteMemory implements MemoryBiz.
func (b *memoryBiz) DeleteMemory(
	ctx context.Context, req *v1.DeleteMemoryRequest,
) (*v1.DeleteMemoryResponse, error) {
	if err := b.store.Memories().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteMemoryResponse{}, nil
}

// CreateEntry implements MemoryBiz.
func (b *memoryBiz) CreateEntry(
	ctx context.Context, req *v1.CreateMemoryEntryRequest,
) (*v1.CreateMemoryEntryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	in := req.GetEntry()
	entry, err := b.service.Create(ctx, mem, memorysvc.CreateInput{
		Agent:          in.GetAgent(),
		AgentNamespace: in.GetAgentNamespace(),
		Subject:        in.GetSubject(),
		SessionID:      in.GetSessionId(),
		RunID:          in.GetRunId(),
		Type:           in.GetType(),
		Content:        in.GetContent(),
		Importance:     in.GetImportance(),
		Confidence:     in.GetConfidence(),
		Tags:           in.GetTags(),
		Metadata:       in.GetMetadata(),
		Source:         in.GetSource(),
		Actor:          in.GetActor(),
		ExpiresAt:      in.GetExpiresAt(),
	})
	if err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateMemoryEntryResponse{Entry: conversion.MemoryEntryModelToV1(entry)}, nil
}

// GetEntry implements MemoryBiz.
func (b *memoryBiz) GetEntry(
	ctx context.Context, req *v1.GetMemoryEntryRequest,
) (*v1.GetMemoryEntryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	entry, err := b.service.Get(ctx, mem, req.GetSubject(), req.GetId())
	if err != nil {
		return nil, storeerr.GetError(entry, err, "memory entry %q not found", req.GetId())
	}
	b.applyContentPolicy(ctx, mem, req.GetSubject(), entry)
	return &v1.GetMemoryEntryResponse{Entry: conversion.MemoryEntryModelToV1(entry)}, nil
}

// ListEntries implements MemoryBiz.
func (b *memoryBiz) ListEntries(
	ctx context.Context, req *v1.ListMemoryEntriesRequest,
) (*v1.ListMemoryEntriesResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	total, rows, err := b.service.List(ctx, mem, memoryentry.ListFilter{
		Agent:     req.GetAgent(),
		Subject:   req.GetSubject(),
		Type:      req.GetType(),
		Status:    req.GetStatus(),
		Source:    req.GetSource(),
		Query:     req.GetQ(),
		SessionID: req.GetSessionId(),
		RunID:     req.GetRunId(),
		Tag:       req.GetTag(),
		Offset:    int(req.GetOffset()),
		Limit:     int(req.GetLimit()),
		Sort:      req.GetSort(),
	})
	if err != nil {
		return nil, storeerr.Read(err)
	}
	b.applyContentPolicy(ctx, mem, req.GetSubject(), rows...)
	out := make([]*v1.MemoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, conversion.MemoryEntryModelToV1(row))
	}
	return &v1.ListMemoryEntriesResponse{Entries: out, Count: total}, nil
}

// UpdateEntry implements MemoryBiz.
func (b *memoryBiz) UpdateEntry(
	ctx context.Context, req *v1.UpdateMemoryEntryRequest,
) (*v1.UpdateMemoryEntryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	in := req.GetEntry()
	importance := in.GetImportance()
	confidence := in.GetConfidence()
	entry, err := b.service.Update(ctx, mem, req.GetSubject(), req.GetId(), memorysvc.UpdateInput{
		Type:       in.GetType(),
		Content:    in.GetContent(),
		Importance: &importance,
		Confidence: &confidence,
		Tags:       in.GetTags(),
		Metadata:   in.GetMetadata(),
		Status:     in.GetStatus(),
	})
	if err != nil {
		return nil, storeerr.Write(err)
	}
	b.applyContentPolicy(ctx, mem, req.GetSubject(), entry)
	return &v1.UpdateMemoryEntryResponse{Entry: conversion.MemoryEntryModelToV1(entry)}, nil
}

// DeleteEntry implements MemoryBiz.
func (b *memoryBiz) DeleteEntry(
	ctx context.Context, req *v1.DeleteMemoryEntryRequest,
) (*v1.DeleteMemoryEntryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if err := b.service.Delete(ctx, mem, req.GetSubject(), req.GetId()); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteMemoryEntryResponse{}, nil
}

// BatchDeleteEntries implements MemoryBiz.
func (b *memoryBiz) BatchDeleteEntries(
	ctx context.Context, req *v1.BatchDeleteMemoryEntriesRequest,
) (*v1.BatchDeleteMemoryEntriesResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	subject := req.GetSubject()
	results := make([]*v1.BatchDeleteMemoryEntriesResult, 0, len(req.GetIds()))
	for _, id := range req.GetIds() {
		err := b.service.Delete(ctx, mem, subject, id)
		item := &v1.BatchDeleteMemoryEntriesResult{Id: id, Ok: err == nil}
		if err != nil {
			item.Reason = errmsg.Text(err)
		}
		results = append(results, item)
	}
	return &v1.BatchDeleteMemoryEntriesResponse{Results: results}, nil
}

// SearchEntries implements MemoryBiz.
func (b *memoryBiz) SearchEntries(
	ctx context.Context, req *v1.SearchMemoryEntriesRequest,
) (*v1.SearchMemoryEntriesResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	result, err := b.service.Search(ctx, mem, memorysvc.SearchInput{
		Agent:     req.GetAgent(),
		Subject:   req.GetSubject(),
		Query:     req.GetQuery(),
		Mode:      req.GetMode(),
		Types:     req.GetTypes(),
		Tags:      req.GetTags(),
		TopK:      int(req.GetTopK()),
		MinScore:  req.GetMinScore(),
		MaxTokens: int(req.GetMaxTokens()),
		Persist:   req.GetPersist(),
		Explain:   req.GetExplain(),
	})
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if !mem.AllowAdminContentRead {
		caller := memorysvc.CallerFromContext(ctx)
		if caller != "" && caller != req.GetSubject() {
			for i := range result.Hits {
				memorysvc.RedactHiddenContent(result.Hits[i].Entry)
			}
		}
	}
	out := make([]*v1.MemorySearchHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		out = append(out, &v1.MemorySearchHit{
			Entry:      conversion.MemoryEntryModelToV1(hit.Entry),
			Score:      hit.Score,
			TokenCount: int32(hit.TokenCount),
			Breakdown: &v1.MemoryScoreBreakdown{
				Final:            hit.Breakdown.Final,
				Rrf:              hit.Breakdown.RRF,
				VectorSimilarity: hit.Breakdown.VectorSimilarity,
				VectorRank:       int32(hit.Breakdown.VectorRank),
				KeywordRank:      int32(hit.Breakdown.KeywordRank),
				Recency:          hit.Breakdown.Recency,
				Importance:       hit.Breakdown.Importance,
				Confidence:       hit.Breakdown.Confidence,
			},
		})
	}
	return &v1.SearchMemoryEntriesResponse{
		Hits:              out,
		VectorCandidates:  int32(result.VectorCandidates),
		KeywordCandidates: int32(result.KeywordCandidates),
		TookMs:            result.TookMs,
		TokenCount:        int32(result.TokenCount),
		Degraded:          result.Degraded,
	}, nil
}

// Stats implements MemoryBiz.
func (b *memoryBiz) Stats(
	ctx context.Context, req *v1.GetMemoryStatsRequest,
) (*v1.GetMemoryStatsResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	stats, err := b.service.Stats(ctx, mem, req.GetAgent(), req.GetSubject())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return &v1.GetMemoryStatsResponse{Stats: &v1.MemoryStats{
		Total:             stats.Total,
		ByType:            stats.ByType,
		ByStatus:          stats.ByStatus,
		PendingEmbeddings: stats.PendingEmbeddings,
		FailedEmbeddings:  stats.FailedEmbeddings,
		Subjects:          stats.Subjects,
	}}, nil
}

// Subjects implements MemoryBiz.
func (b *memoryBiz) Subjects(
	ctx context.Context, req *v1.ListMemorySubjectsRequest,
) (*v1.ListMemorySubjectsResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	total, rows, err := b.service.Subjects(ctx, mem, req.GetAgent(), req.GetQ(), int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := make([]*v1.MemorySubject, 0, len(rows))
	for _, row := range rows {
		out = append(out, &v1.MemorySubject{
			Subject:       row.Subject,
			Entries:       row.Entries,
			LastCreatedAt: row.LastCreatedAt,
		})
	}
	return &v1.ListMemorySubjectsResponse{Subjects: out, Count: total}, nil
}

// PurgeSubject implements MemoryBiz.
func (b *memoryBiz) PurgeSubject(
	ctx context.Context, req *v1.PurgeMemorySubjectRequest,
) (*v1.PurgeMemorySubjectResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	deleted, err := b.service.Purge(ctx, mem, req.GetSubject(), req.GetActor())
	if err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.PurgeMemorySubjectResponse{Deleted: deleted}, nil
}

// Eval implements MemoryBiz. It runs the configured retrieval for each case and
// reports precision/recall/MRR; it never marks entries as accessed.
func (b *memoryBiz) Eval(
	ctx context.Context, req *v1.EvalMemoryRequest,
) (*v1.EvalMemoryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	topK := int(req.GetTopK())
	if topK <= 0 {
		topK = int(mem.TopK)
	}
	if topK <= 0 {
		topK = 8
	}
	out := &v1.EvalMemoryResponse{}
	var sumPrecision, sumRecall, sumMRR float64
	for _, item := range req.GetCases() {
		result, err := b.service.Search(ctx, mem, memorysvc.SearchInput{
			Agent:   req.GetAgent(),
			Subject: req.GetSubject(),
			Query:   item.GetQuery(),
			Mode:    req.GetMode(),
			TopK:    topK,
			Persist: false,
			Explain: false,
		})
		if err != nil {
			return nil, storeerr.Read(err)
		}
		retrieved := make([]string, 0, len(result.Hits))
		retrievedSet := make(map[string]bool, len(result.Hits))
		for _, hit := range result.Hits {
			retrieved = append(retrieved, hit.Entry.ID)
			retrievedSet[hit.Entry.ID] = true
		}
		expected := make(map[string]bool, len(item.GetExpectedIds()))
		for _, id := range item.GetExpectedIds() {
			expected[id] = true
		}
		relevant := 0
		firstRank := 0
		for rank, id := range retrieved {
			if !expected[id] {
				continue
			}
			relevant++
			if firstRank == 0 {
				firstRank = rank + 1
			}
		}
		precision := 0.0
		if len(retrieved) > 0 {
			precision = float64(relevant) / float64(len(retrieved))
		}
		recall := 0.0
		if len(expected) > 0 {
			recall = float64(relevant) / float64(len(expected))
		}
		mrr := 0.0
		if firstRank > 0 {
			mrr = 1 / float64(firstRank)
		}
		missing := make([]string, 0, len(expected))
		for id := range expected {
			if !retrievedSet[id] {
				missing = append(missing, id)
			}
		}
		out.Cases = append(out.Cases, &v1.MemoryEvalCaseResult{
			Query:        item.GetQuery(),
			Precision:    precision,
			Recall:       recall,
			Mrr:          mrr,
			RetrievedIds: retrieved,
			MissingIds:   missing,
		})
		sumPrecision += precision
		sumRecall += recall
		sumMRR += mrr
	}
	if n := float64(len(req.GetCases())); n > 0 {
		out.Precision = sumPrecision / n
		out.Recall = sumRecall / n
		out.Mrr = sumMRR / n
	}
	return out, nil
}

// Reembed implements MemoryBiz.
func (b *memoryBiz) Reembed(
	ctx context.Context, req *v1.ReembedMemoryRequest,
) (*v1.ReembedMemoryResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	result, err := b.service.Reembed(ctx, mem, req.GetDryRun(), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.ReembedMemoryResponse{
		Scanned:   result.Scanned,
		Updated:   result.Updated,
		Failed:    result.Failed,
		Remaining: result.Remaining,
	}, nil
}

// ListAudit implements MemoryBiz.
func (b *memoryBiz) ListAudit(
	ctx context.Context, req *v1.ListMemoryAuditRequest,
) (*v1.ListMemoryAuditResponse, error) {
	mem, err := b.service.Memory(ctx, req.GetMemory())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if b.service.Audit == nil {
		return &v1.ListMemoryAuditResponse{}, nil
	}
	total, rows, err := b.service.Audit.List(ctx, memoryaudit.Filter{
		Namespace: mem.Namespace,
		Memory:    mem.Name,
		Subject:   req.GetSubject(),
		Action:    req.GetAction(),
		Offset:    int(req.GetOffset()),
		Limit:     int(req.GetLimit()),
	})
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := make([]*v1.MemoryAudit, 0, len(rows))
	for _, row := range rows {
		out = append(out, &v1.MemoryAudit{
			Id:        row.ID,
			Memory:    row.Memory,
			Subject:   row.Subject,
			EntryId:   row.EntryID,
			Actor:     row.Actor,
			Action:    row.Action,
			Source:    row.Source,
			Reason:    row.Reason,
			CreatedAt: row.CreatedAt,
		})
	}
	return &v1.ListMemoryAuditResponse{Audits: out, Count: total}, nil
}
