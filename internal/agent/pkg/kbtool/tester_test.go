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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
)

// ragflowTestServer answers every retrieval request with body, and reports the
// request body it saw to onRequest.
func ragflowTestServer(t *testing.T, body string, onRequest func(string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/retrieval" {
			t.Errorf("path = %q, want /api/v1/retrieval", got)
		}
		if onRequest != nil {
			buf, _ := io.ReadAll(r.Body)
			onRequest(string(buf))
		}
		fmt.Fprint(w, body)
	}))
}

func floatPtr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool        { return &v }

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

func TestRAGFlowTester_AppliesOverrides(t *testing.T) {
	const body = `{"code":0,"data":{"chunks":[
		{"id":"c1","content":"passage one","document_keyword":"doc.md","document_id":"d1","dataset_id":"ds-1","similarity":0.9,"vector_similarity":0.8}
	]}}`
	var got string
	srv := ragflowTestServer(t, body, func(b string) { got = b })
	defer srv.Close()

	tester := NewRAGFlowTester(RAGFlowConfig{
		BaseURL:             srv.URL,
		APIKey:              "secret",
		SimilarityThreshold: 0.1,
		RerankID:            "rr-1",
	})
	result, err := tester.Test(context.Background(), ragflowRecord(), "how?", TestOptions{
		TopK:                   7,
		ScoreThreshold:         floatPtr(0.25),
		VectorSimilarityWeight: floatPtr(0.6),
		Rerank:                 boolPtr(true),
		Keyword:                boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result = %+v, want ok", result)
	}

	for _, want := range []string{
		`"page_size":7`, `"similarity_threshold":0.25`, `"vector_similarity_weight":0.6`,
		`"rerank_id":"rr-1"`, `"keyword":true`, `"ds-1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("request body is missing %s: %s", want, got)
		}
	}

	if len(result.Hits) != 1 {
		t.Fatalf("hits = %+v", result.Hits)
	}
	hit := result.Hits[0]
	if hit.ChunkID != "c1" || hit.DocumentKey != "doc.md" || hit.Score != 0.9 || hit.VectorScore != 0.8 {
		t.Errorf("hit = %+v", hit)
	}
	if result.Effective.TopK != 7 || result.Effective.SimilarityThreshold != 0.25 ||
		result.Effective.VectorSimilarityWeight != 0.6 || !result.Effective.Rerank ||
		result.Effective.RerankModel != "rr-1" || !result.Effective.Keyword {
		t.Errorf("effective = %+v", result.Effective)
	}
}

func TestRAGFlowTester_FallsBackToRecordAndDeployment(t *testing.T) {
	var got string
	srv := ragflowTestServer(t, `{"code":0,"data":{"chunks":[]}}`, func(b string) { got = b })
	defer srv.Close()

	tester := NewRAGFlowTester(RAGFlowConfig{
		BaseURL:                srv.URL,
		SimilarityThreshold:    0.1,
		VectorSimilarityWeight: 0, // 0 is RAGFlow's own default and is omitted.
	})
	result, err := tester.Test(context.Background(), ragflowRecord(), "how?", TestOptions{})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	if result.Effective.TopK != 3 {
		t.Errorf("topK = %d, want the record's 3", result.Effective.TopK)
	}
	for _, want := range []string{`"page_size":3`, `"similarity_threshold":0.1`} {
		if !strings.Contains(got, want) {
			t.Errorf("request body is missing %s: %s", want, got)
		}
	}
	for _, absent := range []string{`"rerank_id"`, `"keyword":true`, `"vector_similarity_weight"`} {
		if strings.Contains(got, absent) {
			t.Errorf("request body should omit %s: %s", absent, got)
		}
	}
}

func TestRAGFlowTester_RerankWithoutModelFails(t *testing.T) {
	called := false
	srv := ragflowTestServer(t, `{"code":0,"data":{"chunks":[]}}`, func(string) { called = true })
	defer srv.Close()

	tester := NewRAGFlowTester(RAGFlowConfig{BaseURL: srv.URL})
	_, err := tester.Test(context.Background(), ragflowRecord(), "how?", TestOptions{
		Rerank: boolPtr(true),
	})
	if !errors.Is(err, ErrRerankNotConfigured) {
		t.Fatalf("err = %v, want ErrRerankNotConfigured", err)
	}
	if called {
		t.Error("the request must not reach the platform when rerank is unavailable")
	}
}

func TestRAGFlowTester_RejectsUnsupportedRecords(t *testing.T) {
	configured := NewRAGFlowTester(RAGFlowConfig{BaseURL: "http://example.invalid"})

	dify := ragflowRecord()
	dify.Platform = model.PlatformDify
	if _, err := configured.Test(context.Background(), dify, "q", TestOptions{}); !errors.Is(err, ErrNotRAGFlow) {
		t.Errorf("dify err = %v, want ErrNotRAGFlow", err)
	}

	noDataset := ragflowRecord()
	noDataset.DatasetID = ""
	if _, err := configured.Test(context.Background(), noDataset, "q", TestOptions{}); !errors.Is(err, ErrMissingDataset) {
		t.Errorf("missing dataset err = %v, want ErrMissingDataset", err)
	}

	if _, err := configured.Test(context.Background(), ragflowRecord(), "  ", TestOptions{}); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query err = %v, want ErrEmptyQuery", err)
	}

	unconfigured := NewRAGFlowTester(RAGFlowConfig{})
	if _, err := unconfigured.Test(context.Background(), ragflowRecord(), "q", TestOptions{}); !errors.Is(err, ErrRetrievalNotConfigured) {
		t.Errorf("unconfigured err = %v, want ErrRetrievalNotConfigured", err)
	}
}

func TestRAGFlowTester_PlatformFailureIsOKFalse(t *testing.T) {
	srv := ragflowTestServer(t, `{"code":102,"message":"dataset not found"}`, nil)
	defer srv.Close()

	tester := NewRAGFlowTester(RAGFlowConfig{BaseURL: srv.URL})
	result, err := tester.Test(context.Background(), ragflowRecord(), "how?", TestOptions{})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK {
		t.Fatalf("result = %+v, want ok=false", result)
	}
	if !strings.Contains(result.Message, "dataset not found") {
		t.Errorf("message = %q, want the platform's reason", result.Message)
	}
	if strings.Contains(result.Message, srv.URL) {
		t.Errorf("message leaks the platform address: %q", result.Message)
	}
}

func TestRAGFlowTester_PreservesLongContent(t *testing.T) {
	long := strings.Repeat("章节内容", 2000)
	body := fmt.Sprintf(`{"code":0,"data":{"chunks":[
		{"id":"c1","content":%q,"document_keyword":"doc.md","similarity":0.5}
	]}}`, long)
	srv := ragflowTestServer(t, body, nil)
	defer srv.Close()

	tester := NewRAGFlowTester(RAGFlowConfig{BaseURL: srv.URL})
	result, err := tester.Test(context.Background(), ragflowRecord(), "how?", TestOptions{})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Content != long {
		t.Errorf("content was truncated: got %d chars", len(result.Hits[0].Content))
	}
}

func TestEffectiveTopK(t *testing.T) {
	cases := []struct {
		name      string
		recordTop int
		requested int
		want      int
	}{
		{name: "request wins", recordTop: 3, requested: 7, want: 7},
		{name: "record fallback", recordTop: 3, want: 3},
		{name: "default", want: defaultTopK},
		{name: "capped", requested: 1000, want: maxTestTopK},
		{name: "record capped", recordTop: 1000, want: maxTestTopK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveTopK(tc.recordTop, tc.requested); got != tc.want {
				t.Errorf("effectiveTopK(%d, %d) = %d, want %d", tc.recordTop, tc.requested, got, tc.want)
			}
		})
	}
}

// TestRAGFlowTester_RecordThresholdWinsOverDeployment pins the precedence the
// runtime and the test now share: the record's own threshold beats the
// deployment's, and an explicit request still beats both.
func TestRAGFlowTester_RecordThresholdWinsOverDeployment(t *testing.T) {
	var got string
	srv := ragflowTestServer(t, `{"code":0,"data":{"chunks":[]}}`, func(b string) { got = b })
	defer srv.Close()

	kb := ragflowRecord()
	kb.ScoreThreshold = 0.3
	tester := NewRAGFlowTester(RAGFlowConfig{BaseURL: srv.URL, SimilarityThreshold: 0.1})
	result, err := tester.Test(context.Background(), kb, "how?", TestOptions{})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.Effective.SimilarityThreshold != 0.3 {
		t.Errorf("effective threshold = %v, want the record's 0.3", result.Effective.SimilarityThreshold)
	}
	if !strings.Contains(got, `"similarity_threshold":0.3`) {
		t.Errorf("request body is missing the record threshold: %s", got)
	}
}
