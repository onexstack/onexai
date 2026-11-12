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

// Package kbtool turns knowledge-base records into the retrieval tool an agent
// calls.
//
// A knowledge base is reachable through more than one platform, and which one
// a record uses is a property of the record rather than of the deployment. So
// the tool is written once, against a Retriever, and each platform contributes
// an implementation: adding a platform is a new file, not a second tool with
// its own copy of the formatting, merging and error handling.
package kbtool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/trace"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	"github.com/onexstack/onexai/internal/pkg/kbfingerprint"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ToolName is the name the model sees. It is stable on purpose: a prompt or an
// approval pattern that names it must keep working when the platforms behind it
// change.
const ToolName = "search_knowledge_base"

// defaultTopK is the number of passages returned when neither the record nor
// the deployment asks for a specific count.
const defaultTopK = 5

// limitTopK resolves a record's own TopK against the count the tool asked for.
//
// A record may tighten its own contribution but never widen it: the deployment's
// global top-k is the ceiling the whole run was budgeted for. Zero on either
// side means that side named no count, so the other one applies unchanged.
func limitTopK(recordTopK, requested int) int {
	if recordTopK > 0 && (requested <= 0 || recordTopK < requested) {
		return recordTopK
	}
	return requested
}

// defaultMaxCharsPerChunk bounds one passage. The number is a compromise: a
// programming answer usually needs the surrounding code, and a whole chapter is
// more than any one tool result should hand over. The truncation is marked so
// the model does not read a cut passage as a complete one.
const defaultMaxCharsPerChunk = 1200

// Document is one retrieved passage.
type Document struct {
	// Source is the human-readable name of what the passage came from, when the
	// platform reports one. It is what the model is shown, so it holds a name or
	// nothing: a platform that identifies its documents by an opaque id and
	// keeps the title inside the text leaves this empty (the id belongs in
	// DocumentKey), and printing the id here would put a token the model can
	// neither read nor use into the one place a person-facing name goes.
	Source string
	// DocumentKey is the owning platform's identifier for the document the
	// passage came from — a name, an id, whatever that platform calls it.
	//
	// It is what a caller resolving "which document is this" uses, and it is
	// deliberately not interpreted here: this service does not know any
	// corpus's address space. Several passages may share one key (a document
	// contributes more than one passage), which is why the source report
	// collapses them.
	DocumentKey string
	// ChunkID is the platform's identifier for the passage itself, when it
	// reports one. It is diagnostic: the agent path never renders it, but a
	// person debugging a dataset uses it to find the chunk in the platform.
	ChunkID string
	// URL is the platform's own address for the document, when it reports one.
	// Never built here, and empty for a platform that has no address to give.
	URL string
	// Content is the passage itself.
	Content string
	// Score is the platform's relevance score. Scores from different platforms
	// are not strictly comparable, which is why they are only used to order the
	// merged result and never shown as an absolute quality claim.
	Score float64
	// VectorScore is the vector-only similarity, when the platform reports it.
	// Score stays the ordering field; this one exists so a retrieval test can
	// tell a wording problem from an embedding problem.
	VectorScore float64
	// KB names the knowledge-base record this passage came from, as its display
	// name. It is how a run that declared several bases keeps them apart in the
	// rendering: the document name alone cannot say which base holds it.
	//
	// Empty means the passage cannot be attributed to exactly one record — a
	// platform that does not report a source id, or a chunk that names an id no
	// record claims. render then omits the suffix rather than guessing.
	KB string
}

// Retriever queries one knowledge base.
//
// Retrieve must be safe for concurrent use: the tool queries every configured
// base at the same time, and one slow platform must not delay the others.
type Retriever interface {
	// Name identifies the knowledge base, for the tool description and errors.
	Name() string
	// Retrieve returns the passages matching query, best first.
	Retrieve(ctx context.Context, query string, topK int) ([]Document, error)
}

// AuditEntry is one search as the audit writer sees it.
type AuditEntry struct {
	UserID         string
	Agent          string
	KnowledgeBases string
	Documents      int
	Passages       int
	Bytes          int
	TraceID        string
}

