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

package di

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	larkSDK "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/options"
)

// resolveSkillRecords returns the lookup that turns a run's skill names into
// records.
//
// Resolution happens here rather than in the engine or the builder so neither
// has to know how a name becomes a record. A name that resolves to nothing is
// dropped silently: a run whose agent references a deleted skill must still
// answer, just without that skill.
func resolveSkillRecords(resources store.AIStore) func(context.Context, []string) []*model.SkillM {
	return func(ctx context.Context, names []string) []*model.SkillM {
		out := make([]*model.SkillM, 0, len(names))
		for _, name := range names {
			if record, err := resources.Skills().Get(ctx, model.EntryFilter(name)); err == nil && record != nil {
				out = append(out, record)
			}
		}
		return out
	}
}

// resolveKnowledgeBaseRecords returns the lookup that turns a run's
// knowledge-base names into records.
func resolveKnowledgeBaseRecords(resources store.AIStore) func(context.Context, []string) []*model.KnowledgeBaseM {
	return func(ctx context.Context, names []string) []*model.KnowledgeBaseM {
		out := make([]*model.KnowledgeBaseM, 0, len(names))
		for _, name := range names {
			if record, err := resources.KnowledgeBases().Get(ctx, model.EntryFilter(name)); err == nil && record != nil {
				out = append(out, record)
			}
		}
		return out
	}
}

// buildAgentBuilder assembles the deep-agent builder, or nil when there is no
// model to reason with.
//
// Everything optional is wired as a nil interface rather than a disabled one,
// so "not configured" and "configured but off" take the same path through the
// builder and cannot drift apart.
func buildAgentBuilder(
	cfg *Config, resources store.AIStore, model einomodel.BaseChatModel,
	skills *skill.Manager, drawer agentbuilder.ImageGenerator,
	kbs *kbToolSource, settings *settingstore.Store,
) *agentbuilder.Builder {
	opts := cfg.AgentOptions
	if model == nil {
		return nil
	}

	// The global system prompt comes from configuration or, failing that, from
	// the control plane's settings ConfigMap — so an operator can retune it
	// without a redeploy. Configuration wins: a deployment that pinned it
	// explicitly did so on purpose.
	//
	// The lookup is handed over as a func rather than resolved here, so the
	// builder reads it once per run: this function runs at start-up, and a value
	// read now would be frozen for the life of the process. The store is a
	// dependency rather than a package-level accessor because a dependency Wire
	// cannot see is one whose provider it silently drops.
	builderOpts := agentbuilder.Options{
		Description:         opts.Description,
		MaxIteration:        opts.MaxIteration,
		ExecuteSequentially: opts.ExecuteSequentially,
		DeepExtrasEnabled:   opts.DeepExtrasEnabled,
		ModelRetryMax:       opts.ModelRetryMax,
		SkillsDir:           opts.SkillsDir,
		SystemPrompt:        func() string { return settings.GetString(settingstore.KeySystemPrompt) },
		Prompts:             opts.Prompts,
		// The reducer's two knobs are the engine's own budgets, shared rather
		// than duplicated: the clear threshold is the same point at which the
		// in-run summarizer wakes up, so the cheap shedding runs first.
		ToolSearch: agentbuilder.ToolSearchOptions{
			Enabled:       opts.ToolSearch.Enabled,
			MinTools:      opts.ToolSearch.MinTools,
			AlwaysVisible: opts.ToolSearch.AlwaysVisible,
		},
		MultiModalRead: opts.MultimodalRead.Enabled,
		AgentsMD: agentbuilder.AgentsMDOptions{
			Enabled:         opts.AgentsMD.Enabled,
			Files:           opts.AgentsMD.Files,
			MaxBytes:        opts.AgentsMD.MaxBytes,
			PerFileMaxBytes: opts.AgentsMD.PerFileMaxBytes,
		},
		MaxToolOutputChars: opts.MaxToolOutputChars,
		OffloadClearTokens: agentbiz.InRunCompactionTrigger(opts.MaxContextTokens),
		OffloadTTL:         opts.CheckpointTTL,
		Failover:           buildFailover(cfg),
	}

	// A nil source is not handed over as a typed nil: the field is an interface,
	// and a typed nil pointer in it is non-nil to every `if x == nil` check on the
	// other side.
	var knowledgeBases agentbuilder.KnowledgeBaseTools
	if kbs != nil {
		knowledgeBases = kbs
	}

	return agentbuilder.NewBuilder(agentbuilder.Deps{
		ChatModel:      model,
		Skills:         skills,
		KnowledgeBases: knowledgeBases,
		ImageGen:       drawer,
		Vision:         buildVisionMiddleware(cfg, resources),
		Security:       buildSecurityReviewer(cfg),
		Grounding:      buildGroundingReviewer(cfg),
	}, builderOpts)
}

