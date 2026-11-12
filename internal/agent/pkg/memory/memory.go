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

// Package memory implements the long-term memory service: write validation,
// deduplication, asynchronous embedding, hybrid retrieval and aggregation.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/embedding"
	"github.com/onexstack/onexai/internal/agent/pkg/vector"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/memoryaudit"
	"github.com/onexstack/onexai/internal/agent/store/memoryentry"
	"github.com/onexstack/onexai/internal/pkg/safego"
)

// ModelResolver resolves a Model record to an embeddings endpoint.
type ModelResolver interface {
	EmbeddingEndpoint(ctx context.Context, name string) (baseURL, apiKey, modelName string, err error)
}

// Service is the long-term memory subsystem.
type Service struct {
	Store    *memoryentry.Store
	Memories store.MemoryStore
	Audit    *memoryaudit.Store
	Models   ModelResolver
	Embedder *embedding.Client

	// DefaultTTLDays applies when a Memory does not set one.
	DefaultTTLDays int32
	// MaxRows bounds the scan for one search.
	MaxRows int
}

// CreateInput is one entry to write.
type CreateInput struct {
	Agent string
	// AgentNamespace is where Agent lives. Empty means unknown: a manual entry
	// created through the admin API has no run to read it from.
	AgentNamespace string
	Subject        string
	SessionID      string
	RunID          string
	Type           string
	Content        string
	Importance     float32
	Confidence     float32
	Tags           []string
	Metadata       string
	Source         string
	Actor          string
	ExpiresAt      int64
}

// UpdateInput is the mutable subset of one entry.
type UpdateInput struct {
	Type       string
	Content    string
	Importance *float32
	Confidence *float32
	Tags       []string
	Metadata   string
	Status     string
	ExpiresAt  *int64
}

// SearchInput is one recall request.
type SearchInput struct {
	Agent     string
	Subject   string
	SessionID string
	Query     string
	Mode      string
	Types     []string
	Tags      []string
	TopK      int
	MinScore  float64
	MaxTokens int
	Persist   bool
	Explain   bool
}

// Hit is one search result.
type Hit struct {
	Entry      *model.MemoryEntryM
	Score      float64
	Breakdown  Breakdown
	TokenCount int
}

// Breakdown explains a hit.
type Breakdown struct {
	Final            float64 `json:"final"`
	RRF              float64 `json:"rrf"`
	VectorSimilarity float64 `json:"vectorSimilarity"`
	VectorRank       int     `json:"vectorRank"`
	KeywordRank      int     `json:"keywordRank"`
	Recency          float64 `json:"recency"`
	Importance       float64 `json:"importance"`
	Confidence       float64 `json:"confidence"`
}

// SearchResult is one recall response.
type SearchResult struct {
	Hits              []Hit
	VectorCandidates  int
	KeywordCandidates int
	TookMs            int64
	TokenCount        int
	Degraded          bool
}

// ReembedResult reports one re-embedding batch.
type ReembedResult struct {
	Scanned   int64
	Updated   int64
	Failed    int64
	Remaining int64
}

// Reembed re-embeds entries whose embedding version is stale. It is bounded so
// one HTTP request cannot run for an unbounded time; the caller repeats until
// Remaining is zero.
func (s *Service) Reembed(
	ctx context.Context, mem *model.MemoryM, dryRun bool, limit int,
) (*ReembedResult, error) {
	if mem == nil || s.Embedder == nil || mem.EmbeddingModel == "" {
		return nil, fmt.Errorf("memory %q has no embedding model", memoryName(mem))
	}
	rows, total, err := s.Store.ListNeedingReembed(
		ctx, mem.Namespace, mem.Name, mem.EmbeddingVersion, limit)
	if err != nil {
		return nil, err
	}
	result := &ReembedResult{Scanned: int64(len(rows))}
	if dryRun {
		result.Remaining = total - result.Scanned
		return result, nil
	}
	for _, row := range rows {
		vec, err := s.embed(ctx, mem, row.Content)
		if err != nil {
			result.Failed++
			_ = s.Store.UpdateEmbedding(ctx, row.ID, row.MemoryUID,
				mem.EmbeddingModel, mem.EmbeddingVersion, "failed", nil, err.Error())
			continue
		}
		if err := s.Store.UpdateEmbedding(ctx, row.ID, row.MemoryUID,
			mem.EmbeddingModel, mem.EmbeddingVersion, "ready", vec, ""); err != nil {
			result.Failed++
			continue
		}
		result.Updated++
	}
	result.Remaining = total - result.Updated
	s.audit(ctx, mem, "", "", "", "reembed", "admin", fmt.Sprintf(
		"scanned=%d updated=%d failed=%d", result.Scanned, result.Updated, result.Failed))
	return result, nil
}