// AuditFunc records one search. It must not block the caller.
type AuditFunc func(AuditEntry)

// Config configures one retrieval tool.
type Config struct {
	// Retrievers are the knowledge bases this tool searches, in the order the
	// agents declared them.
	Retrievers []Retriever
	// GlobalTopK caps the merged result and is also the count every retriever is
	// asked for. A record may lower its own contribution via its topK, but it
	// cannot raise it above this ceiling. <=0 means defaultTopK.
	GlobalTopK int
	// MaxChars caps the rendered result, so a chatty knowledge base cannot
	// consume the run's whole context. <=0 disables the cap.
	MaxChars int
	// MaxCharsPerChunk caps one passage. A single chapter-sized chunk handed to
	// a local model is the cheapest way to copy a course, so the cap is per
	// passage rather than only on the total. <=0 means defaultMaxCharsPerChunk.
	MaxCharsPerChunk int
	// WatermarkKey, when non-empty, weaves the asking user's mark into every
	// passage. The key is the kbfingerprint subkey ox-kbtrace derives, so a
	// copied passage can be decoded back to the account it was served to.
	WatermarkKey []byte
	// Audit, when non-nil, receives one entry per search. It must not block:
	// the writer behind it is expected to buffer and drop rather than make a
	// retrieval wait on a database insert.
	Audit AuditFunc
	// Grader reorders the merged passages and reports how well they cover the
	// question. Nil means the platforms' own ranking is used as-is.
	Grader Grader
	// MultiQuery, when non-nil, rewrites the question into several phrasings and
	// fuses what each retrieves. Nil keeps one query per search.
	MultiQuery *MultiQuery
}

// New builds the retrieval tool, or nil when no retriever is configured.
//
// Nil rather than an error: an agent that declares no knowledge base is an
// ordinary agent, and an agent that declares one that failed to configure
// should lose that capability, not its ability to answer.
func New(cfg Config) tool.BaseTool {
	if len(cfg.Retrievers) == 0 {
		return nil
	}
	if cfg.GlobalTopK <= 0 {
		cfg.GlobalTopK = defaultTopK
	}
	if cfg.MaxCharsPerChunk <= 0 {
		cfg.MaxCharsPerChunk = defaultMaxCharsPerChunk
	}
	return &retrievalTool{cfg: cfg}
}

// retrievalTool is the eino tool the model calls.
type retrievalTool struct {
	cfg Config
}

// Info advertises the tool, naming the knowledge bases it covers so the model
// can tell whether the question is worth asking it.
func (t *retrievalTool) Info(context.Context) (*schema.ToolInfo, error) {
	names := make([]string, 0, len(t.cfg.Retrievers))
	for _, r := range t.cfg.Retrievers {
		if r != nil {
			names = append(names, r.Name())
		}
	}
	return &schema.ToolInfo{
		Name: ToolName,
		Desc: fmt.Sprintf("Search the knowledge base for information relevant to the user's question. "+
			"Covered knowledge bases: %s. Use this tool when the answer depends on stored reference "+
			"material rather than on your own knowledge.", strings.Join(names, ", ")),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "The question or search query to find relevant knowledge",
				Required: true,
			},
		}),
	}, nil
}

// input is the tool's argument shape.
type input struct {
	Query string `json:"query"`
}

