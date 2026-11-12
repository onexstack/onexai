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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The AIStore interface is wider than this test needs. Embedding it (nil) and
// overriding only KnowledgeBases() keeps the fake honest: an accidental use of
// another accessor panics instead of silently testing something else.
type stubAIStore struct {
	agentstore.AIStore
	knowledgeBases agentstore.KnowledgeBaseStore
}

func (s stubAIStore) KnowledgeBases() agentstore.KnowledgeBaseStore { return s.knowledgeBases }

type stubKnowledgeBaseStore struct {
	agentstore.KnowledgeBaseStore
	row *model.KnowledgeBaseM
	err error
}

func (s stubKnowledgeBaseStore) Get(context.Context, *where.Options) (*model.KnowledgeBaseM, error) {
	return s.row, s.err
}

func ragflowServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
}

func ragflowRecord() *model.KnowledgeBaseM {
	return &model.KnowledgeBaseM{
		Name:        "kb",
		DisplayName: "KB",
		Platform:    model.PlatformRAGFlow,
		DatasetID:   "ds-1",
		TopK:        3,
		Status:      model.StatusEnabled,
	}
}

func newTestBiz(t *testing.T, baseURL string, rows ...*model.KnowledgeBaseM) *knowledgeBaseBiz {
	t.Helper()
	row := ragflowRecord()
	if len(rows) > 0 {
		row = rows[0]
	}
	return New(
		stubAIStore{knowledgeBases: stubKnowledgeBaseStore{row: row}},
		&Config{RAGFlow: kbtool.RAGFlowConfig{BaseURL: baseURL, APIKey: "k"}},
	)
}

func TestTestRetrievalMapsHits(t *testing.T) {
	srv := ragflowServer(t, `{"code":0,"data":{"chunks":[
		{"id":"c1","content":"passage","document_keyword":"doc.md","similarity":0.9,"vector_similarity":0.8}
	]}}`)
	defer srv.Close()

	resp, err := newTestBiz(t, srv.URL).TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "kb", Query: "how?", TopK: 2})
	require.NoError(t, err)
	require.True(t, resp.GetOk())
	require.Len(t, resp.GetHits(), 1)
	hit := resp.GetHits()[0]
	assert.Equal(t, "c1", hit.GetChunkId())
	assert.Equal(t, "doc.md", hit.GetDocumentName())
	assert.Equal(t, "passage", hit.GetContent())
	assert.InDelta(t, 0.9, hit.GetScore(), 1e-9)
	assert.InDelta(t, 0.8, hit.GetVectorScore(), 1e-9)
	assert.Equal(t, int32(2), resp.GetTopK())
	assert.Equal(t, model.PlatformRAGFlow, resp.GetPlatform())
}

func TestTestRetrievalNotFound(t *testing.T) {
	biz := New(stubAIStore{knowledgeBases: stubKnowledgeBaseStore{}}, &Config{})
	_, err := biz.TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "ghost", Query: "q"})
	assert.ErrorIs(t, err, errno.ErrNotFound)
}

func TestTestRetrievalRejectsDify(t *testing.T) {
	dify := ragflowRecord()
	dify.Platform = model.PlatformDify
	_, err := newTestBiz(t, "http://example.invalid", dify).TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "kb", Query: "q"})
	assert.ErrorIs(t, err, errno.ErrKnowledgeBaseRetrievalUnsupported)
}

func TestTestRetrievalUnconfiguredDeployment(t *testing.T) {
	resp, err := newTestBiz(t, "").TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "kb", Query: "q"})
	require.NoError(t, err)
	assert.False(t, resp.GetOk())
	assert.Contains(t, resp.GetMessage(), "agent.ragflow.base-url")
}

func TestTestRetrievalRerankWithoutModel(t *testing.T) {
	resp, err := newTestBiz(t, "http://example.invalid").TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "kb", Query: "q", Rerank: boolPtr(true)})
	require.NoError(t, err)
	assert.False(t, resp.GetOk())
	assert.Contains(t, resp.GetMessage(), "agent.ragflow.rerank-id")
}

func TestTestRetrievalPlatformFailureIsNotAnError(t *testing.T) {
	srv := ragflowServer(t, `{"code":102,"message":"dataset not found"}`)
	defer srv.Close()

	resp, err := newTestBiz(t, srv.URL).TestRetrieval(context.Background(),
		&v1.TestKnowledgeBaseRetrievalRequest{Name: "kb", Query: "q"})
	require.NoError(t, err)
	assert.False(t, resp.GetOk())
	assert.Contains(t, resp.GetMessage(), "dataset not found")
}

func boolPtr(v bool) *bool { return &v }
