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
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onexstack/onexai/internal/agent/model"
	kbclient "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/kb"
)

// maxTestTopK bounds one retrieval test. The number is a request-shape guard,
// not a platform limit: a person tuning a dataset reads a page of results, and
// a request for hundreds of passages would only make the console slow.
const maxTestTopK = 50

// maxTestMessageRunes bounds the upstream message a test echoes back. The full
// error is logged by the caller; the response is shown in a table cell.
const maxTestMessageRunes = 300

var (
	// ErrNotRAGFlow is returned for a record that does not address RAGFlow.
	// The console hides the test for those records; this is the server-side
	// counterpart, so a direct API caller cannot bypass that rule.
	ErrNotRAGFlow = errors.New("kbtool: retrieval test requires a RAGFlow knowledge base")
	// ErrRetrievalNotConfigured is returned when the deployment has no RAGFlow
	// endpoint. It is a deployment state, not a bad request.
	ErrRetrievalNotConfigured = errors.New("kbtool: agent.ragflow.base-url is not configured")
	// ErrRerankNotConfigured is returned when reranking was asked for but no
	// rerank model is named. Failing is deliberate: silently returning an
	// unranked result would make the test lie about what the run would see.
	ErrRerankNotConfigured = errors.New("kbtool: rerank was requested but agent.ragflow.rerank-id is empty")
	// ErrMissingDataset is returned for a record whose dataset id is empty.
	ErrMissingDataset = errors.New("kbtool: the knowledge base has no dataset id")
	// ErrEmptyQuery is returned when the caller sent no question.
	ErrEmptyQuery = errors.New("kbtool: query is required")
)

// TestOptions are the per-request overrides the retrieval test may apply.
//
// A nil pointer means "use the record's or the deployment's value". That is
// different from an explicit zero: RAGFlow's request fields are omitempty, so 0
// means "let the platform use its own default", which is a real choice a person
// tuning a dataset may want to make.
type TestOptions struct {
	// TopK is how many passages to return. <=0 means the record's topK, then 5.
	TopK int
	// ScoreThreshold overrides the record's or the deployment's similarity
	// threshold.
	ScoreThreshold *float64
	// VectorSimilarityWeight overrides the deployment's dense weight.
	VectorSimilarityWeight *float64
	// Rerank turns the deployment's rerank model on or off for this call.
	Rerank *bool
	// Keyword turns RAGFlow's keyword expansion on or off for this call.
	Keyword *bool
}

// EffectiveRAGFlow is what one test actually ran with. The console shows it,
// so an operator can tell a changed result from a changed parameter.
type EffectiveRAGFlow struct {
	TopK                   int
	SimilarityThreshold    float64
	VectorSimilarityWeight float64
	Rerank                 bool
	RerankModel            string
	Keyword                bool
}

// TestResult is one retrieval test.
//
// OK=false is a valid answer: the request was well-formed, the platform was
// not. The caller renders Message instead of treating the call as broken.
type TestResult struct {
	OK        bool
	Message   string
	Hits      []Document
	TookMs    int64
	Effective EffectiveRAGFlow
}

// RAGFlowTester runs single-record retrievals with per-request overrides.
//
// It holds the deployment's platform configuration and an HTTP client, both
// read-only, so one tester serves every request. The retrievers themselves are
// built per call because the overrides are per call.
type RAGFlowTester struct {
	cfg    RAGFlowConfig
	client *kbclient.Client
}

// NewRAGFlowTester builds a tester from the deployment's RAGFlow settings.
func NewRAGFlowTester(cfg RAGFlowConfig) *RAGFlowTester {
	return &RAGFlowTester{cfg: cfg, client: newRAGFlowClient(cfg)}
}

// Configured reports whether the deployment has a RAGFlow endpoint.
func (t *RAGFlowTester) Configured() bool {
	return t != nil && strings.TrimSpace(t.cfg.BaseURL) != ""
}

// HasRerank reports whether the deployment names a rerank model.
func (t *RAGFlowTester) HasRerank() bool {
	return t != nil && strings.TrimSpace(t.cfg.RerankID) != ""
}

// Test runs one retrieval against a single record.
//
// It calls Retriever.Retrieve directly rather than the tool's InvokableRun:
// the tool truncates, watermarks and renders passages for a model, while a
// person debugging a dataset needs the chunks the platform actually returned.
//
// A disabled record is tested like an enabled one. The test exists to diagnose
// configuration, and refusing to run it on the very record whose configuration
// is in question would remove the only way to find out what is wrong.
func (t *RAGFlowTester) Test(
	ctx context.Context, kb *model.KnowledgeBaseM, query string, opts TestOptions,
) (TestResult, error) {
	if err := t.checkRequest(kb, query); err != nil {
		return TestResult{}, err
	}

	eff := t.effective(kb, opts)
	if eff.Rerank && !t.HasRerank() {
		return TestResult{}, ErrRerankNotConfigured
	}

	started := time.Now()
	hits, err := t.retrieverFor(kb, eff).Retrieve(ctx, query, eff.TopK)
	took := time.Since(started)

	result := TestResult{TookMs: took.Milliseconds(), Effective: eff} //nolint:exhaustruct_v5 // OK, Message and Hits are filled below
	if err != nil {
		return t.failedResult(ctx, kb, err, result)
	}

	result.OK = true
	result.Hits = hits
	return result, nil
}

