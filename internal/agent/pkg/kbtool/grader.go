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
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/modeljson"
)

// Coverage says how much of a question the retrieved material answers.
//
// It exists because the alternative is the model guessing. A run that declared a
// knowledge base and received passages has no way to tell "the corpus covers
// this and these are the passages" from "the corpus was searched and the best it
// had was adjacent" — the tool result looks the same either way, and the model
// resolves the ambiguity the way a language model always does, by answering from
// its own memory. Saying which case it is lets the prompt's rule ("answer from
// the material where the material answers; label what it does not cover") act on
// a fact rather than on an inference.
type Coverage string

const (
	// CoverageSufficient means at least one passage answers the question.
	CoverageSufficient Coverage = "sufficient"
	// CoveragePartial means some passages are on topic but none answers it.
	CoveragePartial Coverage = "partial"
	// CoverageNone means nothing retrieved was on topic.
	CoverageNone Coverage = "none"
)

// Grader re-scores retrieved passages against the question.
//
// # Why this runs at all, when the platform already returns a similarity
//
// A platform score orders the passages a search *returned*; it cannot say
// whether any of them is an answer, and it is not comparable across platforms —
// a run may search two corpora whose scores share no scale. A grader answers
// both: it reads the question, so it can rank passages by "which of these
// answers it" rather than "which of these is closest to these words", and its
// verdict is a judgement rather than a number to be compared.
//
// It also replaces a reranker. RAGFlow can rerank, but only with a model its
// tenant has added, and a deployment without one gets no reordering at all
// rather than a weaker one. This is a capability that is present or absent, so
// the retrieval it grades is the same everywhere.
type Grader interface {
	// Grade returns the passages worth showing, best first, and how well they
	// cover the question.
	//
	// It must never drop every passage: a grader that dislikes all of them is
	// more likely to be wrong about the question than the retrieval was, and an
	// empty result is indistinguishable from an empty corpus. Relevance is
	// carried by Coverage and by the order, not by absence.
	Grade(ctx context.Context, query string, docs []Document) ([]Document, Coverage)
}

// GraderConfig configures the model-backed grader.
type GraderConfig struct {
	// Model rates the passages. Required; a nil model means no grader.
	Model model.BaseChatModel
	// MaxPassages caps how many passages one call is given. Zero means
	// defaultGradePassages.
	MaxPassages int
}

// defaultGradePassages bounds one grading call.
//
// The passages are all sent in one request rather than one each, unlike
// pkg/rag's per-chunk scoring: a RAGFlow passage is already a chunk-sized
// excerpt, so a per-passage call would spend a request to rank a paragraph —
// and the ranking only has to be good enough to reorder a handful of them,
// which one call over the numbered list does.
const defaultGradePassages = 12

// NewGrader builds the model-backed grader, or nil when no model is configured.
//
// Nil rather than an error, and nil rather than a grader that returns everything
// ungraded: the caller reads a nil Grader as "no grading configured", which is
// the ordinary state of a deployment that has not set a model, and the retrieval
// tool treats it exactly as it treated grading before it existed.
func NewGrader(cfg GraderConfig) Grader {
	if cfg.Model == nil {
		return nil
	}
	if cfg.MaxPassages <= 0 {
		cfg.MaxPassages = defaultGradePassages
	}
	return &modelGrader{cfg: cfg}
}

type modelGrader struct {
	cfg GraderConfig
}

// Grade asks the model to score every passage in one call.
//
// Every failure — an unreachable model, an unparsable reply, a reply that names
// no passage — degrades to the ungraded result rather than an error. Grading is
// an improvement to an answer that the retrieval already has the material for,
// so losing it must cost the answer nothing; the log is where a grader that is
// broken in production shows up, because the answer itself will look normal.
func (g *modelGrader) Grade(ctx context.Context, query string, docs []Document) ([]Document, Coverage) {
	if len(docs) == 0 {
		return docs, CoverageNone
	}
	graded := docs
	if len(graded) > g.cfg.MaxPassages {
		graded = graded[:g.cfg.MaxPassages]
	}

	resp, err := g.cfg.Model.Generate(ctx, []*schema.Message{schema.UserMessage(gradePrompt(query, graded))})
	if err != nil || resp == nil {
		slog.WarnContext(ctx, "kb grading failed; passages are shown in the order the platforms ranked them",
			"passages", len(graded), "error", errText(err))
		return docs, coverageOf(docs, nil)
	}

	scores, err := parseGrades(resp.Content, len(graded))
	if err != nil {
		slog.WarnContext(ctx, "kb grading returned nothing usable; passages are shown in the order the platforms ranked them",
			"passages", len(graded), "error", err.Error())
		return docs, coverageOf(docs, nil)
	}
	return reorderByGrade(graded, scores), coverageOf(graded, scores)
}

