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
// The original repo for this file is https://github.com/onexstack/onex.

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

// DifyConfig configures the Dify knowledge-base platform. The endpoint and key
// are shared by every Dify knowledge base in a deployment; each record
// contributes only its dataset id and retrieval knobs.
type DifyConfig struct {
	// BaseURL is the Dify API root, e.g. "https://api.dify.ai/v1".
	BaseURL string
	// APIKey authenticates against BaseURL.
	APIKey string
	// Timeout bounds one retrieval request.
	Timeout time.Duration
	// HTTPClient overrides the default client (tests, proxies).
	HTTPClient *http.Client
}

// defaultDifyBaseURL is used when a deployment does not override it.
const defaultDifyBaseURL = "https://api.dify.ai/v1"

// difyRetriever queries one Dify dataset.
type difyRetriever struct {
	name      string
	datasetID string
	// topK is the record's own cap on returned passages. 0 means the record
	// names no count, and the tool's request applies.
	topK      int
	threshold float64
	cfg       DifyConfig
	client    *kbclient.Client
}

// NewDifyRetriever builds a retriever for one Dify knowledge base. It returns
// nil for a record without a dataset id, which is a record nothing can query.
func NewDifyRetriever(kb *model.KnowledgeBaseM, cfg DifyConfig) Retriever {
	if kb == nil || strings.TrimSpace(kb.DatasetID) == "" {
		return nil
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultDifyBaseURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	var client *kbclient.Client
	if cfg.HTTPClient != nil {
		client = kbclient.NewWithHTTPClient(cfg.HTTPClient)
	} else {
		client = kbclient.NewForConfig(cfg.Timeout)
	}
	return &difyRetriever{
		name:      displayName(kb),
		datasetID: kb.DatasetID,
		topK:      kb.TopK,
		threshold: kb.ScoreThreshold,
		cfg:       cfg,
		client:    client,
	}
}

// Name identifies the knowledge base.
func (r *difyRetriever) Name() string { return r.name }

// Retrieve runs one Dify retrieval call.
func (r *difyRetriever) Retrieve(ctx context.Context, query string, topK int) ([]Document, error) {
	topK = limitTopK(r.topK, topK)
	if topK <= 0 {
		topK = defaultTopK
	}
	url := fmt.Sprintf("%s/datasets/%s/retrieve", strings.TrimRight(r.cfg.BaseURL, "/"), r.datasetID)
	var parsed difyResponse
	request := difyRequest{
		Query: query,
		RetrievalModel: difyRetrievalModel{
			SearchMethod: "semantic_search",
			TopK:         topK,
			// The threshold is applied only when the record set one. Turning it
			// on with the zero value would drop every passage scoring below
			// zero, i.e. none — but it would also tell Dify to compare against a
			// threshold nobody chose.
			ScoreThreshold:        r.threshold,
			ScoreThresholdEnabled: r.threshold > 0,
		},
	}
	if err := postJSON(ctx, r.client, url,
		map[string]string{"Authorization": "Bearer " + r.cfg.APIKey}, request, &parsed,
		postLabels{
			Prefix:      "kbtool/dify",
			Call:        "retrieve from " + r.name,
			StatusLabel: r.name,
		}); err != nil {
		return nil, err
	}
	return parsed.documents(r.name), nil
}

// Wire types for the Dify retrieval API.

type difyRequest struct {
	Query          string             `json:"query"`
	RetrievalModel difyRetrievalModel `json:"retrieval_model"`
}

type difyRetrievalModel struct {
	SearchMethod          string  `json:"search_method"`
	TopK                  int     `json:"top_k"`
	ScoreThreshold        float64 `json:"score_threshold"`
	RerankingEnable       bool    `json:"reranking_enable"`
	ScoreThresholdEnabled bool    `json:"score_threshold_enabled"`
}

type difyResponse struct {
	Records []struct {
		Score   float64 `json:"score"`
		Segment struct {
			Content  string `json:"content"`
			Document struct {
				Name string `json:"name"`
			} `json:"document"`
		} `json:"segment"`
	} `json:"records"`
}

// documents converts the wire response into the neutral shape, stamping the
// knowledge base's name. One Dify retriever covers exactly one record, so every
// passage it returns comes from that record and kb is always known.
func (r difyResponse) documents(kb string) []Document {
	out := make([]Document, 0, len(r.Records))
	for _, rec := range r.Records {
		out = append(out, Document{
			Source:  rec.Segment.Document.Name,
			Content: rec.Segment.Content,
			Score:   rec.Score,
			KB:      kb,
		})
	}
	return out
}

// displayName is the name to show for a record, falling back to its
// machine name so a record with no display name is still identifiable.
func displayName(kb *model.KnowledgeBaseM) string {
	if kb.DisplayName != "" {
		return kb.DisplayName
	}
	return kb.Name
}

// summarise makes an error body safe to log: single line, bounded.
func summarise(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}