// Run maintains the entry table: expired rows become expired and soft-deleted
// rows are physically removed after a 30-day grace period.
func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.Store == nil {
		return nil
	}
	// Prepare the optional pgvector index before the first search so an
	// upgraded deployment gets the accelerator without waiting for a write.
	if err := s.Store.EnsureVectorIndexes(ctx); err != nil {
		slog.WarnContext(ctx, "memory: vector indexes could not be prepared", "error", err)
	}
	s.prune(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.prune(ctx)
		}
	}
}

// EnsureVectorIndex prepares the pgvector index for one embedding width. A
// Memory may be created before any entry is written; building the index then
// keeps the first search from paying for an empty index build.
func (s *Service) EnsureVectorIndex(ctx context.Context, dims int) error {
	if s == nil || s.Store == nil {
		return nil
	}
	return s.Store.EnsureVectorIndex(ctx, dims)
}

func (s *Service) prune(ctx context.Context) {
	now := time.Now().UnixMilli()
	if n, err := s.Store.PruneExpired(ctx, now, 1000); err != nil {
		slog.WarnContext(ctx, "memory: expired entries could not be pruned", "error", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "memory: expired entries pruned", "count", n)
	}
	const deletedGrace = 30 * 24 * time.Hour
	if n, err := s.Store.PurgeDeleted(ctx, now-deletedGrace.Milliseconds(), 1000); err != nil {
		slog.WarnContext(ctx, "memory: deleted entries could not be purged", "error", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "memory: deleted entries purged", "count", n)
	}
}

// bucketSubject is the storage key for one write or recall. A session-scoped
// Memory keys on the conversation; every other mode keys on the person.
func bucketSubject(mem *model.MemoryM, subject, sessionID string) string {
	if mem != nil && mem.SubjectMode == "session" && strings.TrimSpace(sessionID) != "" {
		return sessionID
	}
	return subject
}

func memoryName(mem *model.MemoryM) string {
	if mem == nil {
		return ""
	}
	return mem.Name
}

// Memory returns the Memory declaration with this name.
func (s *Service) Memory(ctx context.Context, name string) (*model.MemoryM, error) {
	if s == nil || s.Memories == nil {
		return nil, fmt.Errorf("memory is not configured")
	}
	row, err := s.Memories.Get(ctx, model.EntryFilter(name))
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("memory %q not found", name)
	}
	return row, nil
}

// Fact is one atomic memory extracted from a run.
type Fact struct {
	Content    string
	Type       string
	Importance float32
	Confidence float32
	Tags       []string
	Metadata   string
}

// Add writes one memory for a run or a tool.
func (s *Service) Add(
	ctx context.Context, memoryName, agent, agentNamespace, subject, sessionID, runID, content, source string,
) error {
	return s.AddFact(ctx, memoryName, agent, agentNamespace, subject, sessionID, runID, source, Fact{
		Content: content, Type: "episodic",
	})
}

// AddFact writes one typed, weighted fact.
func (s *Service) AddFact(
	ctx context.Context, memoryName, agent, agentNamespace, subject, sessionID, runID, source string, fact Fact,
) error {
	mem, err := s.Memory(ctx, memoryName)
	if err != nil {
		return err
	}
	_, err = s.Create(ctx, mem, CreateInput{
		Agent:          agent,
		AgentNamespace: agentNamespace,
		Subject:        subject,
		SessionID:      sessionID,
		RunID:          runID,
		Type:           fact.Type,
		Content:        fact.Content,
		Importance:     fact.Importance,
		Confidence:     fact.Confidence,
		Tags:           fact.Tags,
		Metadata:       fact.Metadata,
		Source:         source,
		Actor:          subject,
	})
	return err
}

