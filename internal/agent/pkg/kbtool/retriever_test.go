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
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// fakeRetriever is a canned Retriever.
type fakeRetriever struct {
	name    string
	docs    []Document
	err     error
	delay   time.Duration
	mu      sync.Mutex
	queries []string
}

func (f *fakeRetriever) Name() string { return f.name }

func (f *fakeRetriever) Retrieve(ctx context.Context, query string, topK int) ([]Document, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.docs, nil
}

func TestNew_NoRetrieversIsNil(t *testing.T) {
	if New(Config{}) != nil {
		t.Error("a tool with nothing to query must be nil, not a tool that always fails")
	}
}

func TestRetrievalTool_MergesAndOrdersByScore(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "low", docs: []Document{{Source: "a", Content: "A", Score: 0.2}}},
			&fakeRetriever{name: "high", docs: []Document{{Source: "b", Content: "B", Score: 0.9}}},
		},
	}).(*retrievalTool)

	res := tool.retrieveAll(context.Background(), "q")
	if res.queried != 2 || len(res.failed) != 0 {
		t.Fatalf("queried=%d failed=%v, want 2 queried and none failed", res.queried, res.failed)
	}
	if len(res.docs) != 2 {
		t.Fatalf("got %d documents, want 2", len(res.docs))
	}
	if res.docs[0].Score != 0.9 || res.docs[1].Score != 0.2 {
		t.Errorf("documents are not ordered best-first: %+v", res.docs)
	}
}

func TestRetrievalTool_FailingBaseDoesNotFailTheCall(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "broken", err: errors.New("boom")},
			&fakeRetriever{name: "ok", docs: []Document{{Content: "found", Score: 1}}},
		},
	}).(*retrievalTool)

	res := tool.retrieveAll(context.Background(), "q")
	if res.queried != 2 || len(res.failed) != 1 || res.failed[0] != "broken" {
		t.Fatalf("queried=%d failed=%v, want one failed base named broken", res.queried, res.failed)
	}
	if len(res.docs) != 1 || res.docs[0].Content != "found" {
		t.Fatalf("a failing base must not lose the healthy one's results: %+v", res.docs)
	}
}

// TestRetrievalTool_AllSourcesFailIsAnError pins that an outage is told apart
// from an empty knowledge base: with every source down, "no results" and "no
// source answered" would otherwise be the same empty string.
func TestRetrievalTool_AllSourcesFailIsAnError(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "alpha", err: errors.New("boom-alpha")},
			&fakeRetriever{name: "beta", err: errors.New("boom-beta")},
		},
	}).(*retrievalTool)

	out, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err == nil {
		t.Fatalf("every source failing must be an error, got %q", out)
	}
	// The message is model-visible, so it names the sources but leaks neither
	// the endpoint nor the underlying cause.
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name source %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q must not carry the underlying cause", err)
	}
}

// TestRetrievalTool_PartialFailureIsNoted pins that a partial answer says so
// instead of reading as a complete one.
func TestRetrievalTool_PartialFailureIsNoted(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "broken", err: errors.New("boom")},
			&fakeRetriever{name: "ok", docs: []Document{{Content: "found", Score: 1}}},
		},
	}).(*retrievalTool)

	out, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatalf("a partial failure must not fail the call: %v", err)
	}
	if !strings.Contains(out, "could not be reached") || !strings.Contains(out, "broken") {
		t.Errorf("the result must say a source was missing: %q", out)
	}
	if strings.Contains(out, "boom") {
		t.Errorf("the note must not carry the underlying cause: %q", out)
	}
	if !strings.Contains(out, "found") {
		t.Errorf("the healthy source's result must still be returned: %q", out)
	}
}

