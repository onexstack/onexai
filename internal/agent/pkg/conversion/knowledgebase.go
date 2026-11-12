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
	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// KnowledgeBaseV1ToKnowledgeBaseModel converts a proto knowledge base to the
// domain model.
func KnowledgeBaseV1ToKnowledgeBaseModel(p *v1.KnowledgeBase) *model.KnowledgeBaseM {
	if p == nil {
		return &model.KnowledgeBaseM{}
	}
	return &model.KnowledgeBaseM{
		Name:           p.GetName(),
		DisplayName:    p.GetDisplayName(),
		Description:    p.GetDescription(),
		Status:         p.GetStatus(),
		Platform:       p.GetPlatform(),
		DatasetID:      p.GetDatasetId(),
		ScoreThreshold: p.GetScoreThreshold(),
		TopK:           int(p.GetTopK()),
		Namespace:      p.GetNamespace(),
		Labels:         p.GetLabels(),
		CreatedAt:      p.GetCreatedAt(),
		UpdatedAt:      p.GetUpdatedAt(),
	}
}

// KnowledgeBaseModelToKnowledgeBaseV1 converts the domain model back to the
// wire shape.
func KnowledgeBaseModelToKnowledgeBaseV1(k *model.KnowledgeBaseM) *v1.KnowledgeBase {
	if k == nil {
		return nil
	}
	return &v1.KnowledgeBase{
		Name:           k.Name,
		DisplayName:    k.DisplayName,
		Description:    k.Description,
		Status:         StatusToV1(k.Status),
		Platform:       k.Platform,
		DatasetId:      k.DatasetID,
		ScoreThreshold: k.ScoreThreshold,
		TopK:           int32(k.TopK),
		Namespace:      k.Namespace,
		Labels:         k.Labels,
		CreatedAt:      k.CreatedAt,
		UpdatedAt:      k.UpdatedAt,
	}
}