// Create validates, redacts, deduplicates and stores one entry.
func (s *Service) Create(
	ctx context.Context, mem *model.MemoryM, in CreateInput,
) (*model.MemoryEntryM, error) {
	if mem == nil {
		return nil, fmt.Errorf("memory is not configured")
	}
	if !mem.Enabled {
		return nil, fmt.Errorf("memory %q is disabled", mem.Name)
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, fmt.Errorf("memory content is empty")
	}
	content, err := applyPII(content, mem)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("memory content is empty after PII redaction")
	}
	if err := checkPoison(content, mem); err != nil {
		return nil, err
	}

	sub := bucketSubject(mem, in.Subject, in.SessionID)
	kind := in.Type
	if kind == "" {
		kind = "semantic"
	}
	source := in.Source
	if source == "" {
		source = "manual"
	}
	importance := in.Importance
	if importance <= 0 {
		importance = 0.5
	}
	confidence := in.Confidence
	if confidence <= 0 {
		confidence = 0.5
	}
	dedupe := dedupeKey(mem, sub, kind, content)
	existing, lookupErr := s.Store.FindActive(ctx, memoryUID(mem), sub, dedupe)
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return nil, lookupErr
	}
	if existing != nil {
		if importance > existing.Importance {
			existing.Importance = importance
		}
		if confidence > existing.Confidence {
			existing.Confidence = confidence
		}
		existing.UpdatedAt = time.Now().UnixMilli()
		_ = s.Store.Update(ctx, existing)
		s.audit(ctx, mem, sub, existing.ID, in.Actor, "dedupe", source, "existing entry reused")
		return existing, nil
	}

	now := time.Now().UnixMilli()
	expiresAt := in.ExpiresAt
	if expiresAt == 0 && mem.TTLDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, int(mem.TTLDays)).UnixMilli()
	}
	if expiresAt == 0 && mem.TTLDays == 0 && s.DefaultTTLDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, int(s.DefaultTTLDays)).UnixMilli()
	}
	entry := &model.MemoryEntryM{
		ID:               newID(),
		Namespace:        mem.Namespace,
		Memory:           mem.Name,
		MemoryUID:        memoryUID(mem),
		Agent:            in.Agent,
		AgentNamespace:   in.AgentNamespace,
		Subject:          sub,
		SessionID:        in.SessionID,
		RunID:            in.RunID,
		Type:             kind,
		Status:           "active",
		Content:          content,
		DedupeKey:        dedupe,
		EmbeddingModel:   mem.EmbeddingModel,
		EmbeddingVersion: mem.EmbeddingVersion,
		EmbeddingStatus:  "pending",
		Source:           source,
		Actor:            in.Actor,
		Importance:       importance,
		Confidence:       confidence,
		ExpiresAt:        expiresAt,
		Tags:             encodeTags(in.Tags),
		Metadata:         in.Metadata,
		Version:          1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.Store.Create(ctx, entry); err != nil {
		return nil, err
	}
	s.audit(ctx, mem, sub, entry.ID, in.Actor, "create", source, "")
	s.embedAsync(ctx, mem, entry)
	return entry, nil
}

// Update changes the mutable fields of one entry and re-embeds its content.
func (s *Service) Update(
	ctx context.Context, mem *model.MemoryM, subject, id string, in UpdateInput,
) (*model.MemoryEntryM, error) {
	entry, err := s.Store.Get(ctx, mem.Namespace, mem.Name, subject, id)
	if err != nil {
		return nil, err
	}
	if in.Type != "" {
		entry.Type = in.Type
	}
	if strings.TrimSpace(in.Content) != "" {
		content, err := applyPII(strings.TrimSpace(in.Content), mem)
		if err != nil {
			return nil, err
		}
		if err := checkPoison(content, mem); err != nil {
			return nil, err
		}
		if content != entry.Content {
			entry.Content = content
			entry.DedupeKey = dedupeKey(mem, subject, entry.Type, content)
			entry.EmbeddingStatus = "pending"
			entry.EmbeddingModel = mem.EmbeddingModel
			entry.EmbeddingVersion = mem.EmbeddingVersion
		}
	}
	if in.Importance != nil {
		entry.Importance = *in.Importance
	}
	if in.Confidence != nil {
		entry.Confidence = *in.Confidence
	}
	if in.Tags != nil {
		entry.Tags = encodeTags(in.Tags)
	}
	if in.Metadata != "" {
		entry.Metadata = in.Metadata
	}
	if in.Status != "" {
		entry.Status = in.Status
	}
	if in.ExpiresAt != nil {
		entry.ExpiresAt = *in.ExpiresAt
	}
	entry.Version++
	entry.UpdatedAt = time.Now().UnixMilli()
	if err := s.Store.Update(ctx, entry); err != nil {
		return nil, err
	}
	s.audit(ctx, mem, subject, entry.ID, subject, "update", "manual", "")
	if entry.EmbeddingStatus == "pending" {
		s.embedAsync(ctx, mem, entry)
	}
	return entry, nil
}

