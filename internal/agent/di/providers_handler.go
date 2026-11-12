// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package di

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/cloudwego/eino/components/tool"

	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	akskbiz "github.com/onexstack/onexai/internal/agent/biz/v1/aksk"
	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	customcommandbiz "github.com/onexstack/onexai/internal/agent/biz/v1/customcommand"
	"github.com/onexstack/onexai/internal/agent/biz/v1/knowledgebase"
	modelbiz "github.com/onexstack/onexai/internal/agent/biz/v1/model"
	"github.com/onexstack/onexai/internal/agent/biz/v1/sessiongc"
	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/embedding"
	"github.com/onexstack/onexai/internal/agent/pkg/inputaudit"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/memory"
	"github.com/onexstack/onexai/internal/agent/pkg/memorytool"
	"github.com/onexstack/onexai/internal/agent/pkg/ratelimit"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	"github.com/onexstack/onexai/internal/agent/pkg/sandbox"
	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/pkg/taskstore"
	"github.com/onexstack/onexai/internal/agent/pkg/usagereport"
	"github.com/onexstack/onexai/internal/agent/pkg/webhook"
	"github.com/onexstack/onexai/internal/agent/store/sandboxstore"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/memoryaudit"
	"github.com/onexstack/onexai/internal/agent/store/memoryentry"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ProvideMemoryService builds long-term memory, or nil when no database is
// configured.
func ProvideMemoryService(db *gorm.DB, resources store.AIStore, cfg *Config) *memory.Service {
	if db == nil || resources == nil {
		return nil
	}
	encryptionKey := ""
	if cfg != nil {
		encryptionKey = cfg.ModelAPIKeyEncryptionKey
	}
	return &memory.Service{
		Store:          memoryentry.New(db),
		Memories:       resources.Memories(),
		Audit:          memoryaudit.New(db),
		Models:         modelEmbeddingResolver{resources: resources, encryptionKey: encryptionKey},
		Embedder:       embedding.New(),
		DefaultTTLDays: 15,
		MaxRows:        2000,
	}
}

// modelEmbeddingResolver turns a Model name into the endpoint used for
// embeddings. The credential stays in this process; the runtime never sees it.
type modelEmbeddingResolver struct {
	resources     store.AIStore
	encryptionKey string
}

func (r modelEmbeddingResolver) EmbeddingEndpoint(
	ctx context.Context, name string,
) (baseURL, apiKey, modelName string, err error) {
	if name == "" {
		return "", "", "", errno.ErrNotConfigured.WithMessage("agent memory has no embedding model configured")
	}
	m, err := r.resources.Models().Get(ctx, model.EntryFilter(name))
	if err := storeerr.GetError(m, err, "embedding model %q not found", name); err != nil {
		return "", "", "", err
	}
	// Storage holds the sealed form; sending it upstream would be a 401. A
	// failed open is a configuration error, not an empty credential, so it is
	// returned rather than swallowed.
	key, err := model.OpenAPIKey(m.APIKey, r.encryptionKey)
	if err != nil {
		return "", "", "", errno.ErrInternal.WithMessage("the api key of embedding model %q could not be opened", name)
	}
	return m.BaseURL, key, m.Model, nil
}

