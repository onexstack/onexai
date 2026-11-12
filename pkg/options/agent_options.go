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

package options

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// ToolSearchOptions configures the tool-search middleware.
//
// Only MCP-published tools can be hidden; the built-in tools and the ones the
// builder adds stay visible, because hiding a tool that exists to consult a
// human would defeat it.
// RuntimeOptions controls the runtime port.
type RuntimeOptions struct {
	// PortEnabled routes Ask/Approve through the runtime port. False keeps the
	// historical Engine path.
	PortEnabled bool `json:"port-enabled" mapstructure:"port-enabled"`
	// DefaultProfile names the profile used when an agent declares none. Empty
	// means the built-in in-process eino profile.
	DefaultProfile string `json:"default-profile" mapstructure:"default-profile"`
	// InternalAddr is the listen address of the runtime-only surface (capability
	// MCP and model data plane). Empty defaults to 127.0.0.1:8091; "off" disables
	// the listener.
	InternalAddr string `json:"internal-addr" mapstructure:"internal-addr"`
	// TokenKey signs per-run capability tokens. Empty generates a random key,
	// which is correct for one replica but does not survive a restart.
	TokenKey string `json:"token-key" mapstructure:"token-key"`
}

// SandboxOptions configures the containerized execution environment.
type SandboxOptions struct {
	Enabled      bool              `json:"enabled" mapstructure:"enabled"`
	Images       map[string]string `json:"images" mapstructure:"images"`
	DefaultImage string            `json:"image" mapstructure:"image"`
	DefaultTTL   time.Duration     `json:"default-ttl" mapstructure:"default-ttl"`
	MaxTTL       time.Duration     `json:"max-ttl" mapstructure:"max-ttl"`
	CPUs         string            `json:"cpus" mapstructure:"cpus"`
	Memory       string            `json:"memory" mapstructure:"memory"`
	PidsLimit    int               `json:"pids-limit" mapstructure:"pids-limit"`
	// Network is the Docker network the sandbox joins. Empty means "none": no
	// route out. Host networking and namespace sharing are refused at startup,
	// because either would undo the isolation the sandbox exists for.
	Network string `json:"network" mapstructure:"network"`
	// EgressProxy is exported into the sandbox as HTTP(S)_PROXY. Set it together
	// with a network whose only reachable endpoint is an allowlisting proxy.
	EgressProxy string `json:"egress-proxy" mapstructure:"egress-proxy"`
	// NoProxy lists hosts that bypass the proxy, exported as NO_PROXY.
	NoProxy string   `json:"no-proxy" mapstructure:"no-proxy"`
	Command []string `json:"command" mapstructure:"command"`
}

// DelegationOptions bounds agent-as-tool.
type DelegationOptions struct {
	// MaxDepth is how many delegations one chain may contain. Zero means the
	// default of 3. It is the only bound on recursive delegation, so a
	// deployment that lets agents call each other in a cycle still terminates.
	MaxDepth int `json:"max-depth" mapstructure:"max-depth"`
}

type ToolSearchOptions struct {
	// Enabled installs the middleware when the run has enough MCP tools.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// MinTools is the number of MCP tools below which nothing is hidden. Zero
	// means the builder's default (40).
	MinTools int `json:"min-tools" mapstructure:"min-tools"`
	// AlwaysVisible names MCP tools that must stay in the model's tool list: the
	// ones a run needs before it knows what to search for.
	AlwaysVisible []string `json:"always-visible" mapstructure:"always-visible"`
}

// AgentsMDOptions configures the AGENTS.md injection. It only takes effect when
// the deployment has a skills directory, because the file is read through the
// agent's own sandbox.
type AgentsMDOptions struct {
	// Enabled installs the middleware.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// Files are read in order, relative to the agent's sandbox root. Empty means
	// AGENTS.md.
	Files []string `json:"files" mapstructure:"files"`
	// MaxBytes bounds all injected content together. Leave it set: the content is
	// added to every model call and is not part of the context budget.
	MaxBytes int `json:"max-bytes" mapstructure:"max-bytes"`
	// PerFileMaxBytes bounds one file. Zero means no per-file limit.
	PerFileMaxBytes int `json:"per-file-max-bytes" mapstructure:"per-file-max-bytes"`
}

// MultimodalReadOptions configures multimodal read_file.
type MultimodalReadOptions struct {
	// Enabled replaces the plain read_file tool with one that returns images
	// and PDFs as model input parts. It requires a skills directory, because
	// the file is read through the agent's sandbox.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
}

// KbMultiQueryOptions configures the knowledge-base multi-query experiment.
type KbMultiQueryOptions struct {
	// Enabled installs the rewriter.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// Count is how many extra phrasings to ask for. Zero uses 2.
	Count int `json:"count" mapstructure:"count"`
	// SampleRate is the fraction of searches that take part, in [0,1]. Zero or
	// negative means every search; a partial rate turns it into an A/B whose arms
	// the "[kbtool] retrieval" log line tells apart.
	SampleRate float64 `json:"sample-rate" mapstructure:"sample-rate"`
}

// ModelGuardOptions configures the per-endpoint model guard.
type ModelGuardOptions struct {
	// QPS is the sustained model-call rate allowed per endpoint. Zero disables
	// the rate limit.
	QPS float64 `json:"qps" mapstructure:"qps"`
	// Burst is the bucket's capacity. Zero uses one call.
	Burst int `json:"burst" mapstructure:"burst"`
	// FailureThreshold is how many consecutive failures open the breaker. Zero
	// disables the breaker.
	FailureThreshold int `json:"failure-threshold" mapstructure:"failure-threshold"`
	// Cooldown is how long an open breaker refuses calls before probing again.
	// Zero uses 30s.
	Cooldown time.Duration `json:"cooldown" mapstructure:"cooldown"`
}