// InvokableRun queries every knowledge base and returns the merged best hits.
//
// The bases are queried concurrently: they are independent network calls, and
// doing them in sequence would make the slowest platform the latency of the
// tool. Ordering is recovered by sorting afterwards, so concurrency costs no
// determinism.
//
// A base that fails does not fail the call — a partial answer is worth more to
// the model than an error naming a platform it cannot do anything about — but
// the failure is not hidden either: the result says which bases were missing,
// so the model does not read a thin answer as a complete one. Only when *every*
// base failed is the call an error, because then "no results" and "no source
// answered" would be the same empty string.
func (t *retrievalTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in input
	if err := decodeArgs(argumentsInJSON, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query cannot be empty")
	}

	res := t.search(ctx, in.Query)

	// The error text is model-visible (the run-lifecycle wrappers render it), so
	// it names the sources and nothing else: an endpoint or a response body is
	// operator information the model can neither act on nor should be shown.
	if res.queried > 0 && len(res.failed) == res.queried {
		return "", fmt.Errorf("knowledge base search failed: none of the %d configured sources (%s) could be reached",
			res.queried, strings.Join(res.failed, ", "))
	}

	note := failureNote(res.failed)
	if len(res.docs) == 0 {
		t.logRetrieval(ctx, res, 0, CoverageNone)
		if note == "" {
			return "No relevant information found in the knowledge base.", nil
		}
		// A partial outage is not "nothing found": say why the search came back
		// thin instead of letting the model conclude the base is empty.
		return note, nil
	}

	docs := res.docs
	// Grading runs on the merged set, before the cap: its whole point is to
	// decide which passages are worth the cap, and a passage the platform ranked
	// sixth is exactly the one a grader exists to promote or drop.
	//
	// coverage is left at its zero value when no grader is configured, and the
	// zero value is not CoverageNone — "no verdict" and "nothing relevant" have
	// to stay apart, because only one of them is a claim about the material. An
	// ungraded run therefore renders exactly what it rendered before grading
	// existed, which is what makes turning the grader on a change in the answer
	// rather than a change in configuration.
	var coverage Coverage
	if t.cfg.Grader != nil {
		docs, coverage = t.cfg.Grader.Grade(ctx, in.Query, docs)
	}
	if coverage == CoverageNone {
		// Nothing retrieved was on topic. The passages are withheld rather than
		// shown anyway: material the grader rated irrelevant is exactly what the
		// model would reach for when it has nothing better, and the honest
		// answer is that the corpus does not cover this.
		return note + coverageNote(coverage), nil
	}
	if t.cfg.GlobalTopK > 0 && len(docs) > t.cfg.GlobalTopK {
		docs = docs[:t.cfg.GlobalTopK]
	}
	docs = capPassages(docs, t.cfg.MaxCharsPerChunk)
	docs = watermarkPassages(ctx, docs, t.cfg.WatermarkKey)
	text, rendered := render(docs, t.cfg.MaxChars)
	t.reportSources(ctx, rendered)
	t.logRetrieval(ctx, res, len(rendered), coverage)
	t.recordAudit(ctx, rendered)
	return note + coverageNote(coverage) + text, nil
}

// logRetrieval records what one search actually did.
//
// # Why a log line rather than a metric
//
// The number this exists to expose — how often a run that declared a knowledge
// base came back with nothing usable — is not a rate anybody watches; it is a
// question somebody asks once ("is the course in the tool's scope at all?"), and
// then answers by reading the line. A counter would also have to be plumbed from
// the metric registry through the builder into this package to say the same
// thing, and this package's job is retrieval.
//
// It is logged at Info on the way out and at Warn when nothing relevant came
// back, because the two have different audiences: a normal search is diagnostic
// colour, and an empty one is the shape of the failure this whole path is prone
// to — a run that answers from its own memory while the log says the corpus was
// asked.
func (t *retrievalTool) logRetrieval(ctx context.Context, res retrievalResult, rendered int, coverage Coverage) {
	if len(res.failed) > 0 {
		slog.WarnContext(ctx, "knowledge base search could not reach every source",
			"sources_queried", res.queried,
			"sources_failed", strings.Join(res.failed, ", "))
	}
	attrs := []any{
		"sources_queried", res.queried,
		"passages_found", len(res.docs),
		"passages_rendered", rendered,
		"coverage", string(coverage),
		"top_score", topScore(res.docs),
	}
	// Who asked is the one field a leak investigation needs and a rate limit
	// cannot reconstruct. The subject is attached by the run path, so a missing
	// one is a fact worth recording rather than a reason to fail the search.
	if subj, ok := authz.SubjectFromContext(ctx); ok {
		attrs = append(attrs, "subject_kind", subj.Kind, "subject_id", subj.UserID)
	}
	// Which bases and documents were read is the other half of a leak
	// investigation: "this account retrieved 200 distinct documents in an hour"
	// is a fact a rate limit cannot reconstruct after the fact.
	if kbs := uniqueStrings(docField(res.docs, func(d Document) string { return d.KB })); len(kbs) > 0 {
		attrs = append(attrs, "knowledge_bases", strings.Join(kbs, ","))
	}
	if docs := uniqueStrings(docField(res.docs, func(d Document) string { return d.Source })); len(docs) > 0 {
		attrs = append(attrs, "documents", len(docs))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs = append(attrs, "trace_id", sc.TraceID().String())
	}
	if len(res.docs) == 0 || coverage == CoverageNone {
		slog.WarnContext(ctx, "knowledge base search returned nothing usable for the question", attrs...)
		return
	}
	slog.InfoContext(ctx, "knowledge base search finished", attrs...)
}