// errText renders an error for a log field without letting a nil error print as
// "<nil>".
func errText(err error) string {
	if err == nil {
		return "empty response"
	}
	return err.Error()
}

// gradePrompt lists the passages and asks for one score per index.
//
// The scale is the one pkg/rag uses, so a deployment that has tuned either has
// tuned both, and the anchors are spelled out because "0-10" alone means
// whatever the model's last few examples meant.
func gradePrompt(query string, docs []Document) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `评估下面每段资料对回答问题的有用程度。

问题：%s

`, query)
	for i, doc := range docs {
		label := doc.Source
		if label == "" {
			label = doc.KB
		}
		fmt.Fprintf(&sb, "【资料 %d】", i)
		if label != "" {
			fmt.Fprintf(&sb, "（%s）", label)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.TrimSpace(doc.Content))
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, `严格返回 JSON，不要有任何其他内容：
{"scores": [{"index": <资料编号>, "score": <0-10>}]}

评分参考：0=与问题无关，3=仅同一主题，7=提供了回答所需的大部分内容，10=直接回答了问题。
每一段资料都要给一个分数，即使为 0。`)
	return sb.String()
}

// grade is one parsed score.
type grade struct {
	Index int `json:"index"`
	Score int `json:"score"`
}

// parseGrades reads the model's scores, keyed by passage index.
//
// A reply that omits an index leaves that passage ungraded rather than failing
// the call: the indices are the model's own numbering, and losing one passage's
// score is not a reason to discard the others. An index outside the list is
// dropped for the same reason it exists — the model repeating a number is not a
// claim about a passage.
func parseGrades(raw string, n int) (map[int]int, error) {
	var parsed struct {
		Scores []grade `json:"scores"`
	}
	if err := json.Unmarshal([]byte(modeljson.StripFence(raw)), &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Scores) == 0 {
		return nil, fmt.Errorf("the reply named no passages")
	}
	out := make(map[int]int, len(parsed.Scores))
	for _, g := range parsed.Scores {
		if g.Index < 0 || g.Index >= n {
			continue
		}
		out[g.Index] = g.Score
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("every scored index was outside the %d passages sent", n)
	}
	return out, nil
}

// reorderByGrade returns the passages best first by the grader's score.
//
// A passage the grader did not score keeps the position the platforms gave it
// and sorts after the graded ones: the grader did not say it is irrelevant, so
// it cannot be demoted on that basis, but it also cannot outrank a passage the
// grader did read and rate.
//
// The order is total and stable: equal scores keep their relative order, so the
// same question and the same passages produce the same context on every run.
func reorderByGrade(docs []Document, scores map[int]int) []Document {
	type graded struct {
		doc     Document
		score   int
		graded  bool
		ordinal int
	}
	items := make([]graded, len(docs))
	for i, doc := range docs {
		score, ok := scores[i]
		items[i] = graded{doc: doc, score: score, graded: ok, ordinal: i}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].graded != items[j].graded {
			return items[i].graded
		}
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].ordinal < items[j].ordinal
	})
	out := make([]Document, 0, len(items))
	for _, item := range items {
		out = append(out, item.doc)
	}
	return out
}

// gradeSufficient is the score at which a passage counts as answering the
// question rather than merely being about it. It is the anchor the prompt gives
// for 7, and it is a threshold on a judgement the model was asked for, not a
// similarity — which is why it is a constant here rather than a configured
// float: an operator tuning "how similar is similar enough" is tuning the
// platform's threshold, and this one is "did it answer".
const gradeSufficient = 7

// coverageOf reads the coverage verdict off the scores.
//
// Derived rather than asked for, because a second question to the same model
// about the same passages can only disagree with the first: the scores already
// say whether anything answered, and asking again is one more chance to be told
// something inconsistent.
//
// With no scores — grading is off, or it failed — the verdict falls back to the
// retrieval's own similarity scores, so the caller still gets an honest answer
// about whether anything was found. That fallback is deliberately coarse: it can
// separate "nothing" from "something", and it cannot claim more than that.
func coverageOf(docs []Document, scores map[int]int) Coverage {
	if len(docs) == 0 {
		return CoverageNone
	}
	if scores == nil {
		return CoveragePartial
	}
	best := 0
	for _, score := range scores {
		if score > best {
			best = score
		}
	}
	switch {
	case best >= gradeSufficient:
		return CoverageSufficient
	case best > 0:
		return CoveragePartial
	default:
		return CoverageNone
	}
}
