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
	"hash/fnv"
	"log/slog"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// MultiQuery turns one question into several phrasings and fuses what each one
// retrieves.
//
// It exists because a single query is one guess at the words the corpus uses. A
// question phrased in Chinese against an English corpus, or one that names a
// symptom where the document names a cause, retrieves the wrong passages even
// though the right ones are indexed. Rewriting recovers those cases at the price
// of one extra model call and N times the retrieval requests.
//
// It is deliberately sampled: without a sample rate it is a full rollout, and
// with one it is an experiment whose two arms are told apart in the log. There is
// no other way to learn whether it helps — the fused result has no ground truth
// to check against at runtime.
type MultiQuery struct {
	// Count is how many extra phrasings to ask for. Zero means 2.
	Count int
	// SampleRate is the fraction of searches that take part, in [0,1]. Zero or
	// negative means every search (the feature is already opt-in).
	SampleRate float64
	// Rewrite produces up to Count alternative phrasings. Required.
	Rewrite func(ctx context.Context, query string, n int) ([]string, error)
}

// defaultRewriteCount is how many extra phrasings one search asks for.
const defaultRewriteCount = 2

// rrfK damps the contribution of deep ranks in reciprocal rank fusion. 60 is the
// value from the original RRF paper and the one every implementation uses.
const rrfK = 60.0

// RewriterConfig configures the model-backed rewriter.
type RewriterConfig struct {
	// Model writes the alternative phrasings. Required.
	Model model.BaseChatModel
}

// NewRewriter returns the model-backed rewrite function, or nil when no model is
// configured.
func NewRewriter(cfg RewriterConfig) func(ctx context.Context, query string, n int) ([]string, error) {
	if cfg.Model == nil {
		return nil
	}
	return func(ctx context.Context, query string, n int) ([]string, error) {
		return rewrite(ctx, cfg.Model, query, n)
	}
}

// rewritePrompt is the instruction the rewriter sends.
//
// It asks for lines rather than JSON because the output is a short list and a
// model that wraps it in prose still yields parseable lines; a JSON-only contract
// would turn every formatting slip into a lost experiment sample.
const rewritePrompt = `You rewrite a search query to improve document retrieval.
Produce at most %d alternative phrasings of the question below.
Rules:
- Keep the same language as the question.
- Keep the same intent; do not answer it and do not add facts.
- Vary the wording, synonyms, and specificity.
- One per line, no numbering, no quotes, no explanation.
Question: %s`

// rewrite asks the model for alternative phrasings and normalises the reply.
func rewrite(ctx context.Context, m model.BaseChatModel, query string, n int) ([]string, error) {
	if n <= 0 {
		n = defaultRewriteCount
	}
	message, err := m.Generate(ctx, []*schema.Message{
		schema.SystemMessage("You are a search query rewriter."),
		schema.UserMessage(fmt.Sprintf(rewritePrompt, n, query)),
	})
	if err != nil {
		return nil, err
	}
	if message == nil {
		return nil, fmt.Errorf("rewriter returned no message")
	}
	return parseRewrites(message.Content, query, n), nil
}

// parseRewrites normalises the model's reply into distinct, non-empty queries.
// The original question is dropped if the model repeats it: retrieving the same
// string twice would double its weight in the fusion rather than broaden it.
func parseRewrites(content, original string, n int) []string {
	seen := map[string]bool{strings.TrimSpace(strings.ToLower(original)): true}
	out := make([]string, 0, n)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*• \t")
		line = strings.TrimSpace(line)
		// Strip "1." / "1)" from a numbered list; anything else is left as
		// written, because a leading digit can be part of the query itself.
		if len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
			if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		line = strings.Trim(line, `"'“”`)
		if line == "" {
			continue
		}
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
		if len(out) == n {
			break
		}
	}
	return out
}

// fuseRRF merges ranked lists with reciprocal rank fusion and drops duplicates.
//
// RRF is used rather than the platforms' own scores because those scores are not
// comparable across platforms (see Document.Score); it needs only the rank. A
// passage found by several phrasings accumulates a contribution from each, which
// is what makes the agreement between phrasings visible.
func fuseRRF(lists [][]Document, topK int) []Document {
	type entry struct {
		doc   Document
		score float64
	}
	merged := make(map[string]*entry)
	order := make([]string, 0)
	for _, list := range lists {
		for rank, doc := range list {
			key := docKey(doc)
			e, ok := merged[key]
			if !ok {
				e = &entry{doc: doc}
				merged[key] = e
				order = append(order, key)
			}
			e.score += 1.0 / (rrfK + float64(rank+1))
			// Keep the best platform score for display; it is never used for
			// ordering here.
			if doc.Score > e.doc.Score {
				e.doc.Score = doc.Score
			}
		}
	}

	scored := make([]*entry, 0, len(order))
	for _, key := range order {
		scored = append(scored, merged[key])
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].doc.Score != scored[j].doc.Score {
			return scored[i].doc.Score > scored[j].doc.Score
		}
		return docKey(scored[i].doc) < docKey(scored[j].doc)
	})

	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	out := make([]Document, 0, len(scored))
	for _, e := range scored {
		out = append(out, e.doc)
	}
	return out
}

// docKey identifies a passage across platforms. The content is part of it
// because a document contributes several passages and the platforms do not all
// report a chunk id.
func docKey(d Document) string {
	return d.KB + "\x1f" + d.DocumentKey + "\x1f" + d.Content
}

// sampled reports whether a search takes part in the experiment. The decision is
// a hash of the run id, so it is stable for a run (a retried tool call stays in
// the same arm) and independent of arrival order.
func sampled(runID string, rate float64) bool {
	if rate <= 0 || rate >= 1 {
		return true
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(runID))
	return float64(h.Sum64()%1000)/1000 < rate
}

// logVariant records which arm a search ran in. It is the only place the two arms
// are distinguishable, so an A/B comparison reads this line rather than guessing
// from latency.
func logVariant(ctx context.Context, variant string, queries, docs int, err error) {
	if err != nil {
		slog.DebugContext(ctx, "[kbtool] query rewrite failed; searching the original question",
			"variant", variant, "error", err)
		return
	}
	slog.DebugContext(ctx, "[kbtool] retrieval", "variant", variant, "queries", queries, "docs", docs)
}
