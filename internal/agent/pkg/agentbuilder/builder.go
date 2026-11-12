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

// Package agentbuilder constructs an eino deep agent (and the runner that
// drives it) from a resolved set of skills, MCP servers, knowledge bases and
// call-site handlers.
//
// It is the single place in the service that knows how an agent graph is
// assembled, so every caller — the per-run engine today, a cached runner
// tomorrow — gets an identically built agent. It deliberately does not know
// where its inputs come from: resources arrive as the domain records the store
// already holds, the two optional subsystems that turn them into agent
// capabilities arrive as the narrow interfaces below, and handlers arrive from
// the caller. That is what keeps this package free of store, biz and provider
// dependencies, and therefore testable with fakes.
package agentbuilder

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	adkfilesystemmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/options"
)

// DefaultAgentKey is used as the runner name and skill-directory segment when
// a build resolves no agent names at all.
//
// It is exported because a caller that asks what an agent's skills are has to
// name the same directory a run would check them out under, and the two only
// agree if they agree on this.
const DefaultAgentKey = "onexai-assistant"

// Options is the deployment configuration a Builder needs. It is deliberately a
// plain struct rather than the whole AgentOptions: the parts of that struct the
// builder has no business knowing about stay out of its dependencies.
type Options struct {
	// Description is passed through to the agent for logs and traces.
	Description string
	// MaxIteration caps the reasoning loop. Zero falls back to the deep agent's
	// own default.
	MaxIteration int
	// ExecuteSequentially runs a tool batch one call at a time. It is forced on
	// whenever the run can pause for approval, because a parallel batch raises
	// several interrupts whose addresses make resume non-deterministic.
	ExecuteSequentially bool
	// DeepExtrasEnabled turns on the deep agent's built-in write_todos and
	// general-purpose sub-agent tools. Off keeps the tool set minimal.
	DeepExtrasEnabled bool
	// ModelRetryMax, when > 0, is handed to the agent as its own model retry
	// budget. The chat client already retries, so this stays off unless asked.
	ModelRetryMax int
	// SkillsDir is the root under which each agent set's skills are isolated.
	// Empty disables the skill middleware and the filesystem/shell tools.
	SkillsDir string
	// Prompts carries deployment-wide prompt fragments.
	Prompts options.PromptsConfig
	// SystemPrompt, when non-nil, supplies the deployment-wide system prompt at
	// build time. It is consulted only when Prompts.System is empty (a
	// deployment that pinned the prompt in its configuration did so on purpose),
	// and it lets the value come from something an operator can edit while the
	// service runs.
	SystemPrompt func() string
	// MaxToolOutputChars bounds one tool output. Where the run has a filesystem
	// backend the full text is offloaded and the model sees a truncated notice;
	// zero disables truncation.
	MaxToolOutputChars int
	// OffloadClearTokens is the context size at which the oldest tool results
	// start being replaced by placeholders (and offloaded). Zero disables it.
	OffloadClearTokens int64
	// OffloadTTL is how long an offloaded file may live. Files are grouped by
	// run and collected once they are this old; the figure should match the
	// checkpoint TTL, past which a paused run can no longer read them.
	// Zero disables collection.
	OffloadTTL time.Duration
	// Failover, when non-nil, lets the agent degrade to another model when the
	// run's own keeps failing. Nil keeps one model for the whole run.
	Failover *Failover
	// ToolSearch hides the MCP-published tools behind a single tool_search
	// entry until the model asks for them. It is off by default: filtering the
	// tool list between model calls invalidates the provider's prompt cache, so
	// it is only worth it when the tool set is large enough that the schemas
	// themselves dominate.
	ToolSearch ToolSearchOptions
	// AgentsMD injects the repository's AGENTS.md into each model call. Off by
	// default: the file is half-trusted input, so turning it on is a decision
	// about which skill sources this deployment trusts.
	AgentsMD AgentsMDOptions
	// MultiModalRead replaces the plain read_file tool with one that hands
	// images and PDFs to the model instead of their bytes as text. Off by
	// default: it only helps a vision model, and it sends file bytes through the
	// model call.
	MultiModalRead bool
}

// AgentsMDOptions configures the AGENTS.md injection.
type AgentsMDOptions struct {
	// Enabled installs the middleware. It also requires a filesystem backend,
	// which exists only when the deployment has a skills directory.
	Enabled bool
	// Files are the paths to read, in order, relative to the agent's sandbox
	// root. Empty means DefaultAgentsMDFile.
	Files []string
	// MaxBytes bounds all injected content together. Zero means no limit, which
	// is a way to push a run over its context budget: leave it set.
	MaxBytes int
	// PerFileMaxBytes bounds one file. Zero means no per-file limit.
	PerFileMaxBytes int
}