// AgentOptions configures the AI agent flow: which execution engine to use,
// per-conversation concurrency, timeouts, and observability.
type AgentOptions struct {
	// Engine is retained for configuration compatibility. Every run now uses the
	// eino engine; "http" is rejected at validation rather than silently mapped,
	// because switching a deployment's execution core without its operator
	// knowing would change tool, approval and truncation behaviour at once.
	Engine string `json:"engine" mapstructure:"engine"`
	// Sandbox configures containerized command/file execution.
	Sandbox SandboxOptions `json:"sandbox" mapstructure:"sandbox"`
	// Delegation bounds agents calling agents as tools.
	Delegation DelegationOptions `json:"delegation" mapstructure:"delegation"`
	// Runtime selects how the runtime port is used. It is off by default so an
	// existing deployment keeps the exact Engine path until the port has passed
	// its parity tests, and it can be turned on per deployment afterwards.
	Runtime RuntimeOptions `json:"runtime" mapstructure:"runtime"`
	// ChatModel overrides the model name for the agent (falls back to llm.model).
	ChatModel string `json:"chat-model" mapstructure:"chat-model"`
	// FallbackModel is tried when the primary model fails after retries.
	FallbackModel string `json:"fallback-model" mapstructure:"fallback-model"`
	// MaxRetries is the number of retries per model on transient LLM failures.
	MaxRetries int `json:"max-retries" mapstructure:"max-retries"`
	// RetryBackoff is the base backoff between retries (exponential).
	RetryBackoff time.Duration `json:"retry-backoff" mapstructure:"retry-backoff"`
	// MaxConcurrentPerConversation caps concurrent AI runs per conversation
	// (1 = single-flight).
	MaxConcurrentPerConversation int `json:"max-concurrent-per-conversation" mapstructure:"max-concurrent-per-conversation"`
	// RunTimeout bounds a single agent run.
	RunTimeout time.Duration `json:"run-timeout" mapstructure:"run-timeout"`
	// ToolTimeout bounds a single tool invocation (eino engine).
	ToolTimeout time.Duration `json:"tool-timeout" mapstructure:"tool-timeout"`
	// MCPEnabled is the master switch for MCP tools. Disabling it leaves the
	// onexai_agent_mcp rows in place but never dials them.
	MCPEnabled bool `json:"mcp-enabled" mapstructure:"mcp-enabled"`
	// MCPDiscoveryTimeout bounds one MCP server's handshake plus tool listing.
	MCPDiscoveryTimeout time.Duration `json:"mcp-discovery-timeout" mapstructure:"mcp-discovery-timeout"`
	// MCPCacheTTL is how long a discovered MCP tool set is served before it is
	// rebuilt, so adding or editing an oai_ai_mcp row takes effect without a
	// restart.
	MCPCacheTTL time.Duration `json:"mcp-cache-ttl" mapstructure:"mcp-cache-ttl"`
	// CustomCommand bounds the outbound calls http custom commands make.
	CustomCommand CustomCommandOptions `json:"custom-command" mapstructure:"custom-command"`
	// ToolSearch hides the MCP-published tools behind a single tool_search entry
	// until the model asks for them. Off by default: filtering the tool list
	// between model calls gives up prompt-cache hits, so it only pays for itself
	// when the MCP tool schemas dominate the prompt. Turn it on per deployment
	// after reading the "[agentbuilder] tool set" count in the logs.
	ToolSearch ToolSearchOptions `json:"tool-search" mapstructure:"tool-search"`
	// AgentsMD injects the agent sandbox's AGENTS.md into every model call. Off
	// by default: the file is half-trusted input, so enabling it says this
	// deployment trusts the skill sources it checks out.
	AgentsMD AgentsMDOptions `json:"agents-md" mapstructure:"agents-md"`
	// ModelGuard bounds and protects model endpoint calls. Both mechanisms are
	// off by default: a rate limit needs a number only the operator knows, and a
	// breaker that opens on the wrong errors makes an outage worse.
	ModelGuard ModelGuardOptions `json:"model-guard" mapstructure:"model-guard"`
	// KbMultiQuery rewrites each knowledge-base question into several phrasings
	// and fuses the results. Off by default: it costs one extra model call and N
	// times the retrieval requests, and its benefit is corpus-specific. Turn it
	// on with a sample rate to run it as an experiment.
	KbMultiQuery KbMultiQueryOptions `json:"kb-multiquery" mapstructure:"kb-multiquery"`
	// MultimodalRead lets a vision model read images and PDFs from the agent's
	// sandbox instead of receiving their bytes as text. Off by default: it only
	// helps a vision model, and the file's bytes then travel through the model
	// call.
	MultimodalRead MultimodalReadOptions `json:"multimodal-read" mapstructure:"multimodal-read"`
	// ShowReasoning surfaces the model's reasoning stream by default.
	ShowReasoning bool `json:"show-reasoning" mapstructure:"show-reasoning"`
	// EnableTracing enables OpenTelemetry spans around agent runs.
	EnableTracing bool `json:"enable-tracing" mapstructure:"enable-tracing"`
	// MaxContextTokens is the prompt budget for one AI turn. History above it
	// is summarized before the run; a run that grows to 1.5x it is summarized
	// mid-run, and that rescue call is handed the whole context at once. So keep
	// this below roughly two thirds of the served model's context window, not
	// merely below the window itself. 0 disables all context management.
	MaxContextTokens int `json:"max-context-tokens" mapstructure:"max-context-tokens"`
	// ContextPreserveTokens is how much of the newest history compaction keeps
	// verbatim. 0 means MaxContextTokens/3.
	ContextPreserveTokens int `json:"context-preserve-tokens" mapstructure:"context-preserve-tokens"`
	// MaxToolOutputChars caps a single tool output, in characters. A tool that
	// returns more has its middle elided, because one oversized tool result can
	// exceed the model's window on its own. 0 disables truncation.
	MaxToolOutputChars int `json:"max-tool-output-chars" mapstructure:"max-tool-output-chars"`
	// MaxOutputTokens overrides llm.max-tokens per caller surface: the key is a
	// surface name, the value the ceiling for it. "default" is consulted for a
	// caller that matches no entry, so a deployment can set one number and then
	// single out the surfaces that need a different one.
	//
	// A surface is a name for *who is asking*, and the map only has entries for
	// the caller credentials in SurfaceKeys below — a caller with no entry gets
	// the "default" ceiling. A group chat wants a short answer it can render in
	// a card; the console can afford a long one. Both are the same model call, so
	// the only place the difference can live is here.
	//
	// Empty means llm.max-tokens decides for everyone.
	MaxOutputTokens map[string]int `json:"max-output-tokens" mapstructure:"max-output-tokens"`
	// SurfaceKeys maps a caller's AK/SK api-key to the surface name its runs are
	// capped as (a key of MaxOutputTokens). The api-key is the identifier the
	// AK/SK middleware already resolves to authorize the request — it is the
	// secret's own id, not the secret — so this maps a credential the deployment
	// has already handed out to a name the deployment chose.
	//
	// Why a mapping rather than a field on the request: the callers are internal
	// services that authenticate with AK/SK, and the middleware already turns
	// that credential into the request's authorization subject, so the
	// distinction exists before a body is parsed. Letting a request *declare* its
	// surface would be a caller-supplied string deciding its own budget, which is
	// the wrong direction. It also needs no contract change: this is
	// configuration, not a new field in AgentAskRequest.
	//
	// Empty means every caller is capped as "default".
	SurfaceKeys map[string]string `json:"surface-keys" mapstructure:"surface-keys"`
	// RequireApproval lists tool-name patterns that must be approved by a human
	// before they run, for every run whatever agents it selects: an exact tool
	// name, a "delete_*" prefix glob, or "*" for everything. It is the
	// server-wide default, unioned with what the run's MCP servers and agents
	// declare of their own. Empty gates nothing.
	RequireApproval []string `json:"require-approval" mapstructure:"require-approval"`
	// SessionIdleTTL is how long a session may go without activity before it
	// is closed. Zero disables the worker.
	SessionIdleTTL time.Duration `json:"session-idle-ttl" mapstructure:"session-idle-ttl"`
	// SessionIdleSweep is how often the idle worker scans. Zero uses 1m.
	SessionIdleSweep time.Duration `json:"session-idle-sweep" mapstructure:"session-idle-sweep"`
	// CheckpointTTL is how long a run paused for approval stays resumable. It is
	// the only garbage collection such a run gets, so it must outlast the
	// slowest plausible answer — a pause is resumed by a human, not by a retry.
	CheckpointTTL time.Duration `json:"checkpoint-ttl" mapstructure:"checkpoint-ttl"`
	// CompatApprovalMode is what the OpenAI-compatible endpoint
	// (/v1/chat/completions) does with a tool whose approval its caller cannot
	// give. That endpoint has no card and no approve call, so a question asked
	// there could never be answered.
	//
	//   - "deny" (default): the tool does not run, and the model is told so as an
	//     ordinary tool result, which lets it explain the refusal in the answer
	//     the caller receives.
	//   - "error": the request fails, for a caller that would rather be told the
	//     work needed a decision than receive an account of a tool that was
	//     quietly declined.
	//   - "allow": gated tools run ungated on this endpoint. It is for a
	//     deployment whose only OpenAI-compatible callers are trusted, and it
	//     makes agent.require-approval and the per-agent/MCP declarations
	//     ineffective for them.
	//
	// It applies to this endpoint alone: a run started from a chat pauses for its
	// approval however this is set.
	CompatApprovalMode string `json:"compat-approval-mode" mapstructure:"compat-approval-mode"`

	// ── deep-agent construction (see internal/agent/pkg/agentbuilder) ─────────

	// Description is the agent's human-readable description, passed through to
	// the deep agent's config so it shows up in logs and traces.
	Description string `json:"description" mapstructure:"description"`
	// UseDeepAgent is retired: every run now goes through the deep agent graph.
	//
	// It is kept, deprecated and ignored, so a configuration that still carries
	// `use-deep-agent: false` parses instead of failing on an unknown key. Its
	// only effect would have been to select the legacy ChatModelAgent path,
	// which no longer exists.
	//
	// Deprecated: has no effect.
	UseDeepAgent bool `json:"use-deep-agent" mapstructure:"use-deep-agent"`
	// SkillsDir is the local root directory where downloaded skills are
	// persisted, isolated per effective agent set:
	//   <SkillsDir>/<agentKey>/<host/owner/repo>/<branch>/<subdir>/...
	// Empty disables the skill subsystem entirely, and with it the filesystem
	// and shell tools the deep agent would otherwise expose — no directory
	// means nothing for those tools to safely operate inside.
	SkillsDir string `json:"skills-dir" mapstructure:"skills-dir"`
	// MaxIteration caps the deep agent's reasoning iterations (tool-call rounds).
	MaxIteration int `json:"max-iteration" mapstructure:"max-iteration"`
	// ExecuteSequentially makes the agent run a batch of tool calls one at a
	// time instead of in parallel. It is forced on whenever a run can pause for
	// approval: parallel batches raise several interrupts at once, which makes
	// the pause/resume addressing non-deterministic.
	ExecuteSequentially bool `json:"execute-sequentially" mapstructure:"execute-sequentially"`
	// DeepExtrasEnabled turns on the deep agent's built-in extras: the
	// write_todos planning tool and the general-purpose sub-agent. Off by
	// default so a deployment that wants none of them keeps exactly the tool
	// set it had before.
	DeepExtrasEnabled bool `json:"deep-extras-enabled" mapstructure:"deep-extras-enabled"`
	// ModelRetryMax, when > 0, is the deep agent's own model retry budget. It is
	// off by default because the chat model client already retries and falls
	// back; setting both multiplies attempts.
	ModelRetryMax int `json:"model-retry-max" mapstructure:"model-retry-max"`
	// RunnerCacheTTL is how long a built runner is reused for an agent set
	// before it is rebuilt. 0 (the default) builds one per run, which is the
	// configuration with no way to be wrong.
	//
	// Enabling it is a trade. A cached graph must be a function of the agent
	// selection alone, and a run paused for approval is resumed by the graph it
	// paused against — so a rebuild in between can leave a human's answer with
	// nothing to resolve. The cache holds an agent set's graph while such a run
	// may still be answered (see AgentOptions.CheckpointTTL, which bounds that),
	// but an entry rebuilt after the pause and before the answer is a resume
	// that fails. On a single-replica deployment the hold covers it; across
	// replicas it covers the pod the pause was taken on.
	RunnerCacheTTL time.Duration `json:"runner-cache-ttl" mapstructure:"runner-cache-ttl"`
	// Prompts injects prompt fragments the resource store does not carry.
	Prompts PromptsConfig `json:"prompts" mapstructure:"prompts"`
	// VisionModel is the model used to describe images before they reach a
	// primary model that cannot see them. Leave Model empty to disable.
	VisionModel *ChatModelConfig `json:"vision-model" mapstructure:"vision-model"`
	// MaxImagePixels is the largest image a model is shown: anything larger is
	// shrunk to fit at the model edge. Zero — the default — disables shrinking
	// and sends the original.
	//
	// # Why the default is off
	//
	// Shrinking is lossy, and the detail it costs first is small text — which is
	// precisely what a screenshot sent to this bot is for. The saving is real
	// (the bytes are inlined and re-sent on every turn that references them),
	// but it is a trade a deployment should make deliberately.
	MaxImagePixels int `json:"max-image-pixels" mapstructure:"max-image-pixels"`
	// ImageModel is the model used to draw images for /v1/images/generations
	// and for the generate_image tool. Leave Model empty to disable drawing —
	// a deployment with no image model answers every other request and refuses
	// a drawing with a message that names the missing configuration.
	ImageModel *ChatModelConfig `json:"image-model" mapstructure:"image-model"`
	// KnowledgeBase configures the Dify knowledge-base platform. Nil or an
	// empty key disables knowledge bases that live on it.
	KnowledgeBase *KnowledgeBaseOptions `json:"knowledge-base" mapstructure:"knowledge-base"`
	// RAGFlow configures the RAGFlow knowledge-base platform. Nil or an empty
	// base URL disables knowledge bases that live on it.
	RAGFlow *RAGFlowOptions `json:"ragflow" mapstructure:"ragflow"`
	// Watermark marks every retrieved passage with the asking user's identity,
	// using the same carrier scheme OneX writes into a knowledge-base chapter.
	// The key must be the subkey ox-kbtrace derives from the capability key
	// (`kbfingerprint.SubkeyLabel`), or a copy decodes to nobody.
	Watermark *WatermarkOptions `json:"watermark" mapstructure:"watermark"`
	// ReviewerModel is the small, fast model shared by the auxiliary review
	// passes: the inbound audit and the outbound security review. Leave Model
	// empty to disable both. APIKey and BaseURL inherit from the primary model
	// when unset.
	ReviewerModel *ChatModelConfig `json:"reviewer-model" mapstructure:"reviewer-model"`
	// InputAudit enables the inbound screening of a user's message before the
	// run starts. It needs ReviewerModel; with no auditor configured it is off.
	InputAudit bool `json:"input-audit" mapstructure:"input-audit"`
	// KbGradeModel is the model that grades a knowledge-base retrieval before it
	// is rendered: one call that reorders the passages by how well they answer
	// the question, drops the ones it rates irrelevant, and reports how much of
	// the question they cover. Leave Model empty to disable grading.
	//
	// # Why it has a model of its own instead of sharing ReviewerModel
	//
	// Sharing was the first shape, and it read well — both are "a small model
	// that reads a short text and returns a verdict". It fails on the switching:
	// configuring ReviewerModel turns on the outbound security review, which has
	// no switch of its own, so an operator who wanted grading would silently
	// have started rewriting every answer on the platform. A setting that
	// changes something else when you set it is a setting whose consequences are
	// not in its name, and the fix is a name of its own rather than a note in
	// the documentation.
	//
	// # Why it is worth an extra call
	//
	// Nothing else in the path can tell "these passages answer the question"
	// from "these passages are the closest thing the corpus had": the platforms
	// rank by similarity, which is a different question, and a model handed both
	// an ambiguous result and its own memory resolves the ambiguity by answering
	// from the memory. It also stands in for a reranker, which a RAGFlow tenant
	// has only if someone added a rerank model to it.
	//
	// Provider, BaseURL and APIKey inherit from the primary model when unset.
	KbGradeModel *ChatModelConfig `json:"kb-grade-model" mapstructure:"kb-grade-model"`
	// GroundingModel is the model that checks a finished answer against the
	// material the run retrieved. Leave Model empty to disable the check.
	//
	// # What it is for
	//
	// The prompt says which source wins where and to say so when they disagree.
	// Nothing verifies that it did, and the failure this whole path is prone to
	// — a model answering from its own memory while the log shows the corpus was
	// queried — is invisible from outside: the answer reads like a grounded one,
	// and the sources shown are what the retrieval read, not what the answer
	// used. This pass is the only place where the answer and the evidence are
	// looked at together.
	//
	// It costs one call per answer that retrieved something, and it only ever
	// appends a caveat — it never rewrites, because a rewrite would be a second
	// answer produced with less context than the first and delivered after the
	// reader had already read the first.
	//
	// It has a model of its own rather than sharing the reviewer's, for the
	// reason KbGradeModel gives: configuring that one also turns on the outbound
	// security review, so a shared model would make this switch change something
	// else.
	GroundingModel *ChatModelConfig `json:"grounding-model" mapstructure:"grounding-model"`
	// K8SGPT configures the per-cluster k8sgpt fleet.
	K8SGPT *K8SGPTOptions `json:"k8sgpt" mapstructure:"k8sgpt"`
	// DailyQuota caps how many requests one sender may make per day. 0 means no
	// quota. It is a capacity guard rather than a rate: the per-conversation
	// concurrency gate already stops one conversation from running two turns at
	// once, and this stops one person from running a thousand.
	//
	// It needs Redis to count. Without it the quota is inert, which startup says.
	DailyQuota int `json:"daily-quota" mapstructure:"daily-quota"`
	// DailyTokenQuota caps how many tokens one sender may spend per day. 0 means
	// no ceiling.
	//
	// It is a different guard from DailyQuota, not a bigger one: a request count
	// bounds how often someone asks, this bounds what their asking costs, and
	// the two diverge by two orders of magnitude — one question answered from a
	// course's knowledge base can cost what a hundred greetings do. Both can be
	// on at once; either alone is a coherent policy.
	//
	// The check happens before a run and the charge after it (see
	// ratelimit.TokenLimiter), because tokens are only known once the model has
	// answered. A sender can therefore finish one run over the ceiling, and the
	// next one is refused.
	//
	// It needs Redis to count. Without it the ceiling is inert, which startup
	// says.
	DailyTokenQuota int64 `json:"daily-token-quota" mapstructure:"daily-token-quota"`
	// WebhookDefaults names the run-lifecycle hooks applied to a run whose
	// agents declared none of their own.
	//
	// It exists for the agents nobody configured: provisioning writes the hook
	// names onto every agent it creates, and an agent created any other way
	// would otherwise run with no admission check at all — silently, because a
	// missing hook looks exactly like a run that was allowed.
	//
	// An agent that names "none" opts out of these; one that names its own
	// replaces them rather than adding to them.
	//
	// The names are resolved against the webhook table at call time, so an entry
	// here that no row matches is reported once per run rather than at startup.
	WebhookDefaults []string `json:"webhook-defaults" mapstructure:"webhook-defaults"`
	// A2APublicURL is the absolute base address the published agent card
	// advertises for its transports, e.g.
	// https://onexai.example.com/v1/chat/a2a. It does not move the card itself:
	// that is always served at the origin-root well-known path
	// /.well-known/agent-card.json.
	//
	// Empty leaves the card with the path-relative default, which a client that
	// fetched the card from the same origin can use and a strict one cannot. It
	// is not derived from the request headers: the card route is unauthenticated,
	// so a forged Host would point a peer at an address of the attacker's
	// choosing. Set A2ATrustedProxy to opt into that derivation instead.
	A2APublicURL string `json:"a2a-public-url" mapstructure:"a2a-public-url"`
	// A2ATrustedProxy lets the card derive its URLs from X-Forwarded-Proto/Host
	// when A2APublicURL is empty. Only correct when every request arrives through
	// a proxy this deployment controls.
	A2ATrustedProxy bool `json:"a2a-trusted-proxy" mapstructure:"a2a-trusted-proxy"`
	// A2ADefaultAgent names the agent set an A2A request gets when it does not
	// name one itself. Empty lets the server's own default apply.
	A2ADefaultAgent string `json:"a2a-default-agent" mapstructure:"a2a-default-agent"`
	// A2ADescription is the description the agent card carries.
	A2ADescription string `json:"a2a-description" mapstructure:"a2a-description"`
	// RagDocument enables the answer_from_document tool, which answers a
	// question from one file the caller names by ranking the file's chunks
	// first. Off by default: it spends a model call per chunk, so it is worth
	// enabling where documents are actually handed to the agent.
	RagDocument bool `json:"rag-document" mapstructure:"rag-document"`
	// RagTopK is how many document chunks reach the answering step. Zero means
	// the tool's own default.
	RagTopK int `json:"rag-top-k" mapstructure:"rag-top-k"`
	// LarkMessage enables the outbound copy-in tool, which lets the agent send a
	// message to a chat or a person on the caller's behalf. Off by default: it is
	// the one tool whose effect is outside the service, and the tool itself
	// refuses any caller who is not an SRE.
	LarkMessage bool `json:"lark-message" mapstructure:"lark-message"`
}