// List returns one page of entries.
func (s *Service) List(
	ctx context.Context, mem *model.MemoryM, f memoryentry.ListFilter,
) (int64, []*model.MemoryEntryM, error) {
	f.Namespace = mem.Namespace
	f.Memory = mem.Name
	return s.Store.List(ctx, f)
}

// Get returns one entry owned by subject.
func (s *Service) Get(
	ctx context.Context, mem *model.MemoryM, subject, id string,
) (*model.MemoryEntryM, error) {
	return s.Store.Get(ctx, mem.Namespace, mem.Name, subject, id)
}

// Delete soft-deletes one entry.
func (s *Service) Delete(ctx context.Context, mem *model.MemoryM, subject, id string) error {
	if err := s.Store.SoftDelete(ctx, id, memoryUID(mem), subject); err != nil {
		return err
	}
	s.audit(ctx, mem, subject, id, subject, "delete", "manual", "")
	return nil
}

// Stats aggregates one memory.
func (s *Service) Stats(
	ctx context.Context, mem *model.MemoryM, agent, subject string,
) (*memoryentry.Stats, error) {
	return s.Store.Stats(ctx, mem.Namespace, mem.Name, agent, subject)
}

// Subjects aggregates memory owners.
func (s *Service) Subjects(
	ctx context.Context, mem *model.MemoryM, agent, q string, offset, limit int,
) (int64, []*memoryentry.Subject, error) {
	return s.Store.Subjects(ctx, mem.Namespace, mem.Name, agent, q, offset, limit)
}

// Purge removes every entry of one subject.
func (s *Service) Purge(ctx context.Context, mem *model.MemoryM, subject, actor string) (int64, error) {
	deleted, err := s.Store.Purge(ctx, mem.Namespace, mem.Name, subject)
	if err != nil {
		return 0, err
	}
	s.audit(ctx, mem, subject, "", actor, "purge", "admin", fmt.Sprintf("deleted=%d", deleted))
	return deleted, nil
}

