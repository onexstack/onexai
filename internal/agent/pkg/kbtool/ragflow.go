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

package kbtool

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	kbclient "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/kb"
)

// RAGFlowConfig configures the RAGFlow knowledge-base platform. The endpoint and
// key are shared by every RAGFlow knowledge base in a deployment; each record
// contributes only its dataset id and retrieval knobs.
//
// There is nothing to fill in per call: RAGFlow authenticates with a
// tenant-wide key and has no notion of an end user, so the set of datasets in
// the request is the whole of a run's scope. See RAGFlowOptions for what that
// means for permission filtering.
type RAGFlowConfig struct {
	// BaseURL is the RAGFlow API root, e.g. "http://ragflow.example.com:9380".
	// The "/api/v1" path is appended per request.
	BaseURL string
	// APIKey authenticates against BaseURL.
	APIKey string
	// SimilarityThreshold is the minimum similarity a chunk must score, used
	// when a record does not set its own. A record's non-zero score threshold
	// wins over this value. <=0 leaves RAGFlow's own default in place.
	SimilarityThreshold float64
	// VectorSimilarityWeight weights vector cosine similarity against term
	// similarity. <=0 leaves RAGFlow's own default in place.
	VectorSimilarityWeight float64
	// RerankID names a rerank model. Empty disables reranking.
	RerankID string
	// Keyword asks RAGFlow to extract keywords from the question and search
	// with them in addition to the question itself. It costs one extra model
	// call on the platform's side and needs that tenant to have a chat model.
	Keyword bool
	// DocumentNameAsSource reports the platform's document name as the
	// passage's Source, so the model is shown it.
	//
	// Off by default, and the default is not an oversight: RAGFlow identifies a
	// document by a name its *upstream sync* chooses, and a sync that names
	// documents after their own identifiers (a node id, a uuid) leaves nothing
	// here a model could read — the field would then carry a token in the place
	// a person-facing name goes, which is worse than empty. A deployment whose
	// documents are named for humans turns this on and gets the chapter title
	// for free, because RAGFlow already returns it per chunk.
	DocumentNameAsSource bool
	// Timeout bounds one request. <=0 means 30s.
	Timeout time.Duration
	// HTTPClient overrides the default client (tests, proxies).
	HTTPClient *http.Client
}

// ragflowRetriever queries a set of RAGFlow datasets that share one retrieval
// setting.
//
// One retriever may cover several datasets: the retrieval API takes a list of
// dataset ids, so records that chose the same threshold and Top K make one call
// rather than one each. Records with different settings get their own
// retriever, because both knobs are per-request in the RAGFlow API.
type ragflowRetriever struct {
	datasetIDs []string
	names      []string
	// byDataset maps a dataset id to the display name of the record that owns
	// it, so a returned chunk can be attributed to the knowledge base it came
	// from. Name(), a comma-joined list, cannot do that.
	byDataset map[string]string
	// recordTopK is the record's own cap on how many passages this call may
	// return. 0 means the record names no count and the tool's request applies.
	recordTopK int
	cfg        RAGFlowConfig
	client     *kbclient.Client
}

// ragflowQueryKnobs is the effective retrieval setting a group of records chose.
// Records with equal knobs share one request.
type ragflowQueryKnobs struct {
	threshold float64
	topK      int
}

// ragflowGroup is one RAGFlow request: its setting and the datasets it covers.
type ragflowGroup struct {
	knobs     ragflowQueryKnobs
	ids       []string
	names     []string
	byDataset map[string]string
}

// NewRAGFlowRetrievers builds one retriever per distinct retrieval setting among
// the given knowledge bases.
//
// The common case stays one request: records that left both knobs unset, or
// that repeat the deployment threshold, all land in the same group. Only a
// record that chose its own threshold or Top K splits off, and it splits off
// with exactly those values.
func NewRAGFlowRetrievers(kbs []*model.KnowledgeBaseM, cfg RAGFlowConfig) []Retriever {
	groups := ragflowGroups(kbs, cfg)
	out := make([]Retriever, 0, len(groups))
	for _, g := range groups {
		groupCfg := cfg
		groupCfg.SimilarityThreshold = g.knobs.threshold
		out = append(out, newRAGFlowRetriever(g.ids, g.names, g.byDataset, groupCfg, g.knobs.topK))
	}
	return out
}