// topScore is the best score the platforms reported, or 0 when there is none.
//
// It is logged and never acted on: the scores of two platforms are not on one
// scale, so the only thing the number can honestly support is "something was
// more like the question than nothing was".
func topScore(docs []Document) float64 {
	var best float64
	for _, doc := range docs {
		if doc.Score > best {
			best = doc.Score
		}
	}
	return best
}

// coverageNote is the line the model reads before the passages, when the tool
// has something to say about how much of the question they answer.
//
// It is a statement of fact and nothing else: the rule that follows from it —
// answer from the material where the material answers, and say so where it does
// not — is the prompt's, and a tool that wrote it would be a second copy of a
// policy it does not own.
//
// The empty Coverage — no grader ran — and CoverageSufficient both get no line.
// Sufficient would be telling the model what the presence of passages already
// implies, and every claim the model has to read past is one it can misread; the
// empty one is not a verdict at all.
func coverageNote(c Coverage) string {
	switch c {
	case CoverageNone:
		// Kept byte for byte as it was before grading existed: a deployment
		// whose grading is off must answer the same way, because the model's
		// answer to "no relevant information" and "nothing on topic" is the same
		// and a difference here would be an accident of configuration.
		return "No relevant information found in the knowledge base.\n\n"
	case CoveragePartial:
		return "Note: the passages below are on the topic but do not fully answer the question; " +
			"say which part they cover and where the rest of your answer comes from.\n\n"
	default:
		return ""
	}
}

// reportSources tells the run which documents the model was just given.
//
// It takes the passages render actually wrote rather than everything the bases
// returned; render says why that distinction is the point of the report.
//
// A tool that runs with no sink — a caller that renders no sources — is the
// ordinary case, not an error: the retrieval happened, the model read it, and
// only the report has nowhere to go.
func (t *retrievalTool) reportSources(ctx context.Context, docs []Document) {
	sink := agentapi.SourceSinkFrom(ctx)
	if sink == nil {
		return
	}
	if refs := sourceRefs(docs); len(refs) > 0 {
		sink.Sources(ctx, refs)
	}
}

// sourceRefs collapses passages to the documents they came from.
//
// Several passages of one document are one source: a reader opening the list
// wants the documents it was built from, and eight entries of the same chapter
// are not eight sources. The first occurrence wins, which preserves the order
// the merge produced — the document whose best passage ranked highest leads.
//
// A passage that names its document in no way at all is dropped rather than
// reported: a reference with no key, no address and no title is not a
// reference, because whoever receives it can render nothing from it. Two
// *different* documents that name nothing but share a title do collapse into
// one; the key that would tell them apart is the one that is missing, and the
// alternative is showing the same line twice.
func sourceRefs(docs []Document) []agentapi.SourceRef {
	seen := make(map[string]struct{}, len(docs))
	refs := make([]agentapi.SourceRef, 0, len(docs))
	for _, doc := range docs {
		key := doc.DocumentKey
		if key == "" {
			key = doc.URL
		}
		if key == "" {
			key = doc.Source
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, agentapi.SourceRef{
			DocumentKey: doc.DocumentKey,
			Title:       doc.Source,
			URL:         doc.URL,
		})
	}
	return refs
}

