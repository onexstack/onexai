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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	customcommandbiz "github.com/onexstack/onexai/internal/agent/biz/v1/customcommand"
	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/pkg/a2atool"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/agenttool"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/customcmd"
	"github.com/onexstack/onexai/internal/agent/pkg/k8sgpt"
	"github.com/onexstack/onexai/internal/agent/pkg/larktool"
	"github.com/onexstack/onexai/internal/agent/pkg/llmhttp"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	"github.com/onexstack/onexai/internal/agent/pkg/modelguard"
	"github.com/onexstack/onexai/internal/agent/pkg/rag"
	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"

	"github.com/onexstack/onexai/internal/pkg/netguard"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/tools"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/controlplanestore"
	sqlstore "github.com/onexstack/onexai/internal/agent/store/sqlstore"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ProvideAgentEngine builds the configured agent execution engine, or nil when
// no LLM backend is configured.
//
// Tools are composed from two sources: the built-ins registered in pkg/tools,
// which every run gets, and the tools of the MCP servers the selected agents
// declare, resolved per run through the manager. MCP servers are dialed lazily
// on first use and cached, so this function does not touch the network and
// startup is not hostage to an unreachable server.
// newRunModelResolver builds the per-run model resolver: it turns an
// agent.model row into a chat model, caching one instance per row revision.
//
// The client is the SSRF-guarded one from netguard: the base URL is written by
// a user and fetched by this process. Only OpenAI-compatible clients are built
// here — the native vendor adapters do not record usage, which would make the
// ledger, the quota and the BYOK accounting silently wrong.
func newRunModelResolver(cfg *Config, resources store.AIStore) func(context.Context, string) (einomodel.ToolCallingChatModel, error) {
	var mu sync.Mutex
	cache := make(map[string]cachedRunModel)

	client := newUserModelHTTPClient(cfg)

	return func(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error) {
		row, err := resources.Models().Get(ctx, model.EntryFilter(name))
		if err := storeerr.GetError(row, err, "model endpoint %q not found", name); err != nil {
			return nil, err
		}
		if !row.Enabled() {
			return nil, errno.ErrInvalidArgument.WithMessage("model endpoint %q is disabled", name)
		}
		if row.Model == "" {
			return nil, errno.ErrNotConfigured.WithMessage("model endpoint %q names no upstream model", name)
		}

		mu.Lock()
		if c, ok := cache[name]; ok && c.updatedAt == row.UpdatedAt && c.model != nil {
			mu.Unlock()
			return c.model, nil
		}
		mu.Unlock()

		key, err := model.OpenAPIKey(row.APIKey, cfg.ModelAPIKeyEncryptionKey)
		if err != nil {
			return nil, errno.ErrInternal.WithMessage("the api key of model endpoint %q could not be opened", name)
		}
		baseURL := row.BaseURL
		if baseURL == "" {
			baseURL = chatmodel.DefaultBaseURL(row.Provider)
		}
		if baseURL == "" {
			return nil, errno.ErrNotConfigured.WithMessage("model endpoint %q has no base URL", name)
		}

		m := chatmodel.NewOpenAIChatModel(baseURL, key, row.Model)
		// No deployment fallback model: that name belongs to the platform's
		// endpoint, not to the user's. Retry the same model instead.
		m = m.WithHTTPClient(client).
			WithMaxTokens(cfg.LLMMaxTokens).
			WithRetry("", agentMaxRetries(cfg), agentRetryBackoff(cfg))

		mu.Lock()
		cache[name] = cachedRunModel{updatedAt: row.UpdatedAt, model: m}
		mu.Unlock()
		return m, nil
	}
}

// cachedRunModel is one resolver cache entry, keyed by the row's revision so a
// saved change is picked up without a restart.
type cachedRunModel struct {
	updatedAt int64
	model     einomodel.ToolCallingChatModel
}