// Values for AgentOptions.CompatApprovalMode.
const (
	// CompatApprovalAllow runs gated tools without asking.
	CompatApprovalAllow = "allow"
	// CompatApprovalDeny refuses gated tools and lets the model explain.
	CompatApprovalDeny = "deny"
	// CompatApprovalError fails the request when a tool needs approval.
	CompatApprovalError = "error"
)

// NewAgentOptions creates AgentOptions with default values.
func NewAgentOptions() *AgentOptions {
	return &AgentOptions{
		Engine:                       "eino",
		MaxConcurrentPerConversation: 3,
		RunTimeout:                   5 * time.Minute,
		ToolTimeout:                  2 * time.Minute,
		MCPEnabled:                   true,
		MCPDiscoveryTimeout:          15 * time.Second,
		MCPCacheTTL:                  5 * time.Minute,
		CustomCommand: CustomCommandOptions{
			MaxTimeout:       60 * time.Second,
			MaxResponseBytes: 1 << 20,
		},
		MaxRetries:         2,
		RetryBackoff:       500 * time.Millisecond,
		MaxContextTokens:   500000,
		MaxToolOutputChars: 50000,
		CheckpointTTL:      24 * time.Hour,
		CompatApprovalMode: CompatApprovalDeny,
		MaxIteration:       50,
		// RunnerCacheTTL is deliberately left at zero: see its field comment.
		RunnerCacheTTL: 0,
	}
}