// Search ranks the memory entries relevant to query.
func (s *Service) Search(
	ctx context.Context, mem *model.MemoryM, in SearchInput,
) (*SearchResult, error) {
	if !mem.Enabled {
		return nil, fmt.Errorf("memory %q is disabled", mem.Name)
	}
	sub := bucketSubject(mem, in.Subject, in.SessionID)
	start := time.Now()
	result := &SearchResult{}

	mode := in.Mode
	if mode == "" {
		mode = mem.RetrievalMode
	}
	if mode == "" {
		mode = "hybrid"
	}
	topK := in.TopK
	if topK <= 0 {
		topK = int(mem.TopK)
	}
	if topK <= 0 {
		topK = 8
	}

	var queryVec []float32
	if mode != "keyword" && mem.EmbeddingModel != "" && s.Embedder != nil && strings.TrimSpace(in.Query) != "" {
		vec, err := s.embedQuery(ctx, mem, in.Query)
		if err != nil {
			result.Degraded = true
		} else {
			queryVec = vec
		}
	} else if mode != "keyword" {
		result.Degraded = true
	}

	// With a query vector, pgvector narrows the pool to the nearest neighbours
	// plus lexical matches, so a subject with many entries costs the same as a
	// small one. Without it (keyword mode, or embeddings unavailable) the
	// bounded scan below is the only option.
	rows, err := s.Store.SearchCandidates(ctx, memoryentry.CandidateFilter{
		Namespace:    mem.Namespace,
		Memory:       mem.Name,
		Agent:        in.Agent,
		Subject:      sub,
		Vector:       queryVec,
		Terms:        queryTerms(in.Query),
		VectorLimit:  vectorCandidateLimit(topK),
		KeywordLimit: keywordCandidateLimit,
		ScanLimit:    s.MaxRows,
	})
	if err != nil {
		return nil, err
	}
	rows = filterRows(rows, in.Types, in.Tags)

	type ranked struct {
		row         *model.MemoryEntryM
		vector      float64
		vectorRank  int
		keyword     float64
		keywordRank int
	}
	vectorRanked := make([]ranked, 0, len(rows))
	keywordRanked := make([]ranked, 0, len(rows))

	for _, row := range rows {
		item := ranked{row: row}
		if len(queryVec) > 0 {
			vec := vector.Decode(row.Embedding)
			if len(vec) == len(queryVec) {
				item.vector = cosine(queryVec, vec)
			}
		}
		item.keyword = keywordScore(row.Content, in.Query)
		if item.vector > 0 {
			vectorRanked = append(vectorRanked, item)
		}
		if item.keyword > 0 {
			keywordRanked = append(keywordRanked, item)
		}
	}
	sort.SliceStable(vectorRanked, func(i, j int) bool { return vectorRanked[i].vector > vectorRanked[j].vector })
	sort.SliceStable(keywordRanked, func(i, j int) bool {
		if keywordRanked[i].keyword == keywordRanked[j].keyword {
			return keywordRanked[i].row.UpdatedAt > keywordRanked[j].row.UpdatedAt
		}
		return keywordRanked[i].keyword > keywordRanked[j].keyword
	})

	result.VectorCandidates = len(vectorRanked)
	result.KeywordCandidates = len(keywordRanked)

	merged := map[string]*Hit{}
	rrfK := 60.0
	for i := range vectorRanked {
		rrf := 0.5 / (rrfK + float64(i+1))
		hit := merged[vectorRanked[i].row.ID]
		if hit == nil {
			hit = &Hit{Entry: vectorRanked[i].row}
			merged[vectorRanked[i].row.ID] = hit
		}
		hit.Breakdown.RRF += rrf
		hit.Breakdown.VectorSimilarity = vectorRanked[i].vector
		hit.Breakdown.VectorRank = i + 1
	}
	for i := range keywordRanked {
		rrf := 0.5 / (rrfK + float64(i+1))
		hit := merged[keywordRanked[i].row.ID]
		if hit == nil {
			hit = &Hit{Entry: keywordRanked[i].row}
			merged[keywordRanked[i].row.ID] = hit
		}
		hit.Breakdown.RRF += rrf
		hit.Breakdown.KeywordRank = i + 1
	}
	for _, hit := range merged {
		hit.Breakdown.Recency = recency(hit.Entry.UpdatedAt)
		hit.Breakdown.Importance = float64(hit.Entry.Importance)
		hit.Breakdown.Confidence = float64(hit.Entry.Confidence)
		hit.Breakdown.Final = hit.Breakdown.RRF +
			0.15*hit.Breakdown.Recency +
			0.15*hit.Breakdown.Importance +
			0.10*hit.Breakdown.Confidence
		hit.Score = hit.Breakdown.Final
		hit.TokenCount = estimateTokens(hit.Entry.Content)
	}

	minScore := in.MinScore
	if minScore <= 0 {
		minScore = mem.MinScore
	}
	hits := make([]Hit, 0, len(merged))
	for _, hit := range merged {
		if minScore > 0 && hit.Score < minScore {
			continue
		}
		hits = append(hits, *hit)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	vectors := make(map[string][]float32, len(rows))
	for _, row := range rows {
		vectors[row.ID] = vector.Decode(row.Embedding)
	}
	hits = diversify(hits, vectors, topK)

	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = int(mem.MaxTokens)
	}
	if maxTokens <= 0 {
		maxTokens = 1200
	}
	total := 0
	filtered := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		if total+hit.TokenCount > maxTokens {
			break
		}
		total += hit.TokenCount
		filtered = append(filtered, hit)
	}
	result.Hits = filtered
	result.TokenCount = total
	result.TookMs = time.Since(start).Milliseconds()

	if in.Persist && len(filtered) > 0 {
		ids := make([]string, 0, len(filtered))
		for _, hit := range filtered {
			ids = append(ids, hit.Entry.ID)
		}
		_ = s.Store.Touch(ctx, ids)
	}
	// Retrieval tests opt into explain; runtime prefetch does not, so only the
	// operator-driven path writes an audit row. The query itself is hashed.
	if in.Explain {
		actor := CallerFromContext(ctx)
		if actor == "" {
			actor = "runtime"
		}
		s.audit(ctx, mem, sub, "", actor, "search", "api", fmt.Sprintf(
			"query_hash=%s results=%d degraded=%t", hashText(in.Query), len(filtered), result.Degraded))
	}
	return result, nil
}

// mmrLambda trades relevance for diversity. 0.7 keeps relevance dominant while
// still pushing a second copy of the same fact down the list.
const mmrLambda = 0.7