// retrievalResult is the merged outcome of one fan-out.
//
// It carries the failed sources because "no documents" and "no source answered"
// are different answers: the first is information, the second is an outage the
// model must not read as "the knowledge base is empty".
type retrievalResult struct {
	docs    []Document
	failed  []string // display names of the errored sources, in declaration order
	queried int      // number of non-nil sources attempted
}

// search runs the configured retrieval strategy.
//
// The single-query path is the default and the fallback. The multi-query path is
// taken only when configured and sampled, and degrades back to a single query if
// the rewrite call fails: a rewriter that is down must cost the search an
// experiment, not its result.
func (t *retrievalTool) search(ctx context.Context, query string) retrievalResult {
	mq := t.cfg.MultiQuery
	if mq == nil || mq.Rewrite == nil || !sampled(agentapi.RunIDFromContext(ctx), mq.SampleRate) {
		res := t.retrieveAll(ctx, query)
		logVariant(ctx, "single", 1, len(res.docs), nil)
		return res
	}

	rewrites, err := mq.Rewrite(ctx, query, mq.Count)
	if err != nil || len(rewrites) == 0 {
		logVariant(ctx, "multiquery", 1, 0, err)
		return t.retrieveAll(ctx, query)
	}

	queries := append([]string{query}, rewrites...)
	results := make([]retrievalResult, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			results[i] = t.retrieveAll(ctx, q)
		}(i, q)
	}
	wg.Wait()

	// The original query's failures are the ones the model is told about: a
	// failed rewrite would otherwise read as a partially broken knowledge base.
	res := results[0]
	lists := make([][]Document, len(results))
	for i := range results {
		lists[i] = results[i].docs
	}
	// Fusion keeps candidates per knowledge base, so a passage two records share
	// can still arrive twice; the same collapse as the single-query path applies.
	res.docs = dedupeChunks(fuseRRF(lists, 0))
	logVariant(ctx, "multiquery", len(queries), len(res.docs), nil)
	return res
}

// retrieveAll queries every retriever concurrently and merges the results,
// best first.
func (t *retrievalTool) retrieveAll(ctx context.Context, query string) retrievalResult {
	// Each goroutine writes its own slot, so no lock is needed and the failure
	// list keeps the declaration order rather than the order the goroutines
	// happened to finish in.
	type slot struct {
		name   string
		failed bool
		docs   []Document
	}
	slots := make([]slot, len(t.cfg.Retrievers))

	// Each base is asked for at least as many passages as the merged result
	// needs, so the merge has something to choose between.
	topK := t.cfg.GlobalTopK
	if topK <= 0 {
		topK = defaultTopK
	}

	var (
		wg      sync.WaitGroup
		queried int
	)
	for i, r := range t.cfg.Retrievers {
		if r == nil {
			continue
		}
		queried++
		slots[i].name = r.Name()
		wg.Add(1)
		go func(i int, r Retriever) {
			defer wg.Done()
			docs, err := r.Retrieve(ctx, query, topK)
			if err != nil {
				// Recorded, not reported here: InvokableRun turns the failure
				// list into model-visible text.
				slots[i].failed = true
				return
			}
			slots[i].docs = docs
		}(i, r)
	}
	wg.Wait()

	res := retrievalResult{queried: queried}
	for _, s := range slots {
		if s.failed {
			res.failed = append(res.failed, s.name)
		}
		res.docs = append(res.docs, s.docs...)
	}
	sortDocs(res.docs)
	// Two records can address the same dataset with different retrieval knobs;
	// they then run as separate requests and the same passage arrives twice.
	// After sorting the first copy is the best-scored one, so keep it.
	res.docs = dedupeChunks(res.docs)
	return res
}