// buildFailover returns the agent-level model fallback policy, or nil when the
// deployment names no fallback model.
//
// The policy is deliberately narrow: it covers the deployment's own model only.
// A run on a user-configured endpoint keeps that endpoint, because the
// deployment's fallback id belongs to the platform's base URL and credentials —
// sending it to the user's URL would fail, and sending it with the platform's
// key would bill the wrong account.
func buildFailover(cfg *Config) *agentbuilder.Failover {
	fallbackID := agentFallbackModel(cfg)
	if fallbackID == "" {
		return nil
	}

	var (
		once     sync.Once
		fallback einomodel.BaseChatModel
		buildErr error
	)
	deploymentModel := func() (einomodel.BaseChatModel, error) {
		once.Do(func() {
			m := chatmodel.NewOpenAIChatModel(cfg.LLMBaseURL, cfg.LLMAPIKey, fallbackID)
			if m == nil {
				buildErr = errno.ErrNotConfigured.WithMessage(
					"fallback model %q is not usable: no base URL configured", fallbackID)
				return
			}
			fallback = m.WithMaxTokens(cfg.LLMMaxTokens).
				WithRetry("", agentMaxRetries(cfg), agentRetryBackoff(cfg))
		})
		return fallback, buildErr
	}

	return &agentbuilder.Failover{
		Model: func(ctx context.Context) (einomodel.BaseChatModel, error) {
			if name := agentapi.ModelNameFromContext(ctx); name != "" {
				return nil, errno.ErrNotConfigured.WithMessage("model endpoint %q has no configured fallback", name)
			}
			return deploymentModel()
		},
		ShouldFailover: func(ctx context.Context, _ *schema.Message, err error) bool {
			if agentapi.ModelNameFromContext(ctx) != "" {
				return false
			}
			// A wrong key or a malformed request fails the same way on every
			// model, so those stop here rather than burning the fallback too.
			var httpErr *llmhttp.HTTPError
			if errors.As(err, &httpErr) {
				return httpErr.Retryable()
			}
			return true
		},
	}
}

// newUserModelHTTPClient builds the SSRF-guarded client used for endpoints a
// user configured. The timeout is the run's scale, not an image download's.
func newUserModelHTTPClient(cfg *Config) *http.Client {
	return netguard.NewFetcher(netguard.Options{
		AllowPrivateHosts: cfg.LLMAllowPrivateEndpoints,
		Timeout:           5 * time.Minute,
	}).Client()
}

// ProvideToolRegistry builds the process's built-in tool set.
//
// It is a provider rather than a local inside ProvideAgentEngine because the MCP
// manager needs the registry's names too: an MCP tool must not be able to shadow
// current_time or ask_user_question, so the list of reserved names is the same
// list in both places.
func ProvideToolRegistry(resources store.AIStore) *tools.Registry {
	return tools.NewDefaultRegistry(resources)
}

// ProvideMCPManager builds the process's one MCP connection pool.
//
// One pool, not one per consumer: the manager caches a dialed connection set per
// (servers, agents) pair, and a second manager would dial — and keep open — a
// duplicate set behind the first one's back. The health reporter probes through
// this same manager, which is also what makes its findings about the servers a
// run would actually reach.
func ProvideMCPManager(
	ctx context.Context, cfg *Config, resources store.AIStore, registry *tools.Registry,
	client *declclient.Client,
) *mcpclient.Manager {
	manager := mcpclient.NewManager(ctx, resources, mcpclient.Options{
		Enabled:          cfg.AgentOptions.MCPEnabled,
		Reserved:         registry.Names(),
		DiscoveryTimeout: cfg.AgentOptions.MCPDiscoveryTimeout,
		TTL:              cfg.AgentOptions.MCPCacheTTL,
	})
	// A control-plane write lands in one replica; the informers are what tell
	// every replica that an agent's tool set may have changed.
	registerToolCacheInvalidators(client, manager)
	return manager
}