// diversify applies maximal marginal relevance to the ranked hits. It is a
// no-op when there are no embeddings (keyword-only fallback) or when the pool
// already fits the budget: MMR can only reorder, never add rows.
func diversify(hits []Hit, vectors map[string][]float32, topK int) []Hit {
	if topK <= 0 || len(hits) <= topK {
		return hits
	}
	if len(vectors) == 0 {
		return hits[:topK]
	}
	maxScore, minScore := hits[0].Score, hits[len(hits)-1].Score
	norm := func(score float64) float64 {
		if maxScore <= minScore {
			return 1
		}
		return (score - minScore) / (maxScore - minScore)
	}

	selected := make([]Hit, 0, topK)
	remaining := append([]Hit(nil), hits...)
	for len(selected) < topK && len(remaining) > 0 {
		best := 0
		bestScore := math.Inf(-1)
		for i, candidate := range remaining {
			candidateVec := vectors[candidate.Entry.ID]
			var maxSim float64
			if len(candidateVec) > 0 {
				for _, chosen := range selected {
					chosenVec := vectors[chosen.Entry.ID]
					if len(chosenVec) != len(candidateVec) {
						continue
					}
					if sim := cosine(candidateVec, chosenVec); sim > maxSim {
						maxSim = sim
					}
				}
			}
			score := mmrLambda*norm(candidate.Score) - (1-mmrLambda)*maxSim
			if score > bestScore {
				bestScore = score
				best = i
			}
		}
		selected = append(selected, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return selected
}

// keywordCandidateLimit bounds the lexical side of an indexed search. The rows
// are still ranked by term frequency afterwards; this only decides who enters
// the ranking.
const keywordCandidateLimit = 200

// vectorCandidateLimit sizes the nearest-neighbour pool. Retrieval reranks the
// pool with recency, importance and MMR, so it needs headroom beyond topK.
func vectorCandidateLimit(topK int) int {
	limit := topK * 8
	if limit < 64 {
		limit = 64
	}
	if limit > 500 {
		limit = 500
	}
	return limit
}

// queryTerms splits a query the same way keywordScore does, so the SQL
// prefilter and the in-process score agree on what a term is.
func queryTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, len(fields))
	for _, term := range fields {
		if len([]rune(term)) < 2 {
			continue
		}
		out = append(out, term)
	}
	return out
}

// keywordScore is a simple term-frequency score. It is intentionally not a
// full BM25 index: the portable fallback runs on SQLite/MySQL too, and the
// vector branch is what carries semantic recall.
func keywordScore(content, query string) float64 {
	haystack := strings.ToLower(content)
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 0
	}
	var score float64
	for _, term := range terms {
		if len([]rune(term)) < 2 {
			continue
		}
		score += float64(strings.Count(haystack, term))
	}
	return score
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:8])
}

// SearchByName is the run-facing recall helper.
func (s *Service) SearchByName(
	ctx context.Context, memoryName, agent, subject, sessionID, query string, limit int,
) ([]Hit, error) {
	mem, err := s.Memory(ctx, memoryName)
	if err != nil {
		return nil, err
	}
	result, err := s.Search(ctx, mem, SearchInput{
		Agent: agent, Subject: subject, SessionID: sessionID,
		Query: query, TopK: limit, Persist: true,
	})
	if err != nil {
		return nil, err
	}
	return result.Hits, nil
}

// Prefetch returns a compact block of relevant memories for a run's first
// message. Empty means nothing relevant was found, which is not an error.
func (s *Service) Prefetch(
	ctx context.Context, memoryName, agent, subject, sessionID, query string, maxChars int,
) (string, error) {
	mem, err := s.Memory(ctx, memoryName)
	if err != nil {
		return "", err
	}
	result, err := s.Search(ctx, mem, SearchInput{
		Agent: agent, Subject: subject, SessionID: sessionID,
		Query: query, TopK: 5, Persist: true,
	})
	if err != nil {
		return "", err
	}
	if len(result.Hits) == 0 {
		return "", nil
	}
	if maxChars <= 0 {
		maxChars = 2000
	}
	var b strings.Builder
	b.WriteString("Relevant memories from earlier conversations:\n")
	for _, hit := range result.Hits {
		line := "- " + strings.TrimSpace(hit.Entry.Content) + "\n"
		if b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
	}
	return b.String(), nil
}