// Validate checks the AgentOptions fields.
func (o *AgentOptions) Validate() []error {
	var errs []error
	switch o.Engine {
	case "", "eino":
	case "http":
		// Fail fast rather than silently selecting eino: the http engine had no
		// tool loop, so a deployment that asked for it is relying on behaviour
		// eino does not reproduce (in particular, tools are simply not called).
		errs = append(errs, fmt.Errorf(
			`agent.engine "http" was removed; use "eino" (the http engine had no tool loop)`))
	default:
		errs = append(errs, fmt.Errorf("agent.engine %q is invalid (want eino)", o.Engine))
	}
	if o.MaxConcurrentPerConversation < 0 {
		errs = append(errs, fmt.Errorf("agent.max-concurrent-per-conversation must be >= 0"))
	}
	if o.MaxImagePixels < 0 {
		errs = append(errs, fmt.Errorf("agent.max-image-pixels must be >= 0"))
	}
	if o.MaxContextTokens < 0 {
		errs = append(errs, fmt.Errorf("agent.max-context-tokens must be >= 0"))
	}
	if o.ContextPreserveTokens < 0 {
		errs = append(errs, fmt.Errorf("agent.context-preserve-tokens must be >= 0"))
	}
	// Preserving at least as much as the budget leaves nothing to compact, so
	// the history would be re-summarized on every turn for no reduction.
	if o.ContextPreserveTokens > 0 && o.ContextPreserveTokens >= o.MaxContextTokens {
		errs = append(errs, fmt.Errorf(
			"agent.context-preserve-tokens (%d) must be less than agent.max-context-tokens (%d)",
			o.ContextPreserveTokens, o.MaxContextTokens))
	}
	if o.MaxToolOutputChars < 0 {
		errs = append(errs, fmt.Errorf("agent.max-tool-output-chars must be >= 0"))
	}
	// A negative ceiling is not "no ceiling" to a model API — it is a malformed
	// request — so it is refused here rather than sent.
	for surface, n := range o.MaxOutputTokens {
		if n < 0 {
			errs = append(errs, fmt.Errorf("agent.max-output-tokens.%s must be >= 0", surface))
		}
	}
	// A surface name that no ceiling declares resolves to "default", so a typo
	// would silently cap a caller at the wrong number and never say so. Refusing
	// it at startup is the only place the mistake is visible.
	if len(o.MaxOutputTokens) > 0 {
		for apiKey, surface := range o.SurfaceKeys {
			if _, ok := o.MaxOutputTokens[surface]; !ok {
				errs = append(errs, fmt.Errorf(
					"agent.surface-keys[%s] names surface %q, which agent.max-output-tokens does not declare",
					apiKey, surface))
			}
		}
	}
	if o.CheckpointTTL < 0 {
		errs = append(errs, fmt.Errorf("agent.checkpoint-ttl must be >= 0"))
	}
	// Checked at startup because the alternative is discovering a typo as an
	// unapproved tool call: an unrecognized mode must not be read as "allow".
	switch o.CompatApprovalMode {
	case "", CompatApprovalAllow, CompatApprovalDeny, CompatApprovalError:
	default:
		errs = append(errs, fmt.Errorf("agent.compat-approval-mode must be one of %q, %q or %q, got %q",
			CompatApprovalAllow, CompatApprovalDeny, CompatApprovalError, o.CompatApprovalMode))
	}
	if o.MaxIteration < 0 {
		errs = append(errs, fmt.Errorf("agent.max-iteration must be >= 0"))
	}
	if o.ModelRetryMax < 0 {
		errs = append(errs, fmt.Errorf("agent.model-retry-max must be >= 0"))
	}
	// An empty skills-dir is the default and means the whole skill subsystem
	// (and the filesystem/shell tools it justifies) is off, so it is not an
	// error. A configured one, however, must not contain traversal: it becomes
	// both the on-disk root and the sandbox allow-list.
	if err := validateSkillsDir(o.SkillsDir); err != nil {
		errs = append(errs, err)
	}
	// The audit is a security control, and one that is switched on in the file
	// but has no model to run it is off in practice: the door reads as screened
	// and nothing screens it. Refused here rather than warned about at the first
	// request, where it would be a log line nobody reads.
	if o.InputAudit && !o.ReviewerModel.Enabled() {
		errs = append(errs, fmt.Errorf(
			"agent.input-audit is on but agent.reviewer-model names no model; set one or turn the audit off"))
	}
	errs = append(errs, o.VisionModel.validate("agent.vision-model")...)
	errs = append(errs, o.ImageModel.validate("agent.image-model")...)
	errs = append(errs, o.ReviewerModel.validate("agent.reviewer-model")...)
	errs = append(errs, o.KbGradeModel.validate("agent.kb-grade-model")...)
	errs = append(errs, o.GroundingModel.validate("agent.grounding-model")...)
	errs = append(errs, o.KnowledgeBase.validate()...)
	errs = append(errs, o.RAGFlow.validate()...)
	errs = append(errs, o.Watermark.validate()...)
	errs = append(errs, o.validateA2A()...)
	return errs
}