// ProvideCustomCommandBiz builds the user-defined slash command executor.
//
// It shares the MCP manager with the agent engine: a tool command and a model
// tool call must reach the same server through the same pooled connection.
func ProvideCustomCommandBiz(
	cfg *Config, resources store.AIStore, mcp *mcpclient.Manager, db *gorm.DB,
) customcommandbiz.CustomCommandBiz {
	opts := customoptions.CustomCommandOptions{}
	var requireApproval []string
	if cfg.AgentOptions != nil {
		opts = cfg.AgentOptions.CustomCommand
		requireApproval = cfg.AgentOptions.RequireApproval
	}
	return customcommandbiz.New(customcommandbiz.Deps{
		Resources: resources,
		MCP:       mcp,
		HTTP: customcmd.NewHTTPExecutor(customcmd.HTTPConfig{
			AllowedHosts:      opts.AllowedHosts,
			AllowPlainHTTP:    opts.AllowPlainHTTP,
			AllowPrivateHosts: opts.AllowPrivateHosts,
			MaxTimeout:        opts.MaxTimeout,
			MaxResponseBytes:  opts.MaxResponseBytes,
			MaxRedirects:      opts.MaxRedirects,
		}),
		RequireApproval: requireApproval,
		Audit:           customcommandbiz.NewAuditStore(db),
		Approvals:       customcommandbiz.NewApprovalStore(db),
	})
}
func ProvideAgentEngine(
	ctx context.Context, cfg *Config, resources store.AIStore, checkpoints checkpoint.Store,
	coordinator *authz.Coordinator, agentWebhookAuthz *agentauthz.Resolver,
	k8sgptRouter *k8sgpt.Router, skills *skill.Manager,
	drawer agentbuilder.ImageGenerator, settings *settingstore.Store,
	registry *tools.Registry, mcpManager *mcpclient.Manager, knowledgeBases *kbToolSource,
	agentProvider *capability.AgentProvider, memoryTools []tool.BaseTool,
) agentapi.Engine {
	// One chat model instance serves the engine, the agent builder and the
	// tools that call a model themselves, so the retry policy is attached
	// exactly once. It is built first because the tool set below depends on it.
	chatModel := chatmodel.NewOpenAIChatModel(cfg.LLMBaseURL, cfg.LLMAPIKey, agentChatModel(cfg))
	if chatModel != nil {
		chatModel = chatModel.WithRetry(agentFallbackModel(cfg), agentMaxRetries(cfg), agentRetryBackoff(cfg)).
			WithMaxTokens(cfg.LLMMaxTokens)
	}
	// The graph's model retries the same model only: degrading to another one is
	// the agent's job (see buildFailover), where a single failure moves the whole
	// rest of the run. Models that are not in the graph — the summarizer, the
	// reviewers, the document rag tool — keep the client-level fallback.
	graphModel := einomodel.ToolCallingChatModel(chatModel)
	if chatModel != nil {
		graphModel = chatModel.WithRetry("", agentMaxRetries(cfg), agentRetryBackoff(cfg))
	}

	AgentConfigWarnings(ctx, cfg, resources)

	builtins := registry.Builtins()
	// The cluster fleet's tools are built once here rather than per run: the
	// list comes from a server handshake, and the same tools are offered by
	// every cluster, so resolving them per run would only repeat the dial.
	if k8sgptRouter != nil {
		fleetTools, err := k8sgptRouter.BuildTools(ctx)
		if err != nil {
			slog.WarnContext(ctx, "k8sgpt: could not build the cluster tools", "error", err)
		} else {
			builtins = append(builtins, fleetTools...)
		}
	}
	if cfg.AgentOptions.RagDocument {
		if docTool := rag.NewTool(rag.Config{Model: chatModel, TopK: cfg.AgentOptions.RagTopK}); docTool != nil {
			builtins = append(builtins, docTool)
		} else {
			slog.WarnContext(ctx, "agent.rag-document is enabled but no chat model is configured; the tool is not offered")
		}
	}
	if cfg.AgentOptions.LarkMessage {
		if msgTool := larktool.NewSendMessage(larkClientOrNil(cfg)); msgTool != nil {
			builtins = append(builtins, msgTool)
		} else {
			slog.WarnContext(ctx, "agent.lark-message is enabled but no Lark client is configured; the tool is not offered")
		}
	}

	toolsFor := func(mcps, agents []string) []tool.BaseTool {
		mcpTools := mcpManager.Tools(mcps, agents)
		if len(mcpTools) == 0 {
			return builtins
		}
		return append(append(make([]tool.BaseTool, 0, len(builtins)+len(mcpTools)), builtins...), mcpTools...)
	}

	// The delegation tool is built over the holder rather than over the agent
	// service, which does not exist yet and is built from this engine. The tool
	// is added to a run's tool set only when its agent names delegates.
	delegationTool := agenttool.New(agentProvider)
	// The A2A dispatcher shares the same store and credential reader as the
	// console: one call path, one audit row, one set of limits.
	a2aTools := a2atool.New(resources.Agents(), a2ainvoke.New(resources.Agents(), resources.A2AInvocations(), resources.Agents()))

	ecfg := agentbiz.EngineConfig{
		BaseURL:        cfg.LLMBaseURL,
		APIKey:         cfg.LLMAPIKey,
		Model:          cfg.LLMModel,
		Tools:          toolsFor,
		DelegationTool: delegationTool,
		CallTools:      a2aTools.Tools,
		MemoryTools:    memoryTools,
		// The reserved names come from the registry the built-ins were built
		// from, so the tool whose contract is to consult a human is never gated
		// by a policy about consulting a human.
		Reserved:    registry.Names(),
		Checkpoints: checkpoints,
		// Authorization is per-run: the gate is built inside the engine from the
		// run's selected MCP servers, so it needs the coordinator and two
		// lookups rather than a ready-made gate.
		Authz:               coordinator,
		AgentAuthz:          agentWebhookAuthz,
		ToolOrigins:         mcpManager.ToolOrigins,
		MCPResourceMappings: resourceMappingsFor(ctx, resources),
		// Nil unless the deployment names a ledger to report to. It matters
		// exactly for the runs OneX never sees — the Lark conversations, which
		// arrive here directly — so a deployment without that path leaves it
		// unset and nothing changes for any other surface.
		UsageReporter: usageReporter(cfg),
	}
	if cfg.AgentOptions.ChatModel != "" {
		ecfg.Model = cfg.AgentOptions.ChatModel
	}
	ecfg.FallbackModel = cfg.AgentOptions.FallbackModel
	ecfg.MaxRetries = cfg.AgentOptions.MaxRetries
	ecfg.Backoff = cfg.AgentOptions.RetryBackoff
	ecfg.ToolTimeout = cfg.AgentOptions.ToolTimeout
	ecfg.MaxContextTokens = cfg.AgentOptions.MaxContextTokens
	ecfg.MaxToolOutputChars = cfg.AgentOptions.MaxToolOutputChars
	ecfg.ModelGuard = modelguard.New(modelguard.Config{
		QPS:              cfg.AgentOptions.ModelGuard.QPS,
		Burst:            cfg.AgentOptions.ModelGuard.Burst,
		FailureThreshold: cfg.AgentOptions.ModelGuard.FailureThreshold,
		Cooldown:         cfg.AgentOptions.ModelGuard.Cooldown,
	})

	ecfg.ChatModel = chatModel

	// The router is the graph's model and the resolver is what it reads per
	// run. Both are process-wide: the resolver caches one client per endpoint
	// revision, and the router is stateless beyond that.
	modelResolver := newRunModelResolver(cfg, resources)
	modelRouter := agentbiz.NewRunModelRouter(graphModel, modelResolver)
	ecfg.ModelRouter = modelRouter
	ecfg.ResolveChatModel = modelResolver

	ecfg.ResolveSkills = resolveSkillRecords(resources)
	ecfg.ResolveKnowledgeBases = resolveKnowledgeBaseRecords(resources)
	ecfg.Builder = buildAgentBuilder(cfg, resources, modelRouter, skills, drawer, knowledgeBases, settings)

	// The resolver and the runner cache are one decision: a cached graph is
	// rebuilt from the agent names alone, which needs the resolver, and the
	// resolver without a cache would only duplicate the per-run path.
	ecfg.ResolveRun = agentbiz.NewResolver(resources, cfg.AgentOptions.RequireApproval).Resolve
	ecfg.RunnerTTL = cfg.AgentOptions.RunnerCacheTTL
	ecfg.PinTTL = cfg.AgentOptions.CheckpointTTL
	if ecfg.RunnerTTL > 0 {
		slog.Info("agent runner caching enabled",
			"ttl", ecfg.RunnerTTL, "pin_ttl", ecfg.PinTTL,
			"note", "a run paused for approval holds its agent set's runner unrebuilt until its pin expires")
	}

	return agentbiz.NewEngine(ecfg)
}