// newRAGFlowRetriever builds a retriever over an explicit dataset set.
//
// It exists separately from NewRAGFlowRetrievers so the retrieval-test path can
// cover exactly one record — including a disabled one, which is a configuration
// the test exists to diagnose — without going through ragflowGroups's
// enabled-only filter.
func newRAGFlowRetriever(
	ids, names []string, byDataset map[string]string, cfg RAGFlowConfig, recordTopK int,
) *ragflowRetriever {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &ragflowRetriever{
		datasetIDs: ids,
		names:      names,
		byDataset:  byDataset,
		recordTopK: recordTopK,
		cfg:        cfg,
		client:     newRAGFlowClient(cfg),
	}
}

// newRAGFlowClient builds the HTTP client one RAGFlow call uses.
func newRAGFlowClient(cfg RAGFlowConfig) *kbclient.Client {
	if cfg.HTTPClient != nil {
		return kbclient.NewWithHTTPClient(cfg.HTTPClient)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return kbclient.NewForConfig(timeout)
}

// Name lists the knowledge bases this retriever covers.
func (r *ragflowRetriever) Name() string {
	if len(r.names) == 0 {
		return "ragflow"
	}
	return strings.Join(r.names, ", ")
}

// Retrieve runs one RAGFlow search across this retriever's datasets.
func (r *ragflowRetriever) Retrieve(ctx context.Context, query string, topK int) ([]Document, error) {
	// The record may narrow the count, never widen it: the tool's request is the
	// run's budget and the deployment's global top-k is its ceiling.
	topK = limitTopK(r.recordTopK, topK)
	if topK <= 0 {
		topK = defaultTopK
	}

	url := strings.TrimRight(r.cfg.BaseURL, "/") + "/api/v1/retrieval"
	var parsed ragflowResponse
	request := ragflowRequest{
		Question:   query,
		DatasetIDs: r.datasetIDs,
		// PageSize is what bounds how many chunks come back; knn_top_k is left
		// at RAGFlow's own default because it sizes the candidate pool the
		// vector search considers, and narrowing it to the page we want would
		// trade recall for nothing. The two are different knobs and only one of
		// them is ours to set.
		PageSize: topK,
		// RerankCandidatesCount must be at least page*page_size, and defaults
		// to 64. Asking for more than that in one page would otherwise be
		// rejected by the platform.
		RerankCandidatesCount:  max(topK, 64),
		SimilarityThreshold:    r.cfg.SimilarityThreshold,
		VectorSimilarityWeight: r.cfg.VectorSimilarityWeight,
		RerankID:               r.cfg.RerankID,
		Keyword:                r.cfg.Keyword,
	}
	if err := postJSON(ctx, r.client, url,
		map[string]string{"Authorization": "Bearer " + r.cfg.APIKey}, request, &parsed,
		postLabels{
			Prefix:      "kbtool/ragflow",
			Call:        "search " + r.Name(),
			StatusLabel: r.Name(),
		}); err != nil {
		return nil, err
	}
	// RAGFlow reports a business failure in the body with HTTP 200 — a missing
	// dataset id, an expired key and "no such dataset" all arrive this way. Not
	// checking Code would turn every one of them into an empty result set, which
	// the model reports as "the knowledge base has nothing to say".
	if parsed.Code != 0 {
		return nil, fmt.Errorf("kbtool/ragflow: %s returned code %d: %s",
			r.Name(), parsed.Code, parsed.Message)
	}
	return r.attribute(parsed.chunks()), nil
}

// Wire types for the RAGFlow retrieval API.

type ragflowRequest struct {
	Question               string   `json:"question"`
	DatasetIDs             []string `json:"dataset_ids"`
	PageSize               int      `json:"page_size"`
	RerankCandidatesCount  int      `json:"rerank_candidates_count"`
	SimilarityThreshold    float64  `json:"similarity_threshold,omitempty"`
	VectorSimilarityWeight float64  `json:"vector_similarity_weight,omitempty"`
	RerankID               string   `json:"rerank_id,omitempty"`
	Keyword                bool     `json:"keyword,omitempty"`
}

type ragflowResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Chunks []ragflowChunk `json:"chunks"`
	} `json:"data"`
}

// ragflowChunk is one retrieved chunk. dataset_id is what ties a chunk back to
// the knowledge base it came from; id is the chunk's own identity. Both are
// returned by the platform and were previously dropped.
type ragflowChunk struct {
	ID               string  `json:"id"`
	Content          string  `json:"content"`
	DocumentKeyword  string  `json:"document_keyword"`
	DocumentID       string  `json:"document_id"`
	DatasetID        string  `json:"dataset_id"`
	Similarity       float64 `json:"similarity"`
	VectorSimilarity float64 `json:"vector_similarity"`
}

// chunks returns the response's chunks, or nil when there are none.
func (r ragflowResponse) chunks() []ragflowChunk { return r.Data.Chunks }