// dedupeChunks drops repeated passages by the platform's own chunk id.
//
// Only a non-empty ChunkID is a usable identity: Dify reports none, and two
// different passages must not be collapsed just because neither platform named
// them. The input is already sorted best-first, so the surviving copy is the
// higher-ranked one.
func dedupeChunks(docs []Document) []Document {
	seen := make(map[string]struct{}, len(docs))
	out := docs[:0]
	for _, doc := range docs {
		if doc.ChunkID != "" {
			if _, ok := seen[doc.ChunkID]; ok {
				continue
			}
			seen[doc.ChunkID] = struct{}{}
		}
		out = append(out, doc)
	}
	return out
}

// sortDocs orders the merged passages best-first.
//
// The tie-break is explicit rather than left to the sort's stability: the
// passages arrive from concurrent goroutines, so equal scores would otherwise
// order by whichever call returned first, and the same question would produce
// different context on different runs.
func sortDocs(docs []Document) {
	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].Score != docs[j].Score {
			return docs[i].Score > docs[j].Score
		}
		if docs[i].Source != docs[j].Source {
			return docs[i].Source < docs[j].Source
		}
		if docs[i].KB != docs[j].KB {
			return docs[i].KB < docs[j].KB
		}
		return docs[i].Content < docs[j].Content
	})
}

// failureNote is the caveat prepended when some — but not all — sources could
// not be reached. It names the sources and nothing else: a partial answer
// presented as a complete one is the failure this exists to prevent.
func failureNote(failed []string) string {
	if len(failed) == 0 {
		return ""
	}
	return fmt.Sprintf("Note: results may be incomplete; %d knowledge base(s) could not be reached: %s.\n\n",
		len(failed), strings.Join(failed, ", "))
}

// render formats the merged passages for the model, and reports which of them
// it actually wrote.
//
// # Why it hands its input back
//
// The text is bounded by maxChars and the cut happens *inside* this loop, so
// this is the only place that knows which passages a model was given. Whoever
// reports "these are the documents behind the answer" has to report that same
// set: one taken from the retrieved list would name documents whose passages
// were cut for length, which is a claim about a reading that never happened.
//
// # The budget is counted in characters, and that is load-bearing
//
// This is the tool whose output another layer may also cap: the engine's
// context guard trims any single tool message over agent.max-tool-output-chars,
// keeping a head and a tail and dropping what is between them. Counting this
// budget in bytes while that one counts characters would make the two
// disagree by whatever the content's byte-to-character ratio happens to be —
// so whether the generic guard ever touches a retrieval result would depend on
// how much CJK versus ASCII a chapter contains, and the passages it dropped
// would be the middle of a relevance-ordered list, which is the part that
// matters. Same unit, and a budget set below that cap, is what keeps the cut
// here: this one is at a document boundary, and it says so.
//
// # Why the knowledge base is printed and the document key is not
//
// A run may search several bases at once, so "which base held this" is a fact
// the model cannot recover from the passage alone — that is the KB line. The
// document key stays out because it is the owning platform's identifier, not a
// name: Source is the field a person-facing name goes in, and it is filled only
// when the platform reports one.
// watermarkPassages writes the asking user's mark into every passage.
//
// Only a user subject is marked: a service-account caller has no person to name,
// and marking it with the service name would make a leak look like the
// platform's own doing. A missing key or subject returns the passages
// unchanged, which is the same result as the feature being off.
func watermarkPassages(ctx context.Context, docs []Document, key []byte) []Document {
	if len(key) == 0 {
		return docs
	}
	subj, ok := authz.SubjectFromContext(ctx)
	if !ok || subj.Kind != authz.SubjectKindUser || subj.UserID == "" {
		return docs
	}
	out := make([]Document, len(docs))
	copy(out, docs)
	for i := range out {
		out[i].Content = kbfingerprint.Weave(out[i].Content, key, subj.UserID, out[i].KB, out[i].Source)
	}
	return out
}