// buildGroundingReviewer builds the answer-against-evidence check, or nil when
// it is off.
//
// The evidence tool is named rather than inferred: only the knowledge-base
// tool's output is the material an answer is supposed to be built from, and a
// check that treated every tool result as evidence would flag an answer for
// disagreeing with a clock.
func buildGroundingReviewer(cfg *Config) agentbuilder.GroundingReviewer {
	opts := cfg.AgentOptions
	if opts.GroundingModel == nil {
		return nil
	}
	reviewer, err := chatmodel.NewChatModel(context.Background(), auxModelConfig(cfg, opts.GroundingModel))
	if err != nil || reviewer == nil {
		// Not fatal, for the same reason the grader's failure is not: the answer
		// exists, and the check was going to add a caveat to it.
		slog.Warn("answer grounding is configured but its model could not be built; answers will not be checked",
			"error", err)
		return nil
	}
	return agentbuilder.NewGroundingReview(reviewer, kbtool.ToolName)
}

// buildSecurityReviewer builds the outbound answer review, or nil when no
// reviewer model is configured.
func buildSecurityReviewer(cfg *Config) agentbuilder.SecurityReviewer {
	reviewer := reviewerModelConfig(cfg)
	if !reviewer.Enabled() {
		return nil
	}
	model, err := chatmodel.NewChatModel(context.Background(), reviewer)
	if err != nil || model == nil {
		return nil
	}
	return agentbuilder.NewSecurityReview(model)
}

// reviewerModelConfig resolves the auxiliary reviewer model, inheriting the
// primary model's endpoint and credentials when it does not name its own.
func reviewerModelConfig(cfg *Config) options.ChatModelConfig {
	if !cfg.AgentOptions.ReviewerModel.Enabled() {
		return options.ChatModelConfig{}
	}
	return auxModelConfig(cfg, cfg.AgentOptions.ReviewerModel)
}

// buildSkillManager builds the skill subsystem, or nil when it is off.
//
// A skills directory is what turns it on: without one there is nowhere to
// check a skill out, and the filesystem sandbox the deep agent would otherwise
// get has nothing to confine itself to.
func buildSkillManager(cfg *Config, resources store.AIStore) *skill.Manager {
	opts := cfg.AgentOptions
	if opts.SkillsDir == "" {
		return nil
	}
	// The secret store is what turns an inline credential into a named one.
	deps := skill.ProviderDeps{LarkClient: larkClientOrNil(cfg), Secrets: resources.Secrets()}
	return skill.NewManager(opts.SkillsDir, opts.Prompts.SkillPrefix, deps)
}

// kbToolSource turns a run's knowledge-base records into retrieval tools.
//
// It is a value rather than a closure so the two platform configurations are
// resolved once at startup instead of on every run.
type kbToolSource struct {
	dify    options.KnowledgeBaseOptions
	ragflow options.RAGFlowOptions
	// grader re-scores the merged passages. Nil when grading is off or no
	// reviewer model is configured, which the retrieval tool treats as "rank
	// them the way the platforms did".
	grader kbtool.Grader
	// watermarkKey marks each passage with the asking user's identity. Empty
	// when the feature is off or the configured key could not be decoded.
	watermarkKey []byte
	// audit receives one entry per search, or nil when no database is wired.
	audit kbtool.AuditFunc
	// multiquery rewrites each question and fuses the results. Nil keeps one
	// query per search.
	multiquery *kbtool.MultiQuery
}

