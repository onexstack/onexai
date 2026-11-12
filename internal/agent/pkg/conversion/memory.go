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

package conversion

import (
	"encoding/json"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// MemoryV1ToMemoryModel converts a proto Memory to the domain model.
func MemoryV1ToMemoryModel(p *v1.Memory) *model.MemoryM {
	if p == nil {
		return &model.MemoryM{}
	}
	return &model.MemoryM{
		Name:                  p.GetName(),
		Namespace:             p.GetNamespace(),
		DisplayName:           p.GetDisplayName(),
		Description:           p.GetDescription(),
		Status:                p.GetStatus(),
		Enabled:               p.GetEnabled(),
		Owner:                 p.GetOwner(),
		EmbeddingModel:        p.GetEmbeddingModel(),
		EmbeddingDimensions:   p.GetEmbeddingDimensions(),
		EmbeddingVersion:      p.GetEmbeddingVersion(),
		ExtractionEnabled:     p.GetExtractionEnabled(),
		ExtractionModel:       p.GetExtractionModel(),
		MaxFactsPerRun:        p.GetMaxFactsPerRun(),
		TTLDays:               p.GetTtlDays(),
		MaxEntriesPerSubject:  p.GetMaxEntriesPerSubject(),
		RetrievalMode:         p.GetRetrievalMode(),
		TopK:                  p.GetTopK(),
		MinScore:              p.GetMinScore(),
		MaxTokens:             p.GetMaxTokens(),
		PIIRedactionEnabled:   p.GetPiiRedactionEnabled(),
		PIIStrategy:           p.GetPiiStrategy(),
		BlockInstructionLike:  p.GetBlockInstructionLike(),
		AllowAdminContentRead: p.GetAllowAdminContentRead(),
		SubjectMode:           p.GetSubjectMode(),
		Phase:                 p.GetPhase(),
		Labels:                p.GetLabels(),
		CreatedAt:             p.GetCreatedAt(),
		UpdatedAt:             p.GetUpdatedAt(),
	}
}

// MemoryModelToMemoryV1 converts the domain model back to the wire shape.
func MemoryModelToMemoryV1(m *model.MemoryM) *v1.Memory {
	if m == nil {
		return nil
	}
	return &v1.Memory{
		Name:                  m.Name,
		Namespace:             m.Namespace,
		DisplayName:           m.DisplayName,
		Description:           m.Description,
		Status:                StatusToV1(m.Status),
		Enabled:               m.Enabled,
		Owner:                 m.Owner,
		EmbeddingModel:        m.EmbeddingModel,
		EmbeddingDimensions:   m.EmbeddingDimensions,
		EmbeddingVersion:      m.EmbeddingVersion,
		ExtractionEnabled:     m.ExtractionEnabled,
		ExtractionModel:       m.ExtractionModel,
		MaxFactsPerRun:        m.MaxFactsPerRun,
		TtlDays:               m.TTLDays,
		MaxEntriesPerSubject:  m.MaxEntriesPerSubject,
		RetrievalMode:         m.RetrievalMode,
		TopK:                  m.TopK,
		MinScore:              m.MinScore,
		MaxTokens:             m.MaxTokens,
		PiiRedactionEnabled:   m.PIIRedactionEnabled,
		PiiStrategy:           m.PIIStrategy,
		BlockInstructionLike:  m.BlockInstructionLike,
		AllowAdminContentRead: m.AllowAdminContentRead,
		SubjectMode:           m.SubjectMode,
		Phase:                 m.Phase,
		Labels:                m.Labels,
		CreatedAt:             m.CreatedAt,
		UpdatedAt:             m.UpdatedAt,
	}
}

// MemoryEntryV1ToModel converts a proto entry to the domain model.
func MemoryEntryV1ToModel(p *v1.MemoryEntry) *model.MemoryEntryM {
	if p == nil {
		return &model.MemoryEntryM{}
	}
	tags, _ := json.Marshal(p.GetTags())
	return &model.MemoryEntryM{
		ID:               p.GetId(),
		Namespace:        p.GetNamespace(),
		Memory:           p.GetMemory(),
		Agent:            p.GetAgent(),
		Subject:          p.GetSubject(),
		SessionID:        p.GetSessionId(),
		RunID:            p.GetRunId(),
		Type:             p.GetType(),
		Status:           p.GetStatus(),
		Content:          p.GetContent(),
		EmbeddingModel:   p.GetEmbeddingModel(),
		EmbeddingVersion: p.GetEmbeddingVersion(),
		EmbeddingStatus:  p.GetEmbeddingStatus(),
		Source:           p.GetSource(),
		Actor:            p.GetActor(),
		Importance:       p.GetImportance(),
		Confidence:       p.GetConfidence(),
		AccessCount:      p.GetAccessCount(),
		LastAccessedAt:   p.GetLastAccessedAt(),
		ExpiresAt:        p.GetExpiresAt(),
		Tags:             string(tags),
		Metadata:         p.GetMetadata(),
		Version:          p.GetVersion(),
		CreatedAt:        p.GetCreatedAt(),
		UpdatedAt:        p.GetUpdatedAt(),
	}
}

// MemoryEntryModelToV1 converts the domain model back to the wire shape.
func MemoryEntryModelToV1(m *model.MemoryEntryM) *v1.MemoryEntry {
	if m == nil {
		return nil
	}
	var tags []string
	_ = json.Unmarshal([]byte(m.Tags), &tags)
	return &v1.MemoryEntry{
		Id:               m.ID,
		Namespace:        m.Namespace,
		Memory:           m.Memory,
		Agent:            m.Agent,
		AgentNamespace:   m.AgentNamespace,
		Subject:          m.Subject,
		SessionId:        m.SessionID,
		RunId:            m.RunID,
		Type:             m.Type,
		Status:           m.Status,
		Content:          m.Content,
		EmbeddingModel:   m.EmbeddingModel,
		EmbeddingVersion: m.EmbeddingVersion,
		EmbeddingStatus:  m.EmbeddingStatus,
		Source:           m.Source,
		Actor:            m.Actor,
		Importance:       m.Importance,
		Confidence:       m.Confidence,
		AccessCount:      m.AccessCount,
		LastAccessedAt:   m.LastAccessedAt,
		ExpiresAt:        m.ExpiresAt,
		Tags:             tags,
		Metadata:         m.Metadata,
		Version:          m.Version,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}