// validateA2A checks the published agent card's address.
//
// A relative value is refused rather than repaired: the card is read by other
// services, and a URL they cannot resolve fails at their first call, where the
// failure looks like this deployment is down. The proxy switch is checked
// against the explicit URL because setting both means one of them is inert, and
// silently ignoring a configured value is how a deployment ends up advertising
// an address nobody intended.
func (o *AgentOptions) validateA2A() []error {
	var errs []error
	if o.A2APublicURL != "" {
		parsed, err := url.Parse(o.A2APublicURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			errs = append(errs, fmt.Errorf(
				"agent.a2a-public-url %q must be an absolute http:// or https:// URL", o.A2APublicURL))
		}
	}
	if o.A2ATrustedProxy && o.A2APublicURL != "" {
		errs = append(errs, fmt.Errorf(
			"agent.a2a-trusted-proxy is set but agent.a2a-public-url already names the advertised address; "+
				"the proxy switch would have no effect, so one of them is a mistake"))
	}
	return errs
}

// WatermarkOptions configures per-reader marks on retrieved passages.
//
// Enabled without a key is refused at startup: the retriever would otherwise
// skip the mark silently and the deployment would believe it had attribution
// while every copy decoded to nobody.
type WatermarkOptions struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// Key is the base64 (standard or raw URL) or hex encoding of the 32-byte
	// fingerprint subkey. It is intentionally the *subkey*, not the capability
	// key it was derived from: onexai needs to write marks, not to mint tokens.
	// The derivation label is fixed in kbfingerprint.SubkeyLabel; making it
	// configurable here would let a deployment write marks that ox-kbtrace
	// cannot read back.
	Key string `json:"key" mapstructure:"key"`
}