// attribute converts the chunks into the neutral shape and stamps each with the
// knowledge base that owns its dataset. The chunk's similarity is the platform's
// own blend of vector and term match, which is why it is used as the ordering
// score and never shown as a quality claim — the same caveat Document.Score
// carries.
//
// # Why the document goes into DocumentKey and not into Source
//
// RAGFlow identifies a document by name — a name the upstream sync chooses, and
// one it reports per chunk as document_keyword — and it carries no title of its
// own: a corpus that wants a heading retrieves one inside the passage text.
// Source is the field the model is shown, so it stays empty by default. Putting
// the key there would hand the model an identifier in the place a name goes,
// which is noise it can neither read nor reason about — and the one thing it
// would tempt the model to do with it is write it into the answer.
//
// Whether the document name is such an identifier or a readable title is a
// property of the *upstream sync*, not of this code, so it is a setting rather
// than a decision made here: DocumentNameAsSource turns the name into Source for
// deployments whose documents are named for people. An empty name never becomes
// a Source either way, because the empty string is how a Document says "nothing
// readable here" and the two must not be confusable.
func (r *ragflowRetriever) attribute(chunks []ragflowChunk) []Document {
	out := make([]Document, 0, len(chunks))
	for _, c := range chunks {
		// document_keyword is the document's name; document_id is the platform's
		// internal id and the fallback, for a deployment that reports only one of
		// the two.
		key := c.DocumentKeyword
		if key == "" {
			key = c.DocumentID
		}
		doc := Document{
			ChunkID:     c.ID,
			DocumentKey: key,
			Content:     c.Content,
			Score:       c.Similarity,
			VectorScore: c.VectorSimilarity,
			KB:          r.datasetName(c.DatasetID),
		}
		if r.cfg.DocumentNameAsSource {
			doc.Source = c.DocumentKeyword
		}
		out = append(out, doc)
	}
	return out
}

// datasetName resolves the knowledge base a chunk's dataset belongs to.
//
// A chunk that names no dataset — an older RAGFlow that omits the field — is
// attributed only when the run covers exactly one record, because then there is
// only one candidate. With several records the field is the only way to tell
// them apart, so an unknown id yields no name rather than a guess. The chunk's
// content is kept either way.
func (r *ragflowRetriever) datasetName(datasetID string) string {
	if name, ok := r.byDataset[datasetID]; ok {
		return name
	}
	if len(r.byDataset) == 1 && len(r.names) == 1 {
		return r.names[0]
	}
	return ""
}

// ragflowGroups partitions the enabled RAGFlow records by their effective
// retrieval setting, preserving first-appearance order so Name() and error text
// stay deterministic.
//
// A dataset id repeated within one group is sent once: the platform treats the
// list as a set, so a repeat would only ask it to search the same dataset twice,
// and the first record wins the attribution. A repeat across differently
// configured groups is searched by each group — that is the only way both
// thresholds can apply — and the resulting duplicate passages are collapsed by
// chunk id after the merge.
func ragflowGroups(kbs []*model.KnowledgeBaseM, cfg RAGFlowConfig) []ragflowGroup {
	var groups []ragflowGroup
	index := make(map[ragflowQueryKnobs]int)
	for _, kb := range kbs {
		if kb == nil || !kb.Enabled() || !isRAGFlow(kb) {
			continue
		}
		id := kb.DatasetID
		if strings.TrimSpace(id) == "" {
			continue
		}
		knobs := ragflowQueryKnobs{
			threshold: effectiveRAGFlowThreshold(kb.ScoreThreshold, cfg.SimilarityThreshold),
			topK:      kb.TopK,
		}
		i, ok := index[knobs]
		if !ok {
			i = len(groups)
			index[knobs] = i
			groups = append(groups, ragflowGroup{
				knobs:     knobs,
				byDataset: make(map[string]string),
			})
		}
		g := &groups[i]
		if _, seen := g.byDataset[id]; seen {
			continue
		}
		name := displayName(kb)
		g.byDataset[id] = name
		g.ids = append(g.ids, id)
		g.names = append(g.names, name)
	}
	return groups
}

// effectiveRAGFlowThreshold resolves a record's threshold against the
// deployment's: the record wins when it set one, otherwise the deployment value
// applies, and 0 leaves RAGFlow's own default in place.
func effectiveRAGFlowThreshold(record, deployment float64) float64 {
	if record > 0 {
		return record
	}
	return deployment
}

// isRAGFlow reports whether a record addresses the RAGFlow platform.
func isRAGFlow(kb *model.KnowledgeBaseM) bool {
	return kb.InferPlatform() == model.PlatformRAGFlow
}