// recordAudit hands the search to the writer, if one is configured. It reports
// only what the model actually received (rendered), not what was retrieved:
// naming a passage the cap omitted would be a claim about a reading that did
// not happen.
func (t *retrievalTool) recordAudit(ctx context.Context, rendered []Document) {
	if t.cfg.Audit == nil {
		return
	}
	entry := AuditEntry{
		Passages:       len(rendered),
		KnowledgeBases: strings.Join(uniqueStrings(docField(rendered, func(d Document) string { return d.KB })), ","),
		Documents:      len(uniqueStrings(docField(rendered, func(d Document) string { return d.Source }))),
	}
	for _, d := range rendered {
		entry.Bytes += len(d.Content)
	}
	if subj, ok := authz.SubjectFromContext(ctx); ok {
		entry.UserID = subj.UserID
	}
	if agents := contextx.AgentNames(ctx); len(agents) > 0 {
		entry.Agent = agents[0]
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		entry.TraceID = sc.TraceID().String()
	}
	t.cfg.Audit(entry)
}

// docField extracts one non-empty string field from every document, in order.
func docField(docs []Document, field func(Document) string) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		if v := field(d); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// uniqueStrings removes duplicates while keeping the first occurrence, so a
// log line names each base or document once.
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// capPassages truncates each passage to limit runes and marks the cut. It
// copies the slice so the reportSources/logRetrieval numbers still describe
// what was read, not a caller's slice that was modified behind its back.
func capPassages(docs []Document, limit int) []Document {
	if limit <= 0 || len(docs) == 0 {
		return docs
	}
	out := make([]Document, len(docs))
	copy(out, docs)
	for i := range out {
		runes := []rune(out[i].Content)
		if len(runes) > limit {
			out[i].Content = string(runes[:limit]) + "…(passage truncated)"
		}
	}
	return out
}

func render(docs []Document, maxChars int) (string, []Document) {
	var (
		sb       strings.Builder
		rendered = make([]Document, 0, len(docs))
		used     int // characters written, matching the unit of maxChars
	)
	write := func(s string) {
		sb.WriteString(s)
		used += utf8.RuneCountInString(s)
	}
	write(fmt.Sprintf("Found %d relevant documents from the knowledge base:\n\n", len(docs)))
	for i, doc := range docs {
		write(fmt.Sprintf("--- Document %d (score: %.3f) ---\n", i+1, doc.Score))
		if doc.KB != "" {
			write(fmt.Sprintf("Knowledge base: %s\n", doc.KB))
		}
		if doc.Source != "" {
			write(fmt.Sprintf("Source: %s\n", doc.Source))
		}
		write(strings.TrimSpace(doc.Content))
		write("\n\n")
		// Written, so it was read — including the passage that trips the budget,
		// which is in the text above this check.
		rendered = append(rendered, doc)
		if maxChars > 0 && used >= maxChars {
			// Cut at a document boundary: a truncated sentence is worse than a
			// missing one, because the model cannot tell it was cut.
			return sb.String() + "(more results omitted)\n", rendered
		}
	}
	return sb.String(), rendered
}

// decodeArgs unmarshals the model's tool arguments.
//
// The error names the shape it expected: the model sees this text and can
// correct itself, so a bare "invalid character" is a wasted round.
func decodeArgs(argumentsInJSON string, out any) error {
	if err := json.Unmarshal([]byte(argumentsInJSON), out); err != nil {
		return fmt.Errorf("expected an object with a string %q field: %w", "query", err)
	}
	return nil
}

// GroupKBsByPlatform partitions knowledge-base records by the platform each
// addresses, keyed by the model.Platform* value, preserving selection order
// within a group.
//
// An agent may reference records from more than one platform, which is why this
// returns every group rather than picking one.
//
// A record whose platform is unstated is resolved by KnowledgeBaseM.InferPlatform,
// so rows written before the field existed keep landing where they used to.
//
// A record with no usable pointer for its platform is *not* dropped here: this
// function answers "which platform", which is a property of the record, and the
// retriever constructors are what return nil for a record nothing can query.
func GroupKBsByPlatform(kbs []*model.KnowledgeBaseM) map[string][]*model.KnowledgeBaseM {
	out := make(map[string][]*model.KnowledgeBaseM)
	for _, kb := range kbs {
		if kb == nil {
			continue
		}
		p := kb.InferPlatform()
		out[p] = append(out[p], kb)
	}
	return out
}