func (o *WatermarkOptions) validate() []error {
	if o == nil || !o.Enabled {
		return nil
	}
	if strings.TrimSpace(o.Key) == "" {
		return []error{fmt.Errorf("agent.watermark.enabled is on but agent.watermark.key is empty; " +
			"set the kbfingerprint subkey or turn the watermark off")}
	}
	return nil
}

// validate checks the Dify platform's configuration. A base URL without a key
// would authenticate every retrieval call as an anonymous client, which the
// platform answers with a permission error on the first question rather than
// at startup.
func (o *KnowledgeBaseOptions) validate() []error {
	if o == nil || !o.Enabled() {
		return nil
	}
	var errs []error
	if o.APIKey == "" {
		errs = append(errs, fmt.Errorf("agent.knowledge-base.api-key is required when base-url is set"))
	}
	if o.Timeout < 0 {
		errs = append(errs, fmt.Errorf("agent.knowledge-base.timeout must be >= 0"))
	}
	return errs
}

// validate checks RAGFlow's configuration. A base URL without a key would have
// every retrieval rejected by the platform on the first question rather than at
// startup.
func (o *RAGFlowOptions) validate() []error {
	if o == nil || !o.Enabled() {
		return nil
	}
	var errs []error
	if o.APIKey == "" {
		errs = append(errs, fmt.Errorf("agent.ragflow.api-key is required when base-url is set"))
	}
	if o.Timeout < 0 {
		errs = append(errs, fmt.Errorf("agent.ragflow.timeout must be >= 0"))
	}
	return errs
}