// ProvideCapabilityHandler builds the runtime-only MCP surface. The skill
// provider is the first capability; knowledge bases, memory, commands and
// sandboxes register here as they land.
func ProvideCapabilityHandler(
	resources store.AIStore, skills *skill.Manager,
	knowledgeBases *kbToolSource, commands customcommandbiz.CustomCommandBiz, memoryProvider *capability.MemoryProvider,
	sandboxes *sandbox.Service, issuer *capability.Issuer, agentProvider *capability.AgentProvider,
	runs runstore.Store,
) *capability.Handler {
	if resources == nil {
		return nil
	}
	registry := capability.NewRegistry()
	registered := false
	if skills != nil {
		registry.Register(&capability.SkillsProvider{
			Resources: resources,
			Source:    capability.ManagerSource{Manager: skills},
		})
		registered = true
	}
	if knowledgeBases != nil {
		// Configuration is checked when a KB is actually resolved; the source
		// itself is registered whenever the subsystem exists.
		registry.Register(&capability.KnowledgeBaseProvider{
			Resources: resources,
			Source:    kbCapabilitySource{source: knowledgeBases},
		})
		registered = true
	}
	if commands != nil {
		registry.Register(&capability.CommandProvider{Resources: resources, Commands: commands})
		registered = true
	}
	if memoryProvider != nil {
		registry.Register(memoryProvider)
		registered = true
	}
	if sandboxes != nil {
		registry.Register(&capability.SandboxProvider{Service: sandboxes})
		registered = true
	}
	// Delegation is registered even when nothing else is: it is the capability a
	// deployment with no skills or knowledge bases still wants.
	if agentProvider != nil {
		registry.Register(agentProvider)
		registered = true
	}
	// The session tools act on conversations other than the one in progress, so
	// they are registered only when they have somewhere to read and someone to
	// run the continuation. The token's session scope decides who may see them.
	if agentProvider != nil && runs != nil {
		registry.Register(&capability.SessionProvider{
			Sessions:   runs,
			Invoker:    agentProvider.Invoker,
			Delegation: agentProvider,
		})
		registered = true
	}
	if !registered {
		return nil
	}
	return &capability.Handler{Service: capability.NewService(registry, issuer, runs)}
}

// kbCapabilitySource calls the same retrieval tool the in-process agent uses,
// so grading, query rewriting, watermarking and audit apply to a remote
// runtime's searches without a second implementation.
type kbCapabilitySource struct {
	source *kbToolSource
}

func (s kbCapabilitySource) Search(ctx context.Context, kbs []*model.KnowledgeBaseM, query string) (string, error) {
	tools := s.source.Tools(ctx, kbs)
	if len(tools) == 0 {
		return "", errno.ErrNotConfigured.WithMessage("no knowledge-base platform is configured")
	}
	invokable, ok := tools[0].(tool.InvokableTool)
	if !ok {
		return "", errno.ErrNotConfigured.WithMessage("the retrieval tool is not invokable")
	}
	args, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return "", errno.ErrInternal.WithMessage("the retrieval arguments could not be encoded")
	}
	return invokable.InvokableRun(ctx, string(args))
}

// ProvideRunStore is the durable session/run/event store.
func ProvideRunStore(db *gorm.DB) runstore.Store {
	if db == nil {
		return nil
	}
	return runstore.New(db)
}

// ProvideMemoryProvider is the memory provider shared by the MCP gateway and
// the in-process tools, so the two transports cannot diverge.
func ProvideMemoryProvider(resources store.AIStore, mem *memory.Service) *capability.MemoryProvider {
	if resources == nil || mem == nil {
		return nil
	}
	return &capability.MemoryProvider{Resources: resources, Service: mem}
}

// ProvideMemoryTools exposes the same provider as eino tools.
func ProvideMemoryTools(provider *capability.MemoryProvider) []tool.BaseTool {
	if provider == nil {
		return nil
	}
	return memorytool.New(provider)
}

// ProvideTokenRefresh renews a run's capability token while the run is alive.
//
// The lifetime it grants is the same one the run path issues with, so a run that
// keeps refreshing never holds a token for longer than it would have had
// anyway — renewal extends the window, not the grant.
func ProvideTokenRefresh(issuer *capability.Issuer, runs runstore.Store) *capability.TokenRefreshHandler {
	if issuer == nil || runs == nil {
		return nil
	}
	return &capability.TokenRefreshHandler{Issuer: issuer, Runs: runs}
}

// ProvideTaskStoreHandler is the durable A2A task contract.
func ProvideTaskStoreHandler(runs runstore.Store, issuer *capability.Issuer) *taskstore.HTTPHandler {
	if runs == nil {
		return nil
	}
	return &taskstore.HTTPHandler{Service: &taskstore.Service{Runs: runs}, Issuer: issuer}
}