// watermarkKey decodes agent.watermark.key, or returns nil when the feature is
// off or the value is unusable.
//
// Two encodings because the key is provisioned by an operator copying output
// from different tools: 64 hex characters is a hex digest, anything else is
// read as base64 (standard first, then raw URL). A value that is neither is a
// startup-log warning rather than a fatal error: the watermark is an
// attribution aid, and refusing to serve knowledge bases because of it would
// trade a forensic gap for an outage.
func watermarkKey(o *options.WatermarkOptions) []byte {
	if o == nil || !o.Enabled {
		return nil
	}
	s := strings.TrimSpace(o.Key)
	if s == "" {
		return nil
	}
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b
	}
	slog.Warn("agent.watermark.key is neither a 64-character hex string nor base64; the watermark is disabled")
	return nil
}

// probeQuery is the retrieval a health check asks. Its content does not matter:
// an answer of any kind — including no passages — proves the platform is
// reachable and the credential was accepted.
const probeQuery = "health check"

// Tools implements agentbuilder.KnowledgeBaseTools. Records are partitioned by
// which platform they address, and the retrievers that resulted are merged
// behind one tool: the model should not have to know which platform holds an
// answer.
func (s kbToolSource) Tools(_ context.Context, kbs []*model.KnowledgeBaseM) []tool.BaseTool {
	retrievers := s.retrieversFor(kbs)
	toolset := kbtool.New(kbtool.Config{
		Retrievers:   retrievers,
		GlobalTopK:   maxTopK(s.dify.TopK, s.ragflow.TopK),
		MaxChars:     maxTopK(s.dify.MaxChars, s.ragflow.MaxChars),
		Grader:       s.grader,
		WatermarkKey: s.watermarkKey,
		Audit:        s.audit,
		MultiQuery:   s.multiquery,
	})
	if toolset == nil {
		return nil
	}
	return []tool.BaseTool{toolset}
}

// Probe runs one minimal retrieval against one knowledge base.
//
// It asks the platform itself rather than reading configuration: whether a
// dataset exists, whether the API key is accepted and whether the network path
// is open are all the same question, and only a retrieval answers it. A full
// search would cost tokens' worth of ranking work per probe interval, so the
// probe asks for one passage.
func (s kbToolSource) Probe(ctx context.Context, kb *model.KnowledgeBaseM) (int, error) {
	retrievers := s.retrieversFor([]*model.KnowledgeBaseM{kb})
	if len(retrievers) == 0 {
		return 0, fmt.Errorf("no %s retrieval platform is configured", kb.Platform)
	}
	passages, err := retrievers[0].Retrieve(ctx, probeQuery, 1)
	if err != nil {
		return 0, err
	}
	return len(passages), nil
}

// retrieversFor builds the retrievers addressed by the given records: one per
// Dify record, and one per distinct retrieval setting among the RAGFlow
// records. Records whose platform is not configured are dropped, which is what
// leaves a knowledge base on an unconfigured platform with no retriever at all.
func (s kbToolSource) retrieversFor(kbs []*model.KnowledgeBaseM) []kbtool.Retriever {
	byPlatform := kbtool.GroupKBsByPlatform(kbs)

	var retrievers []kbtool.Retriever
	if s.dify.Enabled() {
		for _, kb := range byPlatform[model.PlatformDify] {
			if r := kbtool.NewDifyRetriever(kb, kbtool.DifyConfig{
				BaseURL: s.dify.BaseURL,
				APIKey:  s.dify.APIKey,
				Timeout: s.dify.Timeout,
			}); r != nil {
				retrievers = append(retrievers, r)
			}
		}
	}
	if s.ragflow.Enabled() {
		retrievers = append(retrievers,
			kbtool.NewRAGFlowRetrievers(byPlatform[model.PlatformRAGFlow], ragflowConfig(s.ragflow))...)
	}
	return retrievers
}

// ragflowConfig maps the deployment's RAGFlow options onto the retriever's own
// configuration.
//
// It is shared by the run-time retrievers and the console's retrieval test so
// the two cannot drift: a test that used a different threshold or rerank model
// would answer a question nobody asked.
func ragflowConfig(o options.RAGFlowOptions) kbtool.RAGFlowConfig {
	return kbtool.RAGFlowConfig{
		BaseURL:                o.BaseURL,
		APIKey:                 o.APIKey,
		SimilarityThreshold:    o.SimilarityThreshold,
		VectorSimilarityWeight: o.VectorSimilarityWeight,
		RerankID:               o.RerankID,
		Keyword:                o.Keyword,
		DocumentNameAsSource:   o.DocumentNameAsSource,
		Timeout:                o.Timeout,
	}
}