// agentFallbackModel, agentMaxRetries and agentRetryBackoff read the retry
// policy from configuration, so the model can be built before the engine
// config that also carries it.
func agentFallbackModel(cfg *Config) string {
	return cfg.AgentOptions.FallbackModel
}
func agentMaxRetries(cfg *Config) int {
	return cfg.AgentOptions.MaxRetries
}
func agentRetryBackoff(cfg *Config) time.Duration {
	return cfg.AgentOptions.RetryBackoff
}

// configuredMCPs lists the enabled MCP servers the resource store declares.
// A nil store declares none, which is how a deployment with resources disabled
// starts with no MCP servers.
func configuredMCPs(ctx context.Context, resources store.AIStore) []string {
	if resources == nil {
		return nil
	}
	var names []string
	_, mcps, err := resources.MCPs().List(ctx, where.L(-1))
	if err != nil {
		return nil
	}
	for _, m := range mcps {
		if m.Enabled() {
			names = append(names, m.Name)
		}
	}
	return names
}

// ProvideK8SGPTRouter connects the k8sgpt cluster fleet, or returns nil when it
// is not configured.
//
// The endpoints come from the database rather than from configuration: they are
// a fleet that changes as clusters are added, and a table is what makes that a
// data change. Connecting happens here, once, because it is a network dial per
// cluster and doing it lazily would put the first caller's latency on a
// handshake with twenty servers.
func ProvideK8SGPTRouter(ctx context.Context, cfg *Config, db *gorm.DB) *k8sgpt.Router {
	if !cfg.AgentOptions.K8SGPT.IsEnabled() || db == nil {
		return nil
	}
	opts := cfg.AgentOptions.K8SGPT

	token := ""
	if opts.JWTKey != "" {
		signed, err := k8sgpt.GenerateJWTToken(opts.JWTKey)
		if err != nil {
			slog.Error("k8sgpt: could not sign the fleet token; the fleet stays off", "error", err)
			return nil
		}
		token = signed
	}

	_, endpoints, err := sqlstore.NewStore(db).K8SGPTEndpoints().List(ctx, where.L(-1))
	if err != nil {
		slog.Error("k8sgpt: could not load cluster endpoints; the fleet stays off", "error", err)
		return nil
	}
	if len(endpoints) == 0 {
		slog.Warn("k8sgpt: enabled but no cluster endpoints are registered")
		return nil
	}

	router := k8sgpt.NewRouter(endpoints, token)
	clusters := router.Connect(ctx)
	if len(clusters) == 0 {
		slog.Error("k8sgpt: no cluster accepted a connection; the fleet stays off",
			"endpoints", len(endpoints))
		return nil
	}
	slog.Info("k8sgpt fleet connected", "clusters", clusters, "of", len(endpoints))
	return router
}