// ProvideSandboxService builds the container sandbox, or nil when disabled.
func ProvideSandboxService(cfg *Config, db *gorm.DB, resources store.AIStore) *sandbox.Service {
	if db == nil || !cfg.AgentOptions.Sandbox.Enabled {
		return nil
	}
	opts := cfg.AgentOptions.Sandbox
	config := sandbox.Config{
		Images:       opts.Images,
		DefaultImage: opts.DefaultImage,
		DefaultTTL:   opts.DefaultTTL,
		MaxTTL:       opts.MaxTTL,
		CPUs:         opts.CPUs,
		Memory:       opts.Memory,
		PidsLimit:    opts.PidsLimit,
		Network:      opts.Network,
		EgressProxy:  opts.EgressProxy,
		NoProxy:      opts.NoProxy,
		Command:      opts.Command,
	}
	// Refused rather than corrected: a deployment that asked for host
	// networking and silently got "none" would believe a policy is in force.
	if err := config.Validate(); err != nil {
		slog.Error("sandbox: the configured network policy is refused; the sandbox is disabled", "error", err)
		return nil
	}
	return &sandbox.Service{
		Store:     sandboxstore.New(db),
		Config:    config,
		Templates: sandboxTemplates{resources: resources},
	}
}

// sandboxTemplates resolves a declared SandboxTemplate for the sandbox service.
type sandboxTemplates struct {
	resources store.AIStore
}

var _ sandbox.TemplateSource = sandboxTemplates{}

// Template implements sandbox.TemplateSource. A missing template is nil, which
// the service reads as "fall back to the deployment's image map".
func (s sandboxTemplates) Template(ctx context.Context, name string) (*sandbox.Template, error) {
	if s.resources == nil || name == "" {
		return nil, nil
	}
	row, err := s.resources.SandboxTemplates().Get(ctx, where.F("name", name))
	if err != nil || row == nil || row.Status == model.StatusDisabled {
		return nil, nil
	}
	return &sandbox.Template{
		Image:   row.Image,
		Command: []string(row.Command),
		Env:     []string(row.Env),
		TTL:     time.Duration(row.TTLSeconds) * time.Second,
	}, nil
}

// ProvideSessionGC closes sessions idle past the configured TTL.
func ProvideSessionGC(cfg *Config, runs runstore.Store, sessions mem.SessionStore) *sessiongc.Collector {
	if runs == nil || cfg.AgentOptions.SessionIdleTTL <= 0 {
		return nil
	}
	return &sessiongc.Collector{
		Runs:     runs,
		Sessions: sessions,
		TTL:      cfg.AgentOptions.SessionIdleTTL,
		Interval: cfg.AgentOptions.SessionIdleSweep,
	}
}