// DefaultAgentsMDFile is the file name read when AgentsMDOptions.Files is empty.
const DefaultAgentsMDFile = "AGENTS.md"

// ToolSearchOptions decides when the tool-search middleware is installed.
type ToolSearchOptions struct {
	// Enabled turns it on. Even then it only applies when enough tools would be
	// hidden to pay for the extra call.
	Enabled bool
	// MinTools is the number of dynamic tools below which nothing is hidden.
	// Zero uses DefaultToolSearchMinTools.
	MinTools int
	// AlwaysVisible names tools that must stay in the model's tool list even
	// though they come from an MCP server: the ones a run needs before it knows
	// what to search for.
	AlwaysVisible []string
}

// DefaultToolSearchMinTools is the threshold the tool-search middleware uses
// when ToolSearchOptions.MinTools is unset. Below it the schemas are cheap
// enough that hiding them costs more (a search round, a cache miss) than they
// save.
const DefaultToolSearchMinTools = 40

// Failover is the agent-level degradation policy.
//
// It lives at the agent rather than in the chat client so that one failure
// moves the whole rest of the run: after a model has failed, eino keeps using
// the model that worked for every later call in the same run, instead of every
// call retrying the failed one first.
type Failover struct {
	// Model returns the model to fall back to. It is a function because the
	// choice is per run — a run on a user-configured endpoint must not borrow
	// the deployment's fallback — and because a graph may outlive the run it was
	// built for.
	Model func(ctx context.Context) (einomodel.BaseChatModel, error)
	// ShouldFailover reports whether an error is worth another model. Nil means
	// yes for every error other than a cancelled run.
	ShouldFailover func(ctx context.Context, output *schema.Message, err error) bool
}

// SkillMiddleware turns skill records into the middleware that exposes them to
// the model. It is implemented by pkg/skill; a nil implementation disables the
// skill subsystem, and with it the filesystem backend.
type SkillMiddleware interface {
	// Middleware builds the skill middleware for one agent set. agentKey is the
	// normalized key whose directory segment the middleware's backend must also
	// use (see IsolatedSkillsDir).
	Middleware(ctx context.Context, agentKey string, skills []*model.SkillM) (adk.ChatModelAgentMiddleware, error)
}

// KnowledgeBaseTools turns knowledge-base records into retrieval tools. It is
// implemented by pkg/kbtool; a nil implementation disables knowledge bases.
type KnowledgeBaseTools interface {
	// Tools returns the retrieval tools for the given records. Returning an
	// empty slice is normal (nothing configured) and is not an error.
	Tools(ctx context.Context, kbs []*model.KnowledgeBaseM) []tool.BaseTool
}

// VisionMiddleware turns images into text for a primary model that cannot see
// them. It is implemented in this package from a vision endpoint; a nil
// implementation disables image extraction.
type VisionMiddleware interface {
	Middleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error)
}

// Deps are the collaborators a Builder holds for its whole lifetime.
type Deps struct {
	// ChatModel is the model the agent reasons with. Required.
	ChatModel einomodel.BaseChatModel
	// Skills is optional: nil disables the skill subsystem.
	Skills SkillMiddleware
	// KnowledgeBases is optional: nil disables knowledge-base tools.
	KnowledgeBases KnowledgeBaseTools
	// ImageGen draws images for the generate_image tool. Nil leaves the tool
	// out, which is what a deployment with no image model gets: a tool the
	// model can see but cannot use is worse than no tool.
	ImageGen ImageGenerator
	// Vision is optional: nil disables image extraction.
	Vision VisionMiddleware
	// Security is optional: nil disables the outbound review of the answer.
	Security SecurityReviewer
	// Grounding is optional: nil disables the check of the answer against the
	// material the run retrieved.
	Grounding GroundingReviewer
}

// HandlerGroups are the caller's handlers, split by where in the chain they
// belong. The builder interleaves them with its own so the final order is
// correct regardless of who supplies what.
type HandlerGroups struct {
	// ToolHandlers wrap every tool call. They are applied first, in order, so
	// the first entry is the outermost wrapper. A caller that wants failed
	// calls rendered as failures and reported to the model must put the tool
	// tracer before any error-converting handler — the builder's own tool-error
	// handler is always inserted ahead of this list.
	ToolHandlers []adk.ChatModelAgentMiddleware
	// ModelHandlers run around each model call (context trimming, etc.).
	ModelHandlers []adk.ChatModelAgentMiddleware
	// SummaryHandler compresses the in-run context. Nil disables it.
	SummaryHandler adk.ChatModelAgentMiddleware
}