// maxTopK returns the larger of two optional limits, so a limit configured on
// either platform applies to the merged result.
func maxTopK(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildKnowledgeBaseTools builds the knowledge-base tool source, or nil when
// neither platform is configured.
func buildKnowledgeBaseTools(cfg *Config, audit kbtool.AuditFunc) *kbToolSource {
	opts := cfg.AgentOptions
	if !opts.KnowledgeBase.Enabled() && !opts.RAGFlow.Enabled() {
		return nil
	}
	source := kbToolSource{
		watermarkKey: watermarkKey(opts.Watermark),
		audit:        audit,
		dify:         ptr.From(opts.KnowledgeBase),
		ragflow:      ptr.From(opts.RAGFlow),
	}
	// Every platform's rendering budget is pulled under the engine's tool-output
	// cap here, once, where both numbers are known. Doing it at the source means
	// no downstream reader has to remember the relationship; warning about the
	// original values is warnRetrievalBudget's job.
	source.dify.MaxChars = retrievalCharsBudget(source.dify.MaxChars, opts.MaxToolOutputChars)
	source.ragflow.MaxChars = retrievalCharsBudget(source.ragflow.MaxChars, opts.MaxToolOutputChars)
	source.grader = buildKBGrader(cfg)
	source.multiquery = buildKBMultiQuery(cfg)
	return &source
}

// ProvideKBToolSource builds the knowledge-base subsystem, or nil when no
// retrieval platform is configured.
//
// It is a provider because two consumers need the same value: a run, which turns
// it into a retrieval tool, and the health reporter, which asks it whether each
// knowledge base answers. Two instances would not drift — the configuration is
// the same — but only one can be the thing an operator's probe actually
// exercised.
func ProvideKBToolSource(cfg *Config, audit kbtool.AuditFunc) *kbToolSource {
	return buildKnowledgeBaseTools(cfg, audit)
}

// buildKBGrader builds the knowledge-base grader, or nil when it is off.
//
// The model is its own rather than the reviewer's, so that enabling grading
// cannot enable anything else — see KbGradeModel. Provider, base URL and key
// fall back to the primary model's, which is what makes "kb-grade-model: {model:
// <name>}" a complete configuration on a deployment with one vendor.
func buildKBGrader(cfg *Config) kbtool.Grader {
	opts := cfg.AgentOptions
	if opts.KbGradeModel == nil {
		return nil
	}
	graderModel, err := chatmodel.NewChatModel(context.Background(), auxModelConfig(cfg, opts.KbGradeModel))
	if err != nil || graderModel == nil {
		// Not fatal: grading improves a retrieval result the run already has.
		// The log is the only signal, because without it the symptom is an
		// answer whose passages are merely in the platforms' order — which is
		// what a deployment with grading off looks like too. Validation rejects
		// a configured-but-unusable model at startup, so this is the runtime
		// failure, not the typo.
		slog.Warn("knowledge-base grading is configured but its model could not be built; retrieval results will not be graded",
			"error", err)
		return nil
	}
	return kbtool.NewGrader(kbtool.GraderConfig{Model: graderModel})
}

// buildKBMultiQuery builds the query rewriter, or nil when the experiment is off.
//
// It uses the run's primary model rather than requiring a model of its own: a
// rewrite is one short call, and needing a second model configuration to try an
// experiment is how an experiment never gets tried.
func buildKBMultiQuery(cfg *Config) *kbtool.MultiQuery {
	opts := cfg.AgentOptions
	if !opts.KbMultiQuery.Enabled {
		return nil
	}
	rewriteModel, err := chatmodel.NewChatModel(context.Background(), primaryModelConfig(cfg))
	if err != nil || rewriteModel == nil {
		// Not fatal: the search still runs, on the question as asked.
		slog.Warn("knowledge-base multi-query is enabled but its model could not be built; searches stay single-query",
			"error", err)
		return nil
	}
	rewrite := kbtool.NewRewriter(kbtool.RewriterConfig{Model: rewriteModel})
	return &kbtool.MultiQuery{
		Count:      opts.KbMultiQuery.Count,
		SampleRate: opts.KbMultiQuery.SampleRate,
		Rewrite:    rewrite,
	}
}

// retrievalCharsBudget reduces a retrieval rendering budget so the engine's
// context guard never touches the result.
//
// The guard trims any single tool message over agent.max-tool-output-chars by
// keeping a head and a tail and dropping what lies between them. For a
// retrieval result that is the worst cut available: the passages are ordered by
// relevance, so the ones it drops are the *middle-ranked* evidence, and the
// sources the tool reported would go on to name documents the model never read.
// The tool's own cut happens at a document boundary and says so, so the tool has
// to be the one that cuts. A tenth of headroom keeps the two from meeting on a
// rounding difference.
//
// A non-positive value on either side means "no budget configured" or "no cap",
// and then there is no relationship to enforce.
func retrievalCharsBudget(configured, toolOutputCap int) int {
	if configured <= 0 || toolOutputCap <= 0 {
		return configured
	}
	if safe := toolOutputCap * 9 / 10; configured > safe {
		return safe
	}
	return configured
}

// AgentConfigWarnings logs the configuration problems that are cheap to find at
// startup and expensive to find in production.
//
// They are warnings rather than failures: each describes a deployment that will
// run, just not the way its operator probably meant it to.
func AgentConfigWarnings(ctx context.Context, cfg *Config, resources store.AIStore) {
	warnMCPConflicts(ctx, resources)
	warnContextBudget(cfg)
	warnRetrievalBudget(cfg)
	warnRAGFlowDuplicateDatasets(ctx, resources)
	warnUnsearchableKnowledgeBases(ctx, resources)
}

// warnUnsearchableKnowledgeBases reports knowledge bases an agent names that
// will not be searched.
//
// Every way a declared base can go missing is silent at run time, and the three
// ways look identical from the model's side: the base is not in the tool's
// scope, so it contributes nothing, and an answer with nothing in it reads
// exactly like a corpus that does not cover the question. The retrievers drop
// such records on purpose — a record nothing can query should cost the run a
// capability, not the whole tool — so the only thing to add is a voice at
// startup, where the fix is.
//
// Only bases an agent actually names are reported: a record no agent references
// is a record being prepared, not a defect.
func warnUnsearchableKnowledgeBases(ctx context.Context, resources store.AIStore) {
	// Reported once per record however many agents name it: the fix is on the
	// record, and an inventory of which agents inherited the problem is a
	// second question.
	reported := make(map[string]struct{})
	_, agents, err := resources.Agents().List(ctx, where.L(-1))
	if err != nil {
		slog.ErrorContext(ctx, "could not list agents for the knowledge-base check", "error", err)
		return
	}
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		for _, name := range agent.EffectiveKnowledgeBases() {
			if _, done := reported[name]; done {
				continue
			}
			reported[name] = struct{}{}

			record, err := resources.KnowledgeBases().Get(ctx, model.EntryFilter(name))
			if err != nil || record == nil {
				slog.WarnContext(ctx,
					"an agent declares a knowledge base that no record matches; the base is not searched and the tool will not list it",
					"knowledge_base", name, "agent", agent.Name)
				continue
			}
			if !record.Enabled() {
				slog.WarnContext(ctx,
					"an agent declares a disabled knowledge base; the base is not searched",
					"knowledge_base", name, "agent", agent.Name, "status", record.Status)
				continue
			}
			// Both remaining platforms read DatasetID, so a record with no
			// dataset id is silently never searched.
			if missing := missingBackendID(record); missing != "" {
				slog.WarnContext(ctx,
					"an agent declares a knowledge base with no id for its backend; the base is not searched",
					"knowledge_base", name, "agent", agent.Name,
					"platform", record.InferPlatform(), "missing", missing)
			}
		}
	}
}