// ProvideCapabilityIssuer builds the token issuer.
//
// It is built from configuration rather than from the capability handler so the
// two do not depend on each other: the run path needs an issuer to mint a run's
// token, and the handler needs the agent service to execute a delegation, which
// would be a cycle if the issuer came from the handler. One issuer serves both.
func ProvideCapabilityIssuer(cfg *Config) *capability.Issuer {
	if cfg == nil || cfg.AgentOptions == nil {
		return nil
	}
	return capability.NewIssuer([]byte(cfg.AgentOptions.Runtime.TokenKey))
}
func ProvideAgentBiz(
	ctx context.Context, cfg *Config, engine agentapi.Engine, resources store.AIStore,
	sessions mem.SessionStore, stops runstop.Registry, checkpoints checkpoint.Store,
	runtimes *runtime.Registry, runs runstore.Store, capabilityIssuer *capability.Issuer,
	mem *memory.Service, coordinator *authz.Coordinator, agentWebhookAuthz *agentauthz.Resolver,
	interactions *interactionlog.Writer,
	quota *ratelimit.Limiter, tokenQuota *ratelimit.TokenLimiter,
	dispatcher *webhook.Dispatcher, settings *settingstore.Store,
	images *agentedge.Inliner, assets asset.AssetBiz, delegation *DelegationHolder,
) agentapi.IAgentBiz {
	if engine == nil {
		return nil
	}
	maxConc := cfg.AgentOptions.MaxConcurrentPerConversation
	runTimeout := cfg.AgentOptions.RunTimeout
	maxContext := cfg.AgentOptions.MaxContextTokens
	preserveTokens := cfg.AgentOptions.ContextPreserveTokens
	defaultReasoning := cfg.AgentOptions.ShowReasoning
	requireApproval := cfg.AgentOptions.RequireApproval
	compatApproval := customoptions.CompatApprovalDeny
	if cfg.AgentOptions.CompatApprovalMode != "" {
		compatApproval = cfg.AgentOptions.CompatApprovalMode
	}

	// The compactor condenses a session that has outgrown the budget. It builds
	// its own chat model rather than sharing the engine's, so that adding it
	// does not change this provider's signature and force a wire regeneration.
	// The model is stateless, so the duplication is only one extra struct.
	// Same output ceiling as the engine's model: a summary is a completion like
	// any other, and leaving it uncapped would make the one call that only ever
	// runs on an oversized context the one call with no bound on its bill.
	summarize := agentbiz.NewSummarizer(chatmodel.NewOpenAIChatModel(
		cfg.LLMBaseURL, cfg.LLMAPIKey, agentChatModel(cfg)).WithMaxTokens(cfg.LLMMaxTokens))

	// Say what the budget resolved to. A misspelled or absent config key decodes
	// to zero rather than erroring, and zero means "no context management" — the
	// exact state that let a 1.4M-token turn reach the model. The startup log is
	// the only place that difference is visible before a request fails.
	slog.InfoContext(ctx, "agent context management configured",
		"max_context_tokens", maxContext,
		"context_preserve_tokens", preserveTokens,
		"summarizer_configured", summarize != nil,
	)

	biz := agentbiz.New(agentbiz.Deps{
		Engine:                   engine,
		Runtimes:                 runtimes,
		Runs:                     runs,
		CapabilityIssuer:         capabilityIssuer,
		EnableTracing:            cfg.AgentOptions.EnableTracing,
		Memory:                   mem,
		ApprovalTTL:              cfg.AgentOptions.CheckpointTTL,
		PortEnabled:              cfg.AgentOptions.Runtime.PortEnabled,
		Sessions:                 sessions,
		Stops:                    stops,
		Checkpoints:              checkpoints,
		RequireApproval:          requireApproval,
		CompatApprovalMode:       compatApproval,
		MaxConcurrentPerConv:     maxConc,
		RunTimeout:               runTimeout,
		Resources:                resources,
		ModelAPIKeyEncryptionKey: cfg.ModelAPIKeyEncryptionKey,
		// The same service the invoke API and the a2a_invoke tool use: a Chat
		// turn addressed to an external agent must produce the same calls, the
		// same credentials and the same invocation records as the debug panel.
		A2A:                   a2ainvoke.New(resources.Agents(), resources.A2AInvocations(), resources.Agents()),
		Settings:              settings,
		Root:                  ctx,
		MaxContextTokens:      maxContext,
		ContextPreserveTokens: preserveTokens,
		Summarize:             summarize,
		DefaultReasoning:      defaultReasoning,
		Authz:                 coordinator,
		AgentAuthz:            agentWebhookAuthz,
		Interactions:          interactions,
		Quota:                 quota,
		TokenQuota:            tokenQuota,
		Webhooks:              dispatcher,
		Images:                images,
		Assets:                assets,
	})
	// The engine already holds a delegation tool over this holder; installing
	// the service here is what makes that tool call this service rather than
	// nothing. Doing it after construction rather than in the constructor is
	// what keeps the dependency from being circular.
	delegation.Set(agentbiz.NewDelegator(biz))
	return biz
}