// TestRetrievalTool_PartialFailureWithNoDocsIsNoted pins that a partial outage
// with no hits is not reported as "no relevant information".
func TestRetrievalTool_PartialFailureWithNoDocsIsNoted(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "broken", err: errors.New("boom")},
			&fakeRetriever{name: "empty"},
		},
	}).(*retrievalTool)

	out, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if strings.Contains(out, "No relevant information found") {
		t.Errorf("a partial outage is not \"nothing found\": %q", out)
	}
	if !strings.Contains(out, "could not be reached") || !strings.Contains(out, "broken") {
		t.Errorf("the result must explain the thin answer: %q", out)
	}
}

// TestRetrievalTool_QueriesConcurrently pins that the bases are queried at the
// same time: sequentially, two 60ms bases would take ~120ms.
func TestRetrievalTool_QueriesConcurrently(t *testing.T) {
	const delay = 60 * time.Millisecond
	tool := New(Config{
		Retrievers: []Retriever{
			&fakeRetriever{name: "a", delay: delay},
			&fakeRetriever{name: "b", delay: delay},
			&fakeRetriever{name: "c", delay: delay},
		},
	}).(*retrievalTool)

	start := time.Now()
	tool.retrieveAll(context.Background(), "q")
	if elapsed := time.Since(start); elapsed > 2*delay {
		t.Errorf("queries took %v; three concurrent %v calls should take about %v", elapsed, delay, delay)
	}
}

func TestRender_CapsAtMaxChars(t *testing.T) {
	docs := []Document{
		{Content: strings.Repeat("x", 100), Score: 1},
		{Content: strings.Repeat("y", 100), Score: 0.5},
	}
	full, fullDocs := render(docs, 0)
	if !strings.Contains(full, "yyy") {
		t.Fatal("with no cap both documents must be rendered")
	}
	if len(fullDocs) != 2 {
		t.Errorf("rendered %d passages with no cap, want both", len(fullDocs))
	}
	capped, cappedDocs := render(docs, 150)
	if strings.Contains(capped, "yyy") {
		t.Error("the cap must stop after the first document")
	}
	if !strings.Contains(capped, "omitted") {
		t.Error("a capped result must say so; a silent cut reads as a complete answer")
	}
	// The report is built from what render wrote: a document the cap cut is one
	// the model never read, and naming it as a source would be a claim about a
	// reading that did not happen.
	if len(cappedDocs) != 1 || cappedDocs[0].Content != docs[0].Content {
		t.Errorf("rendered subset = %d passages, want exactly the one that fit", len(cappedDocs))
	}
}

// fakeSink records what a run reported about the documents it read.
type fakeSink struct {
	mu   sync.Mutex
	refs []agentapi.SourceRef
}

func (s *fakeSink) Sources(_ context.Context, refs []agentapi.SourceRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs = append(s.refs, refs...)
}