// missingBackendID names the empty field that keeps a record from being queried,
// or "" when the record has what its platform needs.
func missingBackendID(kb *model.KnowledgeBaseM) string {
	if strings.TrimSpace(kb.DatasetID) == "" {
		return "dataset_id"
	}
	return ""
}

// warnRetrievalBudget reports a knowledge base whose rendering budget is at or
// above the engine's tool-output cap.
//
// The cap wins — the context guard runs last — so the configured number is not
// the one in effect: the result is trimmed in the middle instead of at a
// document boundary, and the sources reported for the answer name passages the
// model never saw. buildKnowledgeBaseTools pulls the budget under the cap, so a
// deployment like this runs correctly; this warning exists because a silently
// corrected setting is one the operator cannot reason about, and because the
// corrected number is smaller than the one they chose, which they may want to
// revisit rather than accept.
func warnRetrievalBudget(cfg *Config) {
	opts := cfg.AgentOptions
	report := func(platform string, configured int) {
		if configured <= 0 || opts.MaxToolOutputChars <= 0 {
			return
		}
		if safe := retrievalCharsBudget(configured, opts.MaxToolOutputChars); safe != configured {
			slog.Warn("knowledge-base rendering budget is above the tool-output cap and was reduced; "+
				"raise agent.max-tool-output-chars if the full result should reach the model",
				"platform", platform,
				"configured_max_chars", configured,
				"effective_max_chars", safe,
				"max_tool_output_chars", opts.MaxToolOutputChars)
		}
	}
	if opts.KnowledgeBase != nil {
		report("dify", opts.KnowledgeBase.MaxChars)
	}
	if opts.RAGFlow != nil {
		report("ragflow", opts.RAGFlow.MaxChars)
	}
}