func (s *Service) embedAsync(ctx context.Context, mem *model.MemoryM, entry *model.MemoryEntryM) {
	if s.Embedder == nil || mem.EmbeddingModel == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	safego.Go(ctx, "memory embedding", func() {
		vec, err := s.embed(ctx, mem, entry.Content)
		if err != nil {
			_ = s.Store.UpdateEmbedding(ctx, entry.ID, entry.MemoryUID,
				mem.EmbeddingModel, mem.EmbeddingVersion, "failed", nil, err.Error())
			return
		}
		_ = s.Store.UpdateEmbedding(ctx, entry.ID, entry.MemoryUID,
			mem.EmbeddingModel, mem.EmbeddingVersion, "ready", vec, "")
		// Keep the in-memory copy in step with the row: a later Update selects
		// embedding_status, and writing "pending" back would hide the vector.
		entry.EmbeddingStatus = "ready"
		entry.Embedding = vector.Encode(vec)
		// A new embedding width gets its own partial HNSW index. This is a
		// no-op after the first entry of that width.
		if err := s.Store.EnsureVectorIndex(ctx, len(vec)); err != nil {
			slog.WarnContext(ctx, "memory: the vector index could not be created",
				"dimensions", len(vec), "error", err)
		}
		s.mergeNearDuplicates(ctx, mem, entry, vec)
		s.enforceQuota(ctx, mem, entry.Subject)
	})
}

// nearDuplicateThreshold is where two facts are treated as one. It is high on
// purpose: merging two different preferences is worse than keeping both.
const nearDuplicateThreshold = 0.92

// mergeNearDuplicates merges the new entry with at most one active entry of the
// same type whose embedding is close enough. The longer content wins; importance,
// confidence and tags are unioned; the loser is archived rather than deleted.
func (s *Service) mergeNearDuplicates(
	ctx context.Context, mem *model.MemoryM, entry *model.MemoryEntryM, embedding []float32,
) {
	if len(embedding) == 0 {
		return
	}
	rows, err := s.Store.Candidates(ctx, mem.Namespace, mem.Name, entry.Agent, entry.Subject, 100)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.ID == entry.ID || row.Type != entry.Type {
			continue
		}
		other := vector.Decode(row.Embedding)
		if len(other) != len(embedding) || cosine(embedding, other) < nearDuplicateThreshold {
			continue
		}
		// The newer, longer content is the survivor; it is what the user just
		// said. The older row contributes its confidence and tags.
		if len([]rune(entry.Content)) >= len([]rune(row.Content)) {
			entry.Importance = maxFloat32(entry.Importance, row.Importance)
			entry.Confidence = maxFloat32(entry.Confidence, row.Confidence)
			entry.Tags = mergeTags(entry.Tags, row.Tags)
			entry.UpdatedAt = time.Now().UnixMilli()
			_ = s.Store.Update(ctx, entry)
			_ = s.Store.Archive(ctx, []string{row.ID})
		} else {
			row.Importance = maxFloat32(entry.Importance, row.Importance)
			row.Confidence = maxFloat32(entry.Confidence, row.Confidence)
			row.Tags = mergeTags(row.Tags, entry.Tags)
			row.UpdatedAt = time.Now().UnixMilli()
			_ = s.Store.Update(ctx, row)
			_ = s.Store.Archive(ctx, []string{entry.ID})
		}
		return
	}
}

// enforceQuota archives the entries above the per-subject cap, keeping the most
// important and most recently updated ones.
func (s *Service) enforceQuota(ctx context.Context, mem *model.MemoryM, subject string) {
	if mem.MaxEntriesPerSubject <= 0 {
		return
	}
	ids, err := s.Store.QuotaVictims(ctx, mem.Namespace, mem.Name, subject, int(mem.MaxEntriesPerSubject))
	if err != nil || len(ids) == 0 {
		return
	}
	_ = s.Store.Archive(ctx, ids)
}

