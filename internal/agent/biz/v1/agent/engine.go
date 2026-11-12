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

package agent

import (
	"context"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/modelguard"
	"github.com/onexstack/onexai/internal/agent/pkg/usagereport"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// defaultInstruction is the system prompt used by the eino agent.
const defaultInstruction = "You are onexai, a helpful AI assistant. Answer clearly and concisely."

// ToolsFunc resolves the tool set for a single run from the MCP server names and
// the agents that run selected (see agentapi.RunOptions.MCPs and .Agents). An
// empty server selection must yield the built-in tools only.
//
// It is a function rather than a slice because tool scope is per run, and
// because resolving MCP servers dials them: the caller caches, this package
// stays unaware of how tools are obtained. The agents are passed because a
// server may publish a different tool set to different agents, so two runs that
// name the same servers but different agents must resolve different tools. A nil
// ToolsFunc disables tools.
type ToolsFunc func(mcps, agents []string) []tool.BaseTool

// EngineConfig configures engine construction.
type EngineConfig struct {
	// UsageReporter sends a finished run's usage to the platform's ledger, or nil
	// when this deployment reports nowhere.
	//
	// Nil is the ordinary case for a deployment whose runs all arrive through
	// OneX's own agent service, which does its own accounting from the run's
	// events. It is the Lark path that needs this: those runs never touch the
	// platform, so without a reporter their consumption stays invisible to the
	// ledger. See usagereport for why the report is best-effort.
	UsageReporter usagereport.Reporter
	// BaseURL, APIKey and Model build a chat model when ChatModel is nil. The
	// server always supplies ChatModel, so these exist for callers that have no
	// model of their own to share (the tests).
	BaseURL string
	APIKey  string
	Model   string
	// FallbackModel, MaxRetries and Backoff apply to a model built from the
	// fields above. They are ignored when ChatModel is supplied, which is
	// assumed to be configured already.
	FallbackModel string
	MaxRetries    int
	Backoff       time.Duration
	// Tools resolves the per-run tool set.
	Tools ToolsFunc
	// DelegationTool is the agent-as-tool tool, added to a run's tool set only
	// when the selected agent names delegates. Nil means this deployment does
	// not delegate.
	DelegationTool tool.BaseTool
	// CallTools builds the tools for Agent.spec.calls. It is given the run
	// context and the resolved call names, and returns the dispatcher for the
	// external A2A targets among them. Nil disables calls.
	CallTools func(context.Context, []string) []tool.BaseTool
	// MemoryTools are the long-term memory tools, added only when the selected
	// agent has memory enabled. Empty means this deployment has no memory.
	MemoryTools []tool.BaseTool
	// Checkpoints stores a run that paused for approval, so the answer to it can
	// be served by any pod. Nil means approval is unavailable: a run whose policy
	// gates something then fails rather than running the tool unapproved, because
	// a gate that cannot pause is a gate that does not exist.
	Checkpoints checkpoint.Store
	// Reserved names the tools approval must never gate, however well their names
	// match a pattern. It is the built-in registry's own list: the one tool whose
	// contract is to consult a human is not something to gate on consulting a
	// human. Authorization excludes the same names. Ignored by the "http" engine.
	Reserved []string
	// Authz is the process-wide authorization Coordinator. When non-nil and
	// enabled, a tool call it does not Allow is turned into an approval request
	// rather than a refusal. Ignored by the "http" engine, which has no tool
	// loop to authorize.
	Authz *authz.Coordinator
	// AgentAuthz resolves an Agent's own authorization Webhooks. When any
	// selected Agent names them, they replace Authz for the tool call; the
	// platform coordinator is then not consulted. Nil leaves the existing path.
	AgentAuthz *agentauthz.Resolver
	// ToolOrigins maps a model-facing tool name to the MCP server that published
	// it, for the given servers and agents. Authorization needs it to attribute a
	// call to the right server. The agents matter because a server may publish a
	// different tool set to different agents, so the attribution is read from the
	// same discovery the run's own tools came from. Nil disables the attribution
	// (every MCP tool then reads as "no MCP"), which is what a deployment without
	// MCP servers has.
	ToolOrigins func(mcps, agents []string) map[string]string
	// MCPResourceMappings returns, for the given servers, each server's raw
	// resource_mappings JSON. Authorization builds the resource extractor from
	// it once per run.
	MCPResourceMappings func(mcps []string) map[string]string
	// ToolTimeout bounds a single tool invocation. Zero means unbounded. Ignored
	// by the "http" engine, which has no tool loop.
	ToolTimeout time.Duration
	// MaxContextTokens is the prompt budget the run keeps itself under, by
	// summarizing and by truncating oversized tool outputs. Zero disables both.
	// Ignored by the "http" engine, which has no tool loop to bound.
	MaxContextTokens int
	// MaxToolOutputChars caps one tool output, in characters. Zero disables it.
	// Ignored by the "http" engine.
	MaxToolOutputChars int
	// ModelGuard, when non-nil and enabled, bounds and protects model endpoint
	// calls: a per-endpoint rate limit and a consecutive-failure breaker. A nil
	// guard leaves every call ungated, which is the default.
	ModelGuard *modelguard.Guard

	// ── deep-agent construction (optional) ────────────────────────────────────

	// ChatModel, when non-nil, is used instead of constructing one from the
	// connection fields above. It exists so the builder and the engine share one
	// model instance rather than each holding its own copy of the same client.
	ChatModel *chatmodel.OpenAIChatModel
	// ModelRouter, when non-nil, is the stable model the graph holds. It
	// resolves the run's effective endpoint from the run context on every
	// Generate/Stream call, so a cached runner does not capture one run's
	// model. See runModelRouter.
	ModelRouter einomodel.ToolCallingChatModel
	// ResolveChatModel builds (or returns a cached) model for a configured
	// endpoint name. A nil resolver leaves every run on the deployment model.
	ResolveChatModel func(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error)
	// Builder assembles the agent through pkg/agentbuilder: the deep agent
	// graph, with skills, knowledge bases and the sandboxed filesystem backend.
	// Nil gives the engine a builder with no optional subsystems, which is what
	// the tests use; the server always supplies its own.
	Builder *agentbuilder.Builder
	// ResolveSkills turns the run's skill names into records. Nil means the run
	// gets no skill tool. It is a function rather than a store so this package
	// keeps depending on neither the store nor the builder's inputs.
	ResolveSkills func(ctx context.Context, names []string) []*model.SkillM
	// ResolveKnowledgeBases turns the run's knowledge-base names into records.
	// Nil means the run gets no retrieval tool.
	ResolveKnowledgeBases func(ctx context.Context, names []string) []*model.KnowledgeBaseM
	// ResolveRun resolves an agent selection into the values a runner's graph
	// depends on. It is what lets the runner cache rebuild a runner from nothing
	// but the agent names.
	ResolveRun func(ctx context.Context, agents []string) RunSpec
	// RunnerTTL, when positive, turns on runner caching: a runner for an agent
	// set is built once and reused until it is this old. Zero keeps the
	// per-run build, which is the default because it is the one with no way to
	// be wrong.
	RunnerTTL time.Duration
	// PinTTL is how long a paused run's runner is held unrebuilt. It should
	// match the checkpoint TTL: past that the pause can no longer be answered,
	// so the runner it needs is free to be replaced.
	PinTTL time.Duration
}

// NewEngine constructs the agent Engine: an eino/adk deep agent and Runner, with
// the per-run tools resolved by cfg.Tools, retry, and model fallback.
//
// Returns nil when there is no model to reason with — neither a shared instance
// nor the connection fields to build one.
func NewEngine(cfg EngineConfig) agentapi.Engine {
	if cfg.ChatModel == nil && cfg.BaseURL == "" {
		return nil
	}
	return newEinoEngine(cfg, defaultInstruction)
}