// validateSkillsDir rejects a skills directory containing path traversal.
// It is deliberately lenient about everything else: the directory does not
// have to exist yet, and a relative path is resolved against the working
// directory by the runtime.
func validateSkillsDir(dir string) error {
	if dir == "" {
		return nil
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(dir), "/") {
		if seg == ".." {
			return fmt.Errorf("agent.skills-dir must not contain %q: %s", "..", dir)
		}
	}
	return nil
}

// CustomCommandOptions bounds the outbound calls http custom commands make.
type CustomCommandOptions struct {
	// AllowedHosts, when non-empty, is the only set of hosts a url target may
	// name. Empty allows any host netguard considers public.
	AllowedHosts []string `json:"allowed-hosts" mapstructure:"allowed-hosts"`
	// AllowPlainHTTP permits http:// url targets.
	AllowPlainHTTP bool `json:"allow-plain-http" mapstructure:"allow-plain-http"`
	// AllowPrivateHosts permits private/loopback targets.
	AllowPrivateHosts bool `json:"allow-private-hosts" mapstructure:"allow-private-hosts"`
	// MaxTimeout caps one invocation.
	MaxTimeout time.Duration `json:"max-timeout" mapstructure:"max-timeout"`
	// MaxResponseBytes caps the response body.
	MaxResponseBytes int64 `json:"max-response-bytes" mapstructure:"max-response-bytes"`
	// MaxRedirects bounds the redirect chain. Zero uses the netguard default.
	MaxRedirects int `json:"max-redirects" mapstructure:"max-redirects"`
}