// TestInvokableRun_ReportsOneSourcePerDocument pins the collapse: a document
// that contributed several passages is one source, and the order is the merge's
// — the document whose best passage ranked highest leads.
func TestInvokableRun_ReportsOneSourcePerDocument(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{&fakeRetriever{name: "keyed", docs: []Document{
			{DocumentKey: "doc-b", Content: "B1", Score: 0.9},
			{DocumentKey: "doc-b", Content: "B2", Score: 0.8},
			{DocumentKey: "doc-a", Source: "A name", Content: "A", Score: 0.7},
		}}},
	}).(*retrievalTool)

	sink := &fakeSink{}
	ctx := agentapi.WithSourceSink(context.Background(), sink)
	if _, err := tool.InvokableRun(ctx, `{"query":"q"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	want := []agentapi.SourceRef{
		{DocumentKey: "doc-b"},
		{DocumentKey: "doc-a", Title: "A name"},
	}
	if len(sink.refs) != len(want) {
		t.Fatalf("reported %d sources, want %d: %+v", len(sink.refs), len(want), sink.refs)
	}
	for i := range want {
		if sink.refs[i] != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, sink.refs[i], want[i])
		}
	}
}

// TestInvokableRun_ReportsOnlyWhatSurvivedTheCap pins the other half: the tool
// reports the passages it handed the model, not everything the bases returned.
func TestInvokableRun_ReportsOnlyWhatSurvivedTheCap(t *testing.T) {
	tool := New(Config{
		GlobalTopK: 1,
		Retrievers: []Retriever{&fakeRetriever{name: "keyed", docs: []Document{
			{DocumentKey: "top", Content: "kept", Score: 0.9},
			{DocumentKey: "cut", Content: "dropped", Score: 0.1},
		}}},
	}).(*retrievalTool)

	sink := &fakeSink{}
	ctx := agentapi.WithSourceSink(context.Background(), sink)
	if _, err := tool.InvokableRun(ctx, `{"query":"q"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	if len(sink.refs) != 1 || sink.refs[0].DocumentKey != "top" {
		t.Errorf("reported %+v, want only the document whose passage the model read", sink.refs)
	}
}

// TestInvokableRun_WithoutSinkStillAnswers pins that a run nobody wants sources
// from is ordinary rather than an error: the retrieval happened, the model read
// it, and only the report has nowhere to go.
func TestInvokableRun_WithoutSinkStillAnswers(t *testing.T) {
	tool := New(Config{
		Retrievers: []Retriever{&fakeRetriever{name: "keyed", docs: []Document{
			{DocumentKey: "doc-a", Content: "A", Score: 0.9},
		}}},
	}).(*retrievalTool)

	out, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatalf("InvokableRun without a sink: %v", err)
	}
	if !strings.Contains(out, "A") {
		t.Errorf("the passages must still reach the model; got %q", out)
	}
}

// TestSourceRefs_DropsPassagesThatNameNothing pins that a reference a caller can
// render nothing from is not reported at all.
func TestSourceRefs_DropsPassagesThatNameNothing(t *testing.T) {
	refs := sourceRefs([]Document{
		{Content: "anonymous", Score: 0.9},
		{Source: "a title", Content: "named", Score: 0.5},
	})
	if len(refs) != 1 || refs[0].Title != "a title" {
		t.Errorf("refs = %+v, want only the passage that names itself", refs)
	}
}

func TestGroupKBsByPlatform(t *testing.T) {
	// The shapes that matter: a record that states its platform, one that
	// states nothing (the pre-Platform way of saying "dify"), and nil.
	statedRAGFlow := &model.KnowledgeBaseM{Name: "ragflow-kb", Platform: model.PlatformRAGFlow, DatasetID: "ds-1"}
	statedDify := &model.KnowledgeBaseM{Name: "explicit-dify", Platform: model.PlatformDify, DatasetID: "ds-3"}
	legacyDify := &model.KnowledgeBaseM{Name: "dify-kb", DatasetID: "ds-2"}

	groups := GroupKBsByPlatform([]*model.KnowledgeBaseM{
		statedRAGFlow, nil, legacyDify, statedDify,
	})

	if got := namesOf(groups[model.PlatformRAGFlow]); len(got) != 1 || got[0] != "ragflow-kb" {
		t.Errorf("ragflow group = %v", got)
	}
	if got := namesOf(groups[model.PlatformDify]); len(got) != 2 || got[0] != "dify-kb" || got[1] != "explicit-dify" {
		t.Errorf("dify group = %v", got)
	}
}

// namesOf is the group test's readable view of a partition.
func namesOf(kbs []*model.KnowledgeBaseM) []string {
	out := make([]string, 0, len(kbs))
	for _, kb := range kbs {
		out = append(out, kb.Name)
	}
	return out
}

// ── Dify ────────────────────────────────────────────────────────────────────