// warnRAGFlowDuplicateDatasets reports two RAGFlow knowledge bases that share a
// dataset id.
//
// Records with the same retrieval setting still share one request, where the
// first record wins attribution and the second name never appears on a result.
// Records that differ split into separate requests: both settings then apply,
// but the same passage can come back under both names (it is collapsed by chunk
// id afterwards), and which record a surviving copy is attributed to follows
// the score order rather than declaration order. Which record should exist at
// all is not this service's decision, so it says so with both settings and
// leaves the fix to whoever maintains the records.
func warnRAGFlowDuplicateDatasets(ctx context.Context, resources store.AIStore) {
	type claim struct {
		name      string
		threshold float64
		topK      int
	}
	first := make(map[string]claim) // dataset id -> the first record to claim it
	_, kbs, err := resources.KnowledgeBases().List(ctx, where.L(-1))
	if err != nil {
		slog.ErrorContext(ctx, "could not list knowledge bases for the RAGFlow check", "error", err)
		return
	}
	for _, kb := range kbs {
		if kb == nil || !kb.Enabled() || kb.InferPlatform() != model.PlatformRAGFlow {
			continue
		}
		id := kb.DatasetID
		name := kb.DisplayName
		if name == "" {
			name = kb.Name
		}
		if owner, seen := first[id]; seen {
			slog.WarnContext(ctx,
				"two RAGFlow knowledge bases share a dataset id; attribution follows the score order and "+
					"they are searched separately only when their threshold or Top K differ",
				"dataset_id", id,
				"first", owner.name, "first_score_threshold", owner.threshold, "first_top_k", owner.topK,
				"duplicate", name, "score_threshold", kb.ScoreThreshold, "top_k", kb.TopK)
			continue
		}
		first[id] = claim{name: name, threshold: kb.ScoreThreshold, topK: kb.TopK}
	}
}

// warnMCPConflicts reports MCP servers that two agents declare with different
// authentication or resource-mapping configuration.
//
// A run that selects both agents resolves the name once and keeps the first
// record, so the second agent's configuration is silently dropped. The store is
// the source of truth, and which of two conflicting records wins is not
// something this service should decide — so it says so and leaves the fix to
// whoever maintains the records.
func warnMCPConflicts(ctx context.Context, resources store.AIStore) {
	first := make(map[string]*model.MCPM)
	_, agents, err := resources.Agents().List(ctx, where.L(-1))
	if err != nil {
		slog.ErrorContext(ctx, "could not list agents for the MCP conflict check", "error", err)
		return
	}
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		for _, name := range agent.EffectiveMCPs() {
			mcp, err := resources.MCPs().Get(ctx, model.EntryFilter(name))
			if err != nil || mcp == nil {
				continue
			}
			existing, seen := first[name]
			if !seen {
				first[name] = mcp
				continue
			}
			if existing.Headers == mcp.Headers && existing.ResourceMappings == mcp.ResourceMappings {
				continue
			}
			slog.WarnContext(ctx,
				"two agents declare the same MCP server with different configuration; the first-seen record wins",
				"mcp", name, "agent", agent.Name,
				"headers_match", existing.Headers == mcp.Headers,
				"resource_mappings_match", existing.ResourceMappings == mcp.ResourceMappings)
		}
	}
}