// checkRequest rejects the inputs no request should reach the platform with.
func (t *RAGFlowTester) checkRequest(kb *model.KnowledgeBaseM, query string) error {
	switch {
	case !t.Configured():
		return ErrRetrievalNotConfigured
	case kb == nil || kb.InferPlatform() != model.PlatformRAGFlow:
		return ErrNotRAGFlow
	case strings.TrimSpace(kb.DatasetID) == "":
		return ErrMissingDataset
	case strings.TrimSpace(query) == "":
		return ErrEmptyQuery
	default:
		return nil
	}
}

// retrieverFor builds the single-record retriever this test runs with.
//
// The deployment config is copied before overriding: one tester serves many
// requests, and mutating its config would leak one test's knobs into the next.
func (t *RAGFlowTester) retrieverFor(
	kb *model.KnowledgeBaseM, eff EffectiveRAGFlow,
) *ragflowRetriever {
	cfg := t.cfg
	cfg.SimilarityThreshold = eff.SimilarityThreshold
	cfg.VectorSimilarityWeight = eff.VectorSimilarityWeight
	cfg.Keyword = eff.Keyword
	if !eff.Rerank {
		cfg.RerankID = ""
	}

	name := displayName(kb)
	return &ragflowRetriever{
		datasetIDs: []string{kb.DatasetID},
		names:      []string{name},
		byDataset:  map[string]string{kb.DatasetID: name},
		cfg:        cfg,
		client:     t.client,
	}
}

// failedResult turns a platform error into ok=false, unless the caller's
// context is what ended the request.
//
// A canceled request is the caller's, not the platform's: it has to surface as
// an error rather than as ok=false, or the console would render a timeout as
// "RAGFlow rejected the request".
func (t *RAGFlowTester) failedResult(
	ctx context.Context, kb *model.KnowledgeBaseM, cause error, result TestResult,
) (TestResult, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return TestResult{}, ctxErr
	}
	slog.WarnContext(ctx, "knowledge-base retrieval test failed",
		"knowledge_base", kb.Name, "error", cause)
	result.Message = sanitizeTestMessage(cause.Error(), t.cfg.BaseURL)
	return result, nil
}

// effective resolves the knobs this call runs with.
func (t *RAGFlowTester) effective(kb *model.KnowledgeBaseM, opts TestOptions) EffectiveRAGFlow {
	eff := EffectiveRAGFlow{ //nolint:exhaustruct_v5 // RerankModel is set below when reranking runs
		TopK:                   effectiveTopK(kb.TopK, opts.TopK),
		SimilarityThreshold:    effectiveRAGFlowThreshold(kb.ScoreThreshold, t.cfg.SimilarityThreshold),
		VectorSimilarityWeight: t.cfg.VectorSimilarityWeight,
		Rerank:                 t.HasRerank(),
		Keyword:                t.cfg.Keyword,
	}
	if opts.ScoreThreshold != nil {
		eff.SimilarityThreshold = *opts.ScoreThreshold
	}
	if opts.VectorSimilarityWeight != nil {
		eff.VectorSimilarityWeight = *opts.VectorSimilarityWeight
	}
	if opts.Rerank != nil {
		eff.Rerank = *opts.Rerank
	}
	if opts.Keyword != nil {
		eff.Keyword = *opts.Keyword
	}
	if eff.Rerank {
		eff.RerankModel = t.cfg.RerankID
	}
	return eff
}

// effectiveTopK resolves the requested count against the record and the
// default.
//
// Here an explicit request wins over the record: a retrieval test exists to ask
// "what if", so the operator's count is diagnostic, not a budget. The runtime
// path is stricter — a record may only narrow the deployment's global top-k;
// see limitTopK.
func effectiveTopK(recordTopK, requested int) int {
	topK := requested
	if topK <= 0 {
		topK = recordTopK
	}
	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTestTopK {
		topK = maxTestTopK
	}
	return topK
}

// sanitizeTestMessage makes an upstream error safe to show.
//
// The typed HTTP client's transport errors embed the request URL, and the
// knowledge-base surface is readable by every signed-in user. The address is
// replaced rather than the whole message dropped: "RAGFlow: dial tcp: ..."
// still tells an operator what went wrong, without telling every reader where
// the deployment's platform lives.
func sanitizeTestMessage(message, baseURL string) string {
	message = strings.Join(strings.Fields(message), " ")
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		message = strings.ReplaceAll(message, baseURL, "RAGFlow")
	}
	if utf8.RuneCountInString(message) > maxTestMessageRunes {
		message = string([]rune(message)[:maxTestMessageRunes]) + "…"
	}
	if message == "" {
		return "RAGFlow retrieval failed"
	}
	return message
}