func TestDifyRetriever_Query(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		fmt.Fprint(w, `{"records":[{"score":0.7,"segment":{"content":"passage","document":{"name":"doc.md"}}}]}`)
	}))
	defer srv.Close()

	kb := &model.KnowledgeBaseM{
		Name: "kb", DisplayName: "KB", DatasetID: "ds-1", Status: model.StatusEnabled,
		TopK: 3, ScoreThreshold: 0.5,
	}
	r := NewDifyRetriever(kb, DifyConfig{BaseURL: srv.URL, APIKey: "secret"})
	if r == nil {
		t.Fatal("expected a retriever")
	}
	docs, err := r.Retrieve(context.Background(), "question", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(docs) != 1 || docs[0].Content != "passage" || docs[0].Source != "doc.md" {
		t.Fatalf("unexpected documents: %+v", docs)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/datasets/ds-1/retrieve" {
		t.Errorf("path = %q", gotPath)
	}
	// A configured threshold must actually be applied: the upstream
	// implementation hardcoded this to false, which silently ignored the field.
	if !strings.Contains(gotBody, `"score_threshold_enabled":true`) {
		t.Errorf("the record's score threshold was not applied: %s", gotBody)
	}
}

func TestDifyRetriever_NoDatasetIsNil(t *testing.T) {
	if r := NewDifyRetriever(&model.KnowledgeBaseM{Name: "kb"}, DifyConfig{APIKey: "k"}); r != nil {
		t.Error("a record with no dataset id cannot be queried")
	}
}