// warnContextBudget reports a prompt budget that the configured model cannot
// hold. The field's own documentation says to keep it below about two thirds of
// the model's window; nothing else checks that, and the symptom of getting it
// wrong is a request the backend rejects partway through a conversation.
func warnContextBudget(cfg *Config) {
	if cfg.AgentOptions.MaxContextTokens <= 0 {
		return
	}
	window := chatmodel.ContextWindow(agentChatModel(cfg))
	if safe := window * 2 / 3; cfg.AgentOptions.MaxContextTokens > safe {
		slog.Warn("agent.max-context-tokens is above two thirds of the model's context window; "+
			"runs may reach the backend with more context than it can hold",
			"max_context_tokens", cfg.AgentOptions.MaxContextTokens,
			"model", agentChatModel(cfg), "context_window", window, "recommended_at_most", safe)
	}
}

// buildVisionMiddleware builds the image-extraction middleware, or nil when it
// is not applicable.
//
// It applies only when the primary model cannot see images and a vision model
// is configured: running extraction for a model that accepts images would
// replace every one of them with a lossy description for nothing.
func buildVisionMiddleware(cfg *Config, resources store.AIStore) agentbuilder.VisionMiddleware {
	opts := cfg.AgentOptions
	primary := agentChatModel(cfg)

	// A vision model is optional. When it is configured it describes images
	// for a primary model that cannot read them; when it is not, the middleware
	// is still useful as a guard that drops images rather than sending them to
	// a model that will reject the request.
	var visionModel einomodel.BaseChatModel
	if opts.VisionModel.Enabled() {
		visionCfg := auxModelConfig(cfg, opts.VisionModel)
		model, err := chatmodel.NewChatModel(context.Background(), visionCfg)
		if err != nil || model == nil {
			slog.Warn("vision model is configured but could not be built; images will be dropped",
				"error", err)
		} else {
			visionModel = model
		}
	}

	// Which model needs the extraction is the run's *effective* model, not the
	// deployment default: a user-configured endpoint may be text-only while the
	// platform model sees images, and a cached runner serves both.
	needs := func(ctx context.Context) bool {
		name := agentapi.ModelNameFromContext(ctx)
		if name == "" {
			name = primary
		}
		return !modelAcceptsImages(name, resources)
	}
	return agentbuilder.NewVisionMiddlewareWithPredicate(visionModel, needs)
}

// modelAcceptsImages reports whether the named model can be given an image.
//
// # Declared beats inferred
//
// Until a model row could declare its capabilities, this answer came from the
// model's name alone: everything outside the DeepSeek family was assumed to
// have vision. That is a statement about a vendor rather than about the
// endpoint, and it is wrong in both directions — a self-hosted DeepSeek with a
// vision adapter was denied an image it could have read, and a text-only model
// served under an OpenAI name was handed one it could not.
//
// A row that declares nothing still gets the old inference, so a deployment
// that never fills the field in behaves exactly as it did.
func modelAcceptsImages(modelName string, resources store.AIStore) bool {
	var provider string
	var declared []string
	if row, err := resources.Models().Get(context.Background(), where.F("name", modelName)); err == nil && row != nil {
		provider, declared = row.Provider, row.Capabilities
		// The row's name is a logical endpoint name a deployment chose; the
		// upstream model name is what says whether images are accepted. A row
		// named "deepseek-secured" pointing at deepseek-v4-flash must be read as
		// the latter, or every declared-elsewhere vision model is denied images.
		modelName = cmp.Or(row.Model, modelName)
	}
	return chatmodel.Resolve(declared, provider, modelName).Has(chatmodel.CapVision)
}

// larkClientOrNil returns a Lark client built from configuration, or nil when
// the credentials are not set.
func larkClientOrNil(cfg *Config) *larkSDK.Client {
	if cfg.LarkOptions.AppID == "" || cfg.LarkOptions.AppSecret == "" {
		return nil
	}
	return larkSDK.NewClient(cfg.LarkOptions.AppID, cfg.LarkOptions.AppSecret)
}