// Request is the resolved input of one build. Everything name-based has already
// been turned into records by the caller; the builder does not read a store.
type Request struct {
	// AgentNames are the agents selected for this run; they determine the agent
	// key and the skill directory segment. Empty falls back to the default key.
	AgentNames []string
	// Instruction is the agent's system prompt.
	Instruction string
	// Skills are the skill records the selected agents declared.
	Skills []*model.SkillM
	// KnowledgeBases are the knowledge-base records the selected agents declared.
	KnowledgeBases []*model.KnowledgeBaseM
	// BaseTools are the tools the caller already resolved (built-ins and MCP).
	BaseTools []tool.BaseTool
	// MCPTools names the entries of BaseTools that an MCP server published.
	// Only these are candidates for tool search: the built-ins and the tools
	// this builder adds itself stay visible, because hiding the one tool whose
	// contract is to consult a human would be self-defeating.
	MCPTools []string
	// Handlers are the caller's handler groups.
	Handlers HandlerGroups
	// CheckPoints stores a run that paused for approval so any pod can resume
	// it. Nil means the run cannot pause.
	CheckPoints adk.CheckPointStore
	// ApprovalActive reports whether the run can pause for a human decision.
	// When true tool batches are forced sequential.
	ApprovalActive bool
}

// Result is what a build produced, plus the facts a caller wants to log.
type Result struct {
	Runner *adk.Runner
	// AgentKey is the normalized identity of the agent set, also the runner's name.
	AgentKey string
	// IsolatedSkillsDir is the skill root for this agent set; empty when skills
	// are disabled. It is the same path the LLM is told to look in and the
	// sandbox allows.
	IsolatedSkillsDir string
	// ToolNames are the model-facing tool names, in the order they were kept.
	ToolNames []string
}

// Builder builds runners from resolved resources. It is safe for concurrent use
// as long as its Deps are. The only state it keeps between builds is when it
// last collected expired offloaded files, which is deliberately shared: the
// sweep is process-wide work and doing it once is the point.
type Builder struct {
	deps Deps
	opts Options

	// sweepMu guards sweptAt, which rate-limits the offload sweep.
	sweepMu sync.Mutex
	sweptAt time.Time
}

// NewBuilder returns a Builder. deps.ChatModel is required.
func NewBuilder(deps Deps, opts Options) *Builder {
	return &Builder{deps: deps, opts: opts}
}

// Build assembles the agent and returns a runner for it.
func (b *Builder) Build(ctx context.Context, req Request) (*Result, error) {
	if b.deps.ChatModel == nil {
		return nil, fmt.Errorf("agentbuilder: ChatModel is nil")
	}

	agentKey := NormalizeAgentKey(req.AgentNames, DefaultAgentKey)
	isolatedSkillsDir := ""
	if b.skillsEnabled(req) {
		isolatedSkillsDir = IsolatedSkillsDir(b.opts.SkillsDir, agentKey)
	}

	allTools, toolNames := b.assembleTools(ctx, req)

	// The backend comes first because the handler chain depends on it: the
	// tool-output reducer can only offload where there is somewhere safe to
	// write.
	backend, err := b.filesystemBackend(ctx, isolatedSkillsDir)
	if err != nil {
		return nil, err
	}

	// The count is the figure tool search is decided on, so it is recorded even
	// when the middleware is off: enabling it without a baseline is how a cache
	// regression gets attributed to the wrong change.
	slog.DebugContext(ctx, "[agentbuilder] tool set",
		"agent_key", agentKey, "tools", len(toolNames), "mcp_tools", len(req.MCPTools),
		"tool_search", b.opts.ToolSearch.Enabled)

	handlers, err := b.assembleHandlers(ctx, req, agentKey, isolatedSkillsDir, backend, allTools, toolNames)
	if err != nil {
		return nil, err
	}

	b.sweepOffload(isolatedSkillsDir)

	cfg := &deep.Config{
		Name:                   agentKey,
		Description:            b.opts.Description,
		ChatModel:              b.deps.ChatModel,
		Instruction:            req.Instruction,
		MaxIteration:           b.opts.MaxIteration,
		Handlers:               handlers,
		WithoutWriteTodos:      !b.opts.DeepExtrasEnabled,
		WithoutGeneralSubAgent: !b.opts.DeepExtrasEnabled,
		ModelRetryConfig:       modelRetryConfig(b.opts.ModelRetryMax),
		ModelFailoverConfig:    b.failoverConfig(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               allTools,
				ExecuteSequentially: b.executeSequentially(req),
				UnknownToolsHandler: unknownToolResult(toolNames),
			},
		},
	}
	// Only set the filesystem fields when there is a sandbox to set them to:
	// the deep agent registers its file and execute tools on a non-nil backend.
	if backend != nil {
		if b.opts.MultiModalRead {
			// The deep agent has no switch for multimodal reads, so the
			// middleware is built here and deep is left without a backend —
			// otherwise both would register read_file and the graph would carry
			// the tool twice.
			fm, mwErr := adkfilesystemmw.NewTyped[*schema.Message](ctx, &adkfilesystemmw.MiddlewareConfig{
				Backend:           backend,
				StreamingShell:    backend,
				UseMultiModalRead: true,
			})
			if mwErr != nil {
				return nil, fmt.Errorf("agentbuilder: multimodal filesystem middleware: %w", mwErr)
			}
			cfg.Handlers = append(cfg.Handlers, fm)
		} else {
			cfg.Backend = backend
			cfg.StreamingShell = backend
		}
	}

	agent, err := deep.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("agentbuilder: deep.New: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: req.CheckPoints,
	})

	return &Result{
		Runner:            runner,
		AgentKey:          agentKey,
		IsolatedSkillsDir: isolatedSkillsDir,
		ToolNames:         toolNames,
	}, nil
}