func TestDifyRetriever_HTTPErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"bad key"}`)
	}))
	defer srv.Close()

	r := NewDifyRetriever(&model.KnowledgeBaseM{DatasetID: "ds", Status: model.StatusEnabled},
		DifyConfig{BaseURL: srv.URL, APIKey: "wrong"})
	if _, err := r.Retrieve(context.Background(), "q", 1); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("the error must name the status and the cause, got: %v", err)
	}
}

// ── RAGFlow ─────────────────────────────────────────────────────────────────

func TestRAGFlowRetriever_Query(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[
			{"content":"passage one","document_keyword":"doc.md","document_id":"d1","similarity":0.9,"dataset_id":"ds-1"},
			{"content":"passage two","document_keyword":"","document_id":"d2","similarity":0.4,"dataset_id":"ds-2"}
		],"total":2}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", DisplayName: "A", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
		{Name: "b", DisplayName: "B", Platform: model.PlatformRAGFlow, DatasetID: "ds-2", Status: model.StatusEnabled},
	}
	rs := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "secret"})
	if len(rs) != 1 {
		t.Fatalf("records with the same setting must share one retriever, got %d", len(rs))
	}

	docs, err := rs[0].Retrieve(context.Background(), "how?", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	if gotPath != "/api/v1/retrieval" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("authorization = %q", gotAuth)
	}
	// One request for both datasets, not one per dataset: the API takes a list
	// and the tool would otherwise pay a round trip per knowledge base.
	for _, want := range []string{`"dataset_ids"`, `"ds-1"`, `"ds-2"`, `"question":"how?"`, `"page_size":5`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body is missing %s: %s", want, gotBody)
		}
	}

	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Content != "passage one" || docs[0].DocumentKey != "doc.md" || docs[0].Score != 0.9 {
		t.Errorf("first document = %+v", docs[0])
	}
	// The document name is a key, not a name: RAGFlow reports no title, and the
	// id it names documents by belongs in the field a caller resolves rather
	// than in the one the model is shown.
	if docs[0].Source != "" {
		t.Errorf("Source must stay empty when the platform reports no title, got %q", docs[0].Source)
	}
	// Each chunk is attributed to the knowledge base that owns its dataset.
	if docs[0].KB != "A" || docs[1].KB != "B" {
		t.Errorf("chunks are not attributed to their knowledge base: %+v", docs)
	}
	// A chunk with no document name falls back to its id rather than reporting
	// nothing at all.
	if docs[1].DocumentKey != "d2" {
		t.Errorf("a chunk with no document_keyword should fall back to its id, got %q", docs[1].DocumentKey)
	}
}

// TestRAGFlowRetriever_NonZeroCodeIsAnError pins the platform's one real trap:
// RAGFlow reports a business failure in the body with HTTP 200. Treating that
// as an empty result set would make the model answer "the knowledge base has
// nothing to say" for an expired key or a missing dataset.
func TestRAGFlowRetriever_NonZeroCodeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":102,"message":"dataset not found"}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
	}
	rs := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "k"})
	_, err := rs[0].Retrieve(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("a non-zero business code must be an error, not an empty result")
	}
	if !strings.Contains(err.Error(), "102") || !strings.Contains(err.Error(), "dataset not found") {
		t.Errorf("the error should carry the platform's code and message, got: %v", err)
	}
}

func TestNewRAGFlowRetrievers_NoDatasetsIsEmpty(t *testing.T) {
	kbs := []*model.KnowledgeBaseM{
		{Name: "no-pointer", Platform: model.PlatformRAGFlow, Status: model.StatusEnabled},
		{Name: "disabled", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusDisabled},
		{Name: "other-platform", Platform: model.PlatformDify, DatasetID: "ds-2", Status: model.StatusEnabled},
	}
	if rs := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: "http://x", APIKey: "k"}); len(rs) != 0 {
		t.Errorf("no record here addresses RAGFlow with a usable dataset id, got %v", rs)
	}
}

// TestRAGFlowRetriever_AttributesChunksToKB pins that a chunk is attributed to
// the knowledge base that owns its dataset, which is what lets a run with
// several bases keep them apart in the rendered result.
func TestRAGFlowRetriever_AttributesChunksToKB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[
			{"content":"one","document_keyword":"doc.md","similarity":0.9,"dataset_id":"ds-1"},
			{"content":"two","document_keyword":"other.md","similarity":0.8,"dataset_id":"ds-2"}
		]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", DisplayName: "A", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
		{Name: "b", DisplayName: "B", Platform: model.PlatformRAGFlow, DatasetID: "ds-2", Status: model.StatusEnabled},
	}
	docs, err := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "k"})[0].
		Retrieve(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(docs) != 2 || docs[0].KB != "A" || docs[1].KB != "B" {
		t.Errorf("chunks are not attributed to their knowledge base: %+v", docs)
	}
}

// TestRAGFlowRetriever_UnknownDatasetKeepsContent pins that a chunk naming a
// dataset nobody declared is neither dropped nor guessed at: with several
// records the field is the only way to tell them apart.
func TestRAGFlowRetriever_UnknownDatasetKeepsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[
			{"content":"one","document_keyword":"doc.md","similarity":0.9,"dataset_id":"zz"}
		]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", DisplayName: "A", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
		{Name: "b", DisplayName: "B", Platform: model.PlatformRAGFlow, DatasetID: "ds-2", Status: model.StatusEnabled},
	}
	docs, err := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "k"})[0].
		Retrieve(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(docs) != 1 || docs[0].Content != "one" {
		t.Fatalf("an unattributable chunk must still be returned: %+v", docs)
	}
	if docs[0].KB != "" {
		t.Errorf("an unknown dataset id must not be attributed to a guess: %q", docs[0].KB)
	}
}

// TestRAGFlowRetriever_SingleSourceAttributesEmptyDatasetID pins the one case
// where a chunk with no dataset id can still be attributed: a run over a single
// record has only one candidate, which keeps an older RAGFlow that omits the
// field working.
func TestRAGFlowRetriever_SingleSourceAttributesEmptyDatasetID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[
			{"content":"one","document_keyword":"doc.md","similarity":0.9}
		]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "only", DisplayName: "Only", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
	}
	docs, err := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "k"})[0].
		Retrieve(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(docs) != 1 || docs[0].KB != "Only" {
		t.Errorf("a single source must claim a chunk that names no dataset: %+v", docs)
	}
}

// TestRAGFlowRetriever_DuplicateDatasetIDDeduped pins that a dataset id shared
// by two records is sent once: the platform treats the list as a set, and a
// repeat would only ask it to search the same dataset twice.
func TestRAGFlowRetriever_DuplicateDatasetIDDeduped(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", DisplayName: "A", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
		{Name: "b", DisplayName: "B", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
	}
	if _, err := NewRAGFlowRetrievers(kbs, RAGFlowConfig{BaseURL: srv.URL, APIKey: "k"})[0].
		Retrieve(context.Background(), "q", 5); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if n := strings.Count(gotBody, `"ds-1"`); n != 1 {
		t.Errorf("a duplicated dataset id must be sent once, got %d in %s", n, gotBody)
	}
}

// TestRAGFlowRetrievers_PerRecordKnobs pins the reason the retriever groups by
// setting: a record that chose a threshold and a Top K must have those values
// on the request that serves it, not the deployment's.
func TestRAGFlowRetrievers_PerRecordKnobs(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		mu.Lock()
		bodies = append(bodies, buf.String())
		mu.Unlock()
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{
			Name: "a", DisplayName: "A", Platform: model.PlatformRAGFlow,
			DatasetID: "ds-1", Status: model.StatusEnabled, ScoreThreshold: 0.35, TopK: 3,
		},
		{
			Name: "b", DisplayName: "B", Platform: model.PlatformRAGFlow,
			DatasetID: "ds-2", Status: model.StatusEnabled, ScoreThreshold: 0.1, TopK: 9,
		},
	}
	rs := NewRAGFlowRetrievers(kbs,
		RAGFlowConfig{BaseURL: srv.URL, APIKey: "k", SimilarityThreshold: 0.2})
	if len(rs) != 2 {
		t.Fatalf("differently configured records need one retriever each, got %d", len(rs))
	}
	for _, r := range rs {
		if _, err := r.Retrieve(context.Background(), "q", 5); err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected two requests, got %d: %v", len(bodies), bodies)
	}
	byDataset := make(map[string]string, len(bodies))
	for _, body := range bodies {
		switch {
		case strings.Contains(body, `"ds-1"`):
			byDataset["ds-1"] = body
		case strings.Contains(body, `"ds-2"`):
			byDataset["ds-2"] = body
		default:
			t.Fatalf("a request named neither dataset: %s", body)
		}
	}
	for dataset, wants := range map[string][]string{
		"ds-1": {`"similarity_threshold":0.35`, `"page_size":3`},
		"ds-2": {`"similarity_threshold":0.1`, `"page_size":5`},
	} {
		for _, want := range wants {
			if !strings.Contains(byDataset[dataset], want) {
				t.Errorf("%s request is missing %s: %s", dataset, want, byDataset[dataset])
			}
		}
	}
}

// TestRAGFlowRetrievers_SameKnobsShareOneRequest guards against request
// amplification: records that all left the knobs unset must still be served by
// one call, exactly as before.
func TestRAGFlowRetrievers_SameKnobsShareOneRequest(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		fmt.Fprint(w, `{"code":0,"data":{"chunks":[]}}`)
	}))
	defer srv.Close()

	kbs := []*model.KnowledgeBaseM{
		{Name: "a", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
		{Name: "b", Platform: model.PlatformRAGFlow, DatasetID: "ds-2", Status: model.StatusEnabled},
	}
	rs := NewRAGFlowRetrievers(kbs,
		RAGFlowConfig{BaseURL: srv.URL, APIKey: "k", SimilarityThreshold: 0.1})
	if len(rs) != 1 {
		t.Fatalf("records sharing a setting must share a retriever, got %d", len(rs))
	}
	if _, err := rs[0].Retrieve(context.Background(), "q", 5); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	for _, want := range []string{`"ds-1"`, `"ds-2"`, `"similarity_threshold":0.1`, `"page_size":5`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body is missing %s: %s", want, gotBody)
		}
	}
}

// TestRAGFlowRetrievers_ZeroKnobsFallBack pins the backward-compatible case:
// a record that names no threshold uses the deployment's, and a deployment that
// names none leaves the field out so RAGFlow's own default applies.
func TestRAGFlowRetrievers_ZeroKnobsFallBack(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deployment  float64
		wantPresent bool
	}{
		{name: "deployment threshold applies", deployment: 0.1, wantPresent: true},
		{name: "no deployment threshold", deployment: 0, wantPresent: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := new(strings.Builder)
				_, _ = io.Copy(buf, r.Body)
				gotBody = buf.String()
				fmt.Fprint(w, `{"code":0,"data":{"chunks":[]}}`)
			}))
			defer srv.Close()

			kbs := []*model.KnowledgeBaseM{
				{Name: "a", Platform: model.PlatformRAGFlow, DatasetID: "ds-1", Status: model.StatusEnabled},
			}
			rs := NewRAGFlowRetrievers(kbs,
				RAGFlowConfig{BaseURL: srv.URL, APIKey: "k", SimilarityThreshold: tc.deployment})
			if _, err := rs[0].Retrieve(context.Background(), "q", 5); err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			has := strings.Contains(gotBody, `"similarity_threshold"`)
			if has != tc.wantPresent {
				t.Errorf("similarity_threshold present = %v, want %v: %s", has, tc.wantPresent, gotBody)
			}
			if tc.wantPresent && !strings.Contains(gotBody, `"similarity_threshold":0.1`) {
				t.Errorf("deployment threshold was not sent: %s", gotBody)
			}
		})
	}
}

func TestLimitTopK(t *testing.T) {
	for _, tc := range []struct {
		record, requested, want int
	}{
		{record: 3, requested: 5, want: 3},
		{record: 5, requested: 3, want: 3},
		{record: 0, requested: 5, want: 5},
		{record: 3, requested: 0, want: 3},
		{record: 0, requested: 0, want: 0},
	} {
		if got := limitTopK(tc.record, tc.requested); got != tc.want {
			t.Errorf("limitTopK(%d, %d) = %d, want %d", tc.record, tc.requested, got, tc.want)
		}
	}
}

func TestDedupeChunks(t *testing.T) {
	docs := []Document{
		{Content: "newer", ChunkID: "c1", Score: 0.9},
		{Content: "older", ChunkID: "c1", Score: 0.4},
		{Content: "no id one"},
		{Content: "no id two"},
	}
	got := dedupeChunks(docs)
	if len(got) != 3 {
		t.Fatalf("dedupeChunks kept %d, want 3: %+v", len(got), got)
	}
	if got[0].Content != "newer" {
		t.Errorf("the best-scored copy must survive, got %+v", got[0])
	}
}

// TestDifyRetriever_RecordTopKCapsRequest pins that the record's Top K is no
// longer a dead field: it may lower the count the tool asks for, never raise it.
func TestDifyRetriever_RecordTopKCapsRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		record    int
		requested int
		want      int
	}{
		{name: "record lowers", record: 3, requested: 10, want: 3},
		{name: "record unset", record: 0, requested: 7, want: 7},
		{name: "request lowers", record: 10, requested: 3, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := new(strings.Builder)
				_, _ = io.Copy(buf, r.Body)
				gotBody = buf.String()
				fmt.Fprint(w, `{"records":[]}`)
			}))
			defer srv.Close()

			kb := &model.KnowledgeBaseM{
				Name: "kb", DatasetID: "ds", Status: model.StatusEnabled, TopK: tc.record,
			}
			r := NewDifyRetriever(kb, DifyConfig{BaseURL: srv.URL, APIKey: "k"})
			if _, err := r.Retrieve(context.Background(), "q", tc.requested); err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			if want := fmt.Sprintf(`"top_k":%d`, tc.want); !strings.Contains(gotBody, want) {
				t.Errorf("body is missing %s: %s", want, gotBody)
			}
		})
	}
}