// audit appends one mutation. A failed audit never fails the memory write: the
// content is already durable, and losing the trail is a problem to alert on,
// not a reason to tell the caller the write failed.
func (s *Service) audit(
	ctx context.Context, mem *model.MemoryM, subject, entryID, actor, action, source, reason string,
) {
	if s.Audit == nil || mem == nil {
		return
	}
	if actor == "" {
		actor = subject
	}
	event := &model.MemoryAuditM{
		ID:        newID(),
		Namespace: mem.Namespace,
		Memory:    mem.Name,
		MemoryUID: memoryUID(mem),
		Subject:   subject,
		EntryID:   entryID,
		Actor:     actor,
		Action:    action,
		Source:    source,
		Reason:    reason,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.Audit.Create(ctx, event); err != nil {
		slog.WarnContext(ctx, "memory: the audit event could not be written",
			"memory", mem.Name, "action", action, "error", err)
	}
}

func maxFloat32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func mergeTags(a, b string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, raw := range []string{a, b} {
		for _, tag := range decodeTags(raw) {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return encodeTags(out)
}

func (s *Service) embedQuery(ctx context.Context, mem *model.MemoryM, query string) ([]float32, error) {
	return s.embed(ctx, mem, query)
}

func (s *Service) embed(ctx context.Context, mem *model.MemoryM, text string) ([]float32, error) {
	baseURL, apiKey, modelName, err := s.Models.EmbeddingEndpoint(ctx, mem.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	vectors, err := s.Embedder.Embed(ctx, baseURL, apiKey, modelName, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("embedding returned no vector")
	}
	if mem.EmbeddingDimensions > 0 && len(vectors[0]) != int(mem.EmbeddingDimensions) {
		return nil, fmt.Errorf("embedding returned %d dimensions, want %d",
			len(vectors[0]), mem.EmbeddingDimensions)
	}
	return vectors[0], nil
}

func filterRows(rows []*model.MemoryEntryM, types, tags []string) []*model.MemoryEntryM {
	typeSet := map[string]bool{}
	for _, t := range types {
		typeSet[t] = true
	}
	tagSet := map[string]bool{}
	for _, t := range tags {
		tagSet[t] = true
	}
	if len(typeSet) == 0 && len(tagSet) == 0 {
		return rows
	}
	out := rows[:0]
	for _, row := range rows {
		if len(typeSet) > 0 && !typeSet[row.Type] {
			continue
		}
		if len(tagSet) > 0 {
			rowTags := decodeTags(row.Tags)
			ok := false
			for _, tag := range rowTags {
				if tagSet[tag] {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, row)
	}
	return out
}

func recency(updatedAt int64) float64 {
	if updatedAt <= 0 {
		return 0
	}
	days := time.Since(time.UnixMilli(updatedAt)).Hours() / 24
	return math.Exp(-math.Ln2 * days / 30)
}

func estimateTokens(content string) int {
	tokens := len([]rune(content)) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func dedupeKey(mem *model.MemoryM, subject, kind, content string) string {
	raw := strings.Join([]string{
		mem.Namespace, mem.Name, subject, kind,
		strings.Join(strings.Fields(strings.ToLower(content)), " "),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func memoryUID(mem *model.MemoryM) string {
	if mem.UID != "" {
		return mem.UID
	}
	return mem.Name
}

func newID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro()%1_000_000)
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	raw, _ := json.Marshal(tags)
	return string(raw)
}

func decodeTags(raw string) []string {
	var tags []string
	_ = json.Unmarshal([]byte(raw), &tags)
	return tags
}

func cosine(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

var (
	emailPattern    = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	phonePattern    = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	idCardPattern   = regexp.MustCompile(`\b\d{17}[\dXx]\b`)
	bankCardPattern = regexp.MustCompile(`\b\d{16,19}\b`)
	apiKeyPattern   = regexp.MustCompile(`\b(sk|ak|pk|ghp|glpat)[-_][A-Za-z0-9\-_]{12,}\b`)
)

func applyPII(content string, mem *model.MemoryM) (string, error) {
	if mem == nil || !mem.PIIRedactionEnabled {
		return content, nil
	}
	patterns := []*regexp.Regexp{emailPattern, phonePattern, idCardPattern, bankCardPattern, apiKeyPattern}
	strategy := mem.PIIStrategy
	if strategy == "" {
		strategy = "redact"
	}
	found := false
	for _, pattern := range patterns {
		if pattern.MatchString(content) {
			found = true
			break
		}
	}
	if !found {
		return content, nil
	}
	switch strategy {
	case "drop":
		return "", fmt.Errorf("memory content contains PII and the drop strategy is set")
	case "hash":
		for _, pattern := range patterns {
			content = pattern.ReplaceAllStringFunc(content, func(value string) string {
				sum := sha256.Sum256([]byte(value))
				return "[HASH:" + hex.EncodeToString(sum[:4]) + "]"
			})
		}
	default:
		for _, pattern := range patterns {
			content = pattern.ReplaceAllString(content, "[REDACTED]")
		}
	}
	return content, nil
}

var poisonMarkers = []string{
	"ignore previous instructions",
	"ignore all previous",
	"<|im_start|>",
	"<|system|>",
	"</system>",
	"<script",
	"system:",
	"you are now",
}

func checkPoison(content string, mem *model.MemoryM) error {
	if mem == nil || !mem.BlockInstructionLike {
		return nil
	}
	lower := strings.ToLower(content)
	for _, marker := range poisonMarkers {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("memory content looks like prompt instructions and was rejected")
		}
	}
	return nil
}