// skillsEnabled reports whether the skill middleware should be built at all.
func (b *Builder) skillsEnabled(req Request) bool {
	return b.deps.Skills != nil && b.opts.SkillsDir != "" && len(req.Skills) > 0
}

// executeSequentially reports whether tool batches run one call at a time. A
// run that can pause for approval is always sequential (see Options).
func (b *Builder) executeSequentially(req Request) bool {
	return b.opts.ExecuteSequentially || req.ApprovalActive
}

// systemPrompt returns the deployment-wide system prompt for this build.
//
// The configured value wins; the provider is consulted only when it is empty, so
// a deployment that pinned the prompt is never overridden by an edit made at
// runtime. Reading it per build is what makes such an edit take effect on the
// next run rather than the next restart.
func (b *Builder) systemPrompt() string {
	if b.opts.Prompts.System != "" {
		return b.opts.Prompts.System
	}
	if b.opts.SystemPrompt == nil {
		return ""
	}
	return b.opts.SystemPrompt()
}

// modelRetryConfig returns nil unless a budget was configured, so the deep
// agent does not add its own retries on top of the chat client's.
func modelRetryConfig(max int) *adk.ModelRetryConfig {
	if max <= 0 {
		return nil
	}
	return &adk.ModelRetryConfig{MaxRetries: max}
}

// failoverAttempts is the budget handed to eino's failover wrapper.
//
// It counts the first call as an attempt: attempt 1 dispatches to the run's own
// model, attempt 2 to the fallback. So 2 means "one fallback", not two.
const failoverAttempts = 2

// failoverConfig turns the deployment's policy into eino's.
//
// eino calls GetFailoverModel for every attempt including the first, so the
// first attempt must return the run's own model — otherwise configuring a
// fallback would silently replace the primary one.
func (b *Builder) failoverConfig() *adk.ModelFailoverConfig[*schema.Message] {
	policy := b.opts.Failover
	if policy == nil || policy.Model == nil {
		return nil
	}
	primary := b.deps.ChatModel

	return &adk.ModelFailoverConfig[*schema.Message]{
		MaxRetries: failoverAttempts,
		ShouldFailover: func(ctx context.Context, output *schema.Message, err error) bool {
			if ctx.Err() != nil || err == nil {
				return false
			}
			if policy.ShouldFailover != nil {
				return policy.ShouldFailover(ctx, output, err)
			}
			return true
		},
		GetFailoverModel: func(ctx context.Context, fc *adk.FailoverContext[*schema.Message]) (einomodel.BaseChatModel, []*schema.Message, error) {
			if fc.FailoverAttempt <= 1 {
				return primary, nil, nil
			}
			m, err := policy.Model(ctx)
			if err != nil {
				return nil, nil, err
			}
			return m, nil, nil
		},
	}
}
