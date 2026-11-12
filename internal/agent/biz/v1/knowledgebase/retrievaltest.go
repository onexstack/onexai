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

package knowledgebase

import (
	"context"
	"errors"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestRetrieval implements KnowledgeBaseBiz.
//
// The split follows the repository's other "test" endpoints
// (POST /v1/agent/models/test, POST /v1/agent/mcpservers/test): a request-shape
// problem is an HTTP error, while a platform that could not answer is a
// successful call with ok=false. The console shows the message inline either
// way, and a 200 keeps "RAGFlow rejected this dataset" distinguishable from
// "the control plane is broken".
func (b *knowledgeBaseBiz) TestRetrieval(
	ctx context.Context, req *v1.TestKnowledgeBaseRetrievalRequest,
) (*v1.TestKnowledgeBaseRetrievalResponse, error) {
	row, err := b.store.KnowledgeBases().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "knowledge base %q not found", req.GetName()); err != nil {
		return nil, err
	}

	result, err := b.tester.Test(ctx, row, req.GetQuery(), kbtool.TestOptions{
		TopK:                   int(req.GetTopK()),
		ScoreThreshold:         req.ScoreThreshold,
		VectorSimilarityWeight: req.VectorSimilarityWeight,
		Rerank:                 req.Rerank,
		Keyword:                req.Keyword,
	})

	if err != nil {
		return b.testFailure(err)
	}

	hits := make([]*v1.KnowledgeBaseRetrievalHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		hits = append(hits, &v1.KnowledgeBaseRetrievalHit{
			ChunkId:      hit.ChunkID,
			DocumentName: documentNameOf(hit),
			Content:      hit.Content,
			Score:        hit.Score,
			VectorScore:  hit.VectorScore,
		})
	}

	return &v1.TestKnowledgeBaseRetrievalResponse{
		Ok:                     result.OK,
		Message:                result.Message,
		Hits:                   hits,
		TookMs:                 result.TookMs,
		TopK:                   int32(result.Effective.TopK), //nolint:gosec // bounded by maxTestTopK
		ScoreThreshold:         result.Effective.SimilarityThreshold,
		VectorSimilarityWeight: result.Effective.VectorSimilarityWeight,
		Rerank:                 result.Effective.Rerank,
		Keyword:                result.Effective.Keyword,
		Platform:               model.PlatformRAGFlow,
		RerankModel:            result.Effective.RerankModel,
	}, nil
}

// testFailure maps the tester's failures onto the API's vocabulary.
//
// A deployment that has no RAGFlow endpoint, or no rerank model when one was
// asked for, is not a caller error: the request is exactly what the console
// sends, and the answer is "this deployment cannot run it". Those stay 200 with
// ok=false, like every other test outcome, so the panel renders one consistent
// inline message instead of switching between an alert and an error page.
func (b *knowledgeBaseBiz) testFailure(err error) (*v1.TestKnowledgeBaseRetrievalResponse, error) {
	switch {
	case errors.Is(err, kbtool.ErrRetrievalNotConfigured):
		return &v1.TestKnowledgeBaseRetrievalResponse{ //nolint:exhaustruct_v5 // ok=false is the zero value
			Message:  "This deployment has no RAGFlow endpoint configured (agent.ragflow.base-url).",
			Platform: model.PlatformRAGFlow,
		}, nil
	case errors.Is(err, kbtool.ErrRerankNotConfigured):
		return &v1.TestKnowledgeBaseRetrievalResponse{ //nolint:exhaustruct_v5 // ok=false is the zero value
			Message:  "This deployment has no rerank model configured (agent.ragflow.rerank-id).",
			Platform: model.PlatformRAGFlow,
		}, nil
	case errors.Is(err, kbtool.ErrNotRAGFlow):
		return nil, errno.ErrKnowledgeBaseRetrievalUnsupported
	case errors.Is(err, kbtool.ErrMissingDataset), errors.Is(err, kbtool.ErrEmptyQuery):
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	default:
		return nil, err
	}
}

// documentNameOf picks the name a person can act on: the readable name when
// the platform reported one, otherwise its document key, which is the only
// identifier the platform gave us.
func documentNameOf(hit kbtool.Document) string {
	if hit.Source != "" {
		return hit.Source
	}
	return hit.DocumentKey
}