// ProvideScreener is the inbound message audit, or nil when it is off.
func ProvideScreener(cfg *Config) handler.Screener { return buildScreener(cfg) }

// ProvideOutputCaps is the per-surface output ceiling, or nil when the
// deployment declares no surfaces.
func ProvideOutputCaps(cfg *Config) *handler.OutputCaps { return outputCaps(cfg) }

// ProvideModelConfig carries the user-model settings the write/test surfaces
// need.
func ProvideModelConfig(cfg *Config) *modelbiz.Config {
	return &modelbiz.Config{
		EncryptionKey:         cfg.ModelAPIKeyEncryptionKey,
		AllowPrivateEndpoints: cfg.LLMAllowPrivateEndpoints,
		TestTimeout:           cfg.LLMTestTimeout,
	}
}

// ProvideKnowledgeBaseConfig builds the retrieval-test configuration.
//
// It is the same mapping the run-time retrievers use, through ragflowConfig:
// a console test that resolved a different threshold or rerank model would
// answer a question the agent would answer differently, which is exactly what
// the test is supposed to rule out.
func ProvideKnowledgeBaseConfig(cfg *Config) *knowledgebase.Config {
	return &knowledgebase.Config{RAGFlow: ragflowConfig(ptr.From(cfg.AgentOptions.RAGFlow))}
}

// ProvideAccessKeyConfig builds the personal credential configuration. The
// cipher secret is the deployment's JWT secret: it is already present wherever
// a console user can be authenticated, so reusing it avoids a second knob whose
// absence would silently disable the reveal path.
func ProvideAccessKeyConfig(cfg *Config) *akskbiz.Config {
	return &akskbiz.Config{CipherSecret: cfg.JWTSecret}
}

// buildScreener returns the inbound audit bound to configuration, or nil when
// it is disabled.
//
// The binding lives here rather than in the handler so the handler stays
// ignorant of how the auditor is configured — and so a deployment that does not
// enable it passes a nil function rather than a function that always allows.
func buildScreener(cfg *Config) handler.Screener {
	opts := cfg.AgentOptions
	if !opts.InputAudit {
		return nil
	}
	reviewer := reviewerModelConfig(cfg)
	if !reviewer.Enabled() {
		// Enabled with no auditor to run. Said once, at startup, because the
		// alternative is an operator believing their input is being screened.
		slog.Warn("agent.input-audit is enabled but agent.reviewer-model is not configured; input will not be screened")
		return nil
	}
	// The auditor is built once here rather than per message: building it
	// allocates an HTTP-backed client, which is not part of answering a turn.
	return inputaudit.NewScreener(reviewer)
}

// usageReporter builds the ledger reporter, or nil when no ledger is configured.
//
// It returns the interface rather than the client, and that is the whole reason
// this is a function: a nil *usagereport.Client stored in a Reporter interface is
// not a nil interface, so the engine's own nil check would pass and every run
// would POST to an empty URL — a failure that logs on every run and reports
// nothing, which is exactly what the configuration meant to avoid.
func usageReporter(cfg *Config) usagereport.Reporter {
	opts := cfg.UsageReportOptions
	if !opts.Enabled() {
		return nil
	}
	c := usagereport.New(opts.Endpoint, opts.Token, opts.Timeout)
	if c == nil {
		return nil
	}
	return c
}

// outputCaps builds the per-surface output ceilings, or nil when the deployment
// declares none.
//
// Nil rather than an empty struct because the two mean the same thing to the
// handler (nobody gets a surface ceiling) and one of them is checkable: a nil
// OutputCaps cannot be mistaken for a deployment that configured surfaces and
// forgot to fill them in.
func outputCaps(cfg *Config) *handler.OutputCaps {
	opts := cfg.AgentOptions
	if len(opts.MaxOutputTokens) == 0 {
		return nil
	}
	return &handler.OutputCaps{
		BySurface:   opts.MaxOutputTokens,
		SurfaceKeys: opts.SurfaceKeys,
	}
}