// agentChatModel returns the model name the agent should use: the agent-level
// override when set, else the shared llm.model. The engine resolves the same
// name the same way, so the summarizer and the engine always agree.
func agentChatModel(cfg *Config) string {
	if cfg.AgentOptions.ChatModel != "" {
		return cfg.AgentOptions.ChatModel
	}
	return cfg.LLMModel
}

// auxModelConfig inherits the primary model's provider, endpoint and key onto
// an auxiliary model's own settings, so a deployment with one vendor names only
// the auxiliary model.
func auxModelConfig(cfg *Config, model *customoptions.ChatModelConfig) customoptions.ChatModelConfig {
	return chatmodel.Inherit(*model, customoptions.ChatModelConfig{
		Provider: chatmodel.InferProvider(agentChatModel(cfg)),
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.LLMAPIKey,
	})
}

// primaryModelConfig is the run's primary model as a chat-model config.
func primaryModelConfig(cfg *Config) customoptions.ChatModelConfig {
	name := agentChatModel(cfg)
	return customoptions.ChatModelConfig{
		Provider: chatmodel.InferProvider(name),
		Model:    name,
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.LLMAPIKey,
	}
}

// ProvideAIStore builds the AI-resource store from configuration. Returns a
// no-op empty store when no backend is configured, so consumers never receive
// a nil store.
//
// The migrated resources come from the declarative control plane; the tables
// that stayed behind are served by the fallback SQL store. The database is the
// only SQL backend: the Feishu Bitable one this used to select is gone (see
// options.StoreOptions.Source), which is also why there is no switch here any
// more.
func ProvideAIStore(
	ctx context.Context, cfg *Config, db *gorm.DB, client *declclient.Client, fallback store.RemainingStore,
) (store.AIStore, error) {
	so := cfg.StoreOptions

	// A deployment with no control plane can still be a pure SQL deployment for
	// the legacy tables. A deployment with a control plane must build the
	// composite even when SQL is disabled: otherwise the declarative resources
	// (and now the AK/SK secrets) would silently resolve to the empty store.
	if client == nil {
		if so == nil || so.Source == "" || db == nil {
			return store.NewEmptyStore(), nil
		}
		return nil, fmt.Errorf("agent: the resource store needs the declarative control plane; set controlplane.kubeconfig")
	}
	if db == nil {
		// The SQL-only tables (audit, asset, access key) answer empty/refusing
		// instead of dereferencing a nil DB; the declarative resources are fully
		// available.
		fallback = store.NewEmptyRemainingStore()
	}

	namespace := ""
	if cfg.ControlPlaneOptions != nil {
		namespace = cfg.ControlPlaneOptions.Namespace
	}
	slog.InfoContext(ctx,
		"store: agents, prompts, skills, knowledge bases, models, MCP servers, webhooks and AK/SK secrets come from the control plane",
		"namespace", namespace)
	if so != nil && so.UsesMirror() {
		slog.InfoContext(ctx, "store: reading from the PostgreSQL projection",
			"kinds", so.MirroredKinds(), "schema", so.Mirror.Schema, "maxLag", so.Mirror.MaxLag)
	}
	return controlplanestore.NewAIStore(client, namespace, fallback,
		&controlplanestore.MirrorConfig{DB: db, Options: so, Namespace: namespace})
}