// AddFlags binds AgentOptions to command-line flags.
func (o *AgentOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Engine, fullPrefix+".engine", o.Engine, "Agent execution engine: eino (the http engine was removed).")
	fs.BoolVar(&o.Sandbox.Enabled, fullPrefix+".sandbox.enabled", o.Sandbox.Enabled,
		"Enable the containerized sandbox.")
	fs.StringVar(&o.Sandbox.DefaultImage, fullPrefix+".sandbox.image", o.Sandbox.DefaultImage,
		"Digest-pinned image used when a sandbox names no template.")
	fs.DurationVar(&o.Sandbox.DefaultTTL, fullPrefix+".sandbox.default-ttl", o.Sandbox.DefaultTTL,
		"Default sandbox lifetime (default 30m).")
	fs.DurationVar(&o.Sandbox.MaxTTL, fullPrefix+".sandbox.max-ttl", o.Sandbox.MaxTTL,
		"Maximum sandbox lifetime (default 4h).")
	fs.StringVar(&o.Sandbox.CPUs, fullPrefix+".sandbox.cpus", o.Sandbox.CPUs, "Sandbox CPU limit, e.g. 0.5.")
	fs.StringVar(&o.Sandbox.Memory, fullPrefix+".sandbox.memory", o.Sandbox.Memory, "Sandbox memory limit, e.g. 512m.")
	fs.IntVar(&o.Sandbox.PidsLimit, fullPrefix+".sandbox.pids-limit", o.Sandbox.PidsLimit, "Sandbox process limit (default 256).")
	fs.StringVar(&o.Sandbox.Network, fullPrefix+".sandbox.network", o.Sandbox.Network, "Sandbox network mode (default none).")
	fs.BoolVar(&o.Runtime.PortEnabled, fullPrefix+".runtime.port-enabled", o.Runtime.PortEnabled,
		"Route runs through the runtime port (off keeps the legacy in-process Engine path).")
	fs.StringVar(&o.Runtime.DefaultProfile, fullPrefix+".runtime.default-profile", o.Runtime.DefaultProfile,
		"Runtime profile used when an agent declares none (empty uses the built-in in-process eino profile).")
	fs.StringVar(&o.Runtime.InternalAddr, fullPrefix+".runtime.internal-addr", o.Runtime.InternalAddr,
		"Listen address for the runtime-only surface (capability MCP/model data plane); default 127.0.0.1:8091, off disables.")
	fs.StringVar(&o.Runtime.TokenKey, fullPrefix+".runtime.token-key", o.Runtime.TokenKey,
		"HMAC key for per-run capability tokens (empty generates a process-local key).")
	fs.StringVar(&o.ChatModel, fullPrefix+".chat-model", o.ChatModel, "Agent chat model name (defaults to llm.model).")
	fs.StringVar(&o.FallbackModel, fullPrefix+".fallback-model", o.FallbackModel, "Fallback model tried when the primary fails after retries.")
	fs.IntVar(&o.MaxRetries, fullPrefix+".max-retries", o.MaxRetries, "Retries per model on transient LLM failures.")
	fs.DurationVar(&o.RetryBackoff, fullPrefix+".retry-backoff", o.RetryBackoff, "Base backoff between LLM retries (exponential).")
	fs.IntVar(&o.MaxConcurrentPerConversation, fullPrefix+".max-concurrent-per-conversation", o.MaxConcurrentPerConversation,
		"Max concurrent AI runs per conversation (1 = single-flight, the default allows several).")
	fs.DurationVar(&o.RunTimeout, fullPrefix+".run-timeout", o.RunTimeout, "Timeout for a single agent run.")
	fs.DurationVar(&o.SessionIdleTTL, fullPrefix+".session-idle-ttl", o.SessionIdleTTL,
		"Close a session after this long without activity (0 disables the worker).")
	fs.DurationVar(&o.SessionIdleSweep, fullPrefix+".session-idle-sweep", o.SessionIdleSweep,
		"How often the idle-session worker scans (default 1m).")
	fs.DurationVar(&o.ToolTimeout, fullPrefix+".tool-timeout", o.ToolTimeout, "Timeout for a single tool invocation.")
	fs.BoolVar(&o.MCPEnabled, fullPrefix+".mcp-enabled", o.MCPEnabled, "Enable tools from MCP servers configured in the resource store.")
	fs.DurationVar(&o.MCPDiscoveryTimeout, fullPrefix+".mcp-discovery-timeout", o.MCPDiscoveryTimeout,
		"Timeout for one MCP server's handshake and tool discovery.")
	fs.DurationVar(&o.MCPCacheTTL, fullPrefix+".mcp-cache-ttl", o.MCPCacheTTL,
		"How long a discovered MCP tool set is cached before being rebuilt.")
	fs.StringSliceVar(&o.CustomCommand.AllowedHosts, fullPrefix+".custom-command.allowed-hosts",
		o.CustomCommand.AllowedHosts,
		"Hosts an http custom command may call; empty allows any public host. Supports a *.example.com suffix.")
	fs.BoolVar(&o.CustomCommand.AllowPlainHTTP, fullPrefix+".custom-command.allow-plain-http",
		o.CustomCommand.AllowPlainHTTP, "Permit http:// (not https) url targets in custom commands.")
	fs.BoolVar(&o.CustomCommand.AllowPrivateHosts, fullPrefix+".custom-command.allow-private-hosts",
		o.CustomCommand.AllowPrivateHosts, "Permit custom commands to reach private/loopback addresses.")
	fs.DurationVar(&o.CustomCommand.MaxTimeout, fullPrefix+".custom-command.max-timeout",
		o.CustomCommand.MaxTimeout, "Upper bound on one custom command invocation.")
	fs.Int64Var(&o.CustomCommand.MaxResponseBytes, fullPrefix+".custom-command.max-response-bytes",
		o.CustomCommand.MaxResponseBytes, "Largest response body an http custom command may return.")
	fs.IntVar(&o.CustomCommand.MaxRedirects, fullPrefix+".custom-command.max-redirects",
		o.CustomCommand.MaxRedirects, "Redirects an http custom command may follow (0 = default).")
	fs.BoolVar(&o.ShowReasoning, fullPrefix+".show-reasoning", o.ShowReasoning, "Surface the model reasoning stream by default.")
	fs.BoolVar(&o.EnableTracing, fullPrefix+".enable-tracing", o.EnableTracing, "Enable OpenTelemetry tracing for agent runs.")
	fs.IntVar(&o.MaxContextTokens, fullPrefix+".max-context-tokens", o.MaxContextTokens,
		"Prompt budget per AI turn; history above it is summarized and a run past 1.5x it is summarized mid-run. Keep it below ~2/3 of the model context window. 0 disables.")
	fs.IntVar(&o.ContextPreserveTokens, fullPrefix+".context-preserve-tokens", o.ContextPreserveTokens,
		"Tokens of newest history kept verbatim when compacting (0 = a third of max-context-tokens).")
	fs.IntVar(&o.MaxImagePixels, fullPrefix+".max-image-pixels", o.MaxImagePixels,
		"Shrink an image larger than this many pixels before showing it to a model (0 = send the original).")
	fs.IntVar(&o.MaxToolOutputChars, fullPrefix+".max-tool-output-chars", o.MaxToolOutputChars,
		"Max characters of a single tool output before its middle is elided (0 disables).")
	fs.StringVar(&o.Description, fullPrefix+".description", o.Description, "Human-readable agent description.")
	fs.BoolVar(&o.UseDeepAgent, fullPrefix+".use-deep-agent", o.UseDeepAgent,
		"Deprecated and ignored: every run uses the deep agent graph.")
	fs.StringVar(&o.SkillsDir, fullPrefix+".skills-dir", o.SkillsDir,
		"Root directory for downloaded skills; empty disables the skill subsystem and the filesystem tools.")
	fs.IntVar(&o.MaxIteration, fullPrefix+".max-iteration", o.MaxIteration, "Max reasoning iterations per run (deep agent).")
	fs.BoolVar(&o.ExecuteSequentially, fullPrefix+".execute-sequentially", o.ExecuteSequentially,
		"Run a batch of tool calls one at a time (forced on when a run can pause for approval).")
	fs.BoolVar(&o.DeepExtrasEnabled, fullPrefix+".deep-extras-enabled", o.DeepExtrasEnabled,
		"Enable the deep agent's built-in write_todos and general sub-agent tools.")
	fs.IntVar(&o.ModelRetryMax, fullPrefix+".model-retry-max", o.ModelRetryMax,
		"Deep agent's own model retry budget (0 disables; the chat client already retries).")
	fs.DurationVar(&o.RunnerCacheTTL, fullPrefix+".runner-cache-ttl", o.RunnerCacheTTL,
		"How long a cached per-agent-set runner is served before a background rebuild (0 disables caching).")
}
