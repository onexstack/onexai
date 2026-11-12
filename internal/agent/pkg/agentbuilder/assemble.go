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

package agentbuilder

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	adkfilesystem "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/agentsmd"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	adkpatchtoolcalls "github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// maxUnknownToolNamesList caps how many tool names the hallucinated-tool reply
// repeats. The reply is model-visible context, so a run with a hundred MCP
// tools must not spend a paragraph on a name the model just invented.
const maxUnknownToolNamesList = 60

// unknownToolResult answers a call to a tool that does not exist in this run.
//
// The eino tool node fails the whole run when a model calls a name it was never
// given and no handler is configured. That is the one failure the model can
// always repair itself: it invented the name, so naming the real ones is
// usually enough. The reply is ordinary tool output, and the names in it are
// the run's own, so nothing reaches the model that it was not already sent.
//
// It always returns a nil error: a non-nil one fails the run again, which is
// the behaviour the handler exists to avoid.
func unknownToolResult(names []string) func(ctx context.Context, name, input string) (string, error) {
	if len(names) == 0 {
		return func(_ context.Context, toolName, _ string) (string, error) {
			return fmt.Sprintf("unknown tool %q: no tools are available in this run. "+
				"Answer directly instead of calling a tool.", toolName), nil
		}
	}

	available := names
	if len(available) > maxUnknownToolNamesList {
		available = available[:maxUnknownToolNamesList]
	}
	list := strings.Join(available, ", ")
	return func(_ context.Context, toolName, _ string) (string, error) {
		return fmt.Sprintf("unknown tool %q: it does not exist in this run. "+
			"Available tools: %s. Do not call a tool outside this list; "+
			"answer directly if none applies.", toolName, list), nil
	}
}

// danglingToolResult answers a tool call that history says ran but that has no
// result message.
//
// A provider rejects such a history outright, so it can never reach the model.
// The usual cause is a run that was persisted mid-call — a crash, a resumed run
// whose result was never written — and the safest repair is to say plainly that
// the call did not happen, so the model neither assumes its effect nor reports
// it as a finding.
func danglingToolResult(_ context.Context, toolName, _ string, _ *schema.ToolArgument) (
	*adkpatchtoolcalls.PatchedToolResult, error,
) {
	return &adkpatchtoolcalls.PatchedToolResult{
		Content: fmt.Sprintf("tool %q was not executed: its result was never produced. "+
			"Do not assume it ran; call the tool again if the answer still depends on it.", toolName),
	}, nil
}

// assembleTools merges the caller's tools with the knowledge-base tools and
// keeps the first tool seen for each name.
//
// Deduplication is not optional: the chat backend rejects a payload carrying
// two tools with the same name, and the tool set is aggregated from several
// independent sources (built-ins, MCP servers, knowledge bases) that can each
// expose a colliding name. First-seen wins, which puts the caller's
// deliberate choices (built-ins, then MCP) ahead of generated ones.
func (b *Builder) assembleTools(ctx context.Context, req Request) ([]tool.BaseTool, []string) {
	all := make([]tool.BaseTool, 0, len(req.BaseTools)+len(req.KnowledgeBases)+1)
	all = append(all, req.BaseTools...)
	if b.deps.KnowledgeBases != nil && len(req.KnowledgeBases) > 0 {
		all = append(all, b.deps.KnowledgeBases.Tools(ctx, req.KnowledgeBases)...)
	}
	// The drawing tool is unconditional where a knowledge-base tool is not:
	// whether a run may draw is a property of the deployment, while which
	// knowledge bases it may search is a property of the agents it selected.
	if t := newGenerateImageTool(b.deps.ImageGen); t != nil {
		all = append(all, t)
	}
	return dedupeTools(ctx, all)
}

// dedupeTools drops nil tools and every repeat of a name after its first
// occurrence, returning the surviving tools and their names.
//
// The names come back with the tools because reading a tool's Info is not free
// — an MCP tool rebuilds its schema from the wire form, and k8sgpt re-decodes a
// stored one — and the caller wants them for the run log anyway.
func dedupeTools(ctx context.Context, tools []tool.BaseTool) ([]tool.BaseTool, []string) {
	seen := make(map[string]struct{}, len(tools))
	kept := make([]tool.BaseTool, 0, len(tools))
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name == "" {
			slog.WarnContext(ctx, "[agentbuilder] dropping tool with unreadable info", "error", err)
			continue
		}
		if _, dup := seen[info.Name]; dup {
			slog.WarnContext(ctx, "[agentbuilder] duplicate tool name, dropping later occurrence", "tool", info.Name)
			continue
		}
		seen[info.Name] = struct{}{}
		kept = append(kept, t)
		names = append(names, info.Name)
	}
	return kept, names
}

// assembleHandlers builds the middleware chain in its final order.
//
// Ordering rules, all of which matter:
//   - Image extraction runs first so every later handler sees plain text.
//   - The tool-error handler is the outermost tool wrapper, ahead of the
//     caller's, so those still observe the original error (a failed call must
//     still render as a failure) while the model receives recoverable text.
//   - The skill middleware follows the caller's, so it sees the trimmed context.
//   - The tool-output reducer runs before the summarizer, so the cheap,
//     model-free shedding of tool results happens first and the summarizer is
//     only paid for when that was not enough.
//   - The summarizer runs last: it must compress the final context, not an
//     intermediate one that other handlers would have shrunk anyway.
func (b *Builder) assembleHandlers(
	ctx context.Context, req Request, agentKey, isolatedSkillsDir string, backend adkfilesystem.Backend,
	tools []tool.BaseTool, toolNames []string,
) ([]adk.ChatModelAgentMiddleware, error) {
	handlers := make([]adk.ChatModelAgentMiddleware, 0, len(req.Handlers.ToolHandlers)+len(req.Handlers.ModelHandlers)+5)

	// Repairing dangling tool calls comes before every other handler: they run
	// in registration order in BeforeModelRewriteState, and anything that
	// counted or summarized the history first would treat a call that never ran
	// as an established fact.
	patch, err := adkpatchtoolcalls.New(ctx, &adkpatchtoolcalls.Config{
		PatchedToolResultGenerator: danglingToolResult,
	})
	if err != nil {
		return nil, fmt.Errorf("agentbuilder: patch tool calls middleware: %w", err)
	}
	handlers = append(handlers, patch)

	if mw := b.visionHandler(ctx); mw != nil {
		handlers = append(handlers, mw)
	}

	handlers = append(handlers, NewToolErrorHandler())
	handlers = append(handlers, req.Handlers.ToolHandlers...)
	handlers = append(handlers, req.Handlers.ModelHandlers...)

	if b.skillsEnabled(req) {
		mw, err := b.deps.Skills.Middleware(ctx, agentKey, req.Skills)
		if err != nil {
			return nil, fmt.Errorf("agentbuilder: skill middleware for %q: %w", agentKey, err)
		}
		if mw != nil {
			handlers = append(handlers, mw)
		}
	} else if isolatedSkillsDir != "" {
		// The caller configured a skills dir but nothing to put in it; say so
		// once per build rather than silently disarming the sandbox.
		slog.DebugContext(ctx, "[agentbuilder] skills directory configured but no skills resolved",
			"agent_key", agentKey, "isolated_dir", isolatedSkillsDir)
	}

	// Tool search filters the model-visible tool list, so it sits ahead of every
	// handler that reads or counts that list — the summarizer and the reducer
	// would otherwise budget for schemas the model never sees.
	if mw := b.toolSearchHandler(ctx, req, tools, toolNames); mw != nil {
		handlers = append(handlers, mw)
	}

	if system := b.systemPrompt(); system != "" {
		handlers = append(handlers, newInstructionAppendHandler(system))
	}
	// The grounding check comes before the outbound review, and the order is
	// load-bearing: this one appends a caveat to the answer, and the review that
	// follows has to see that caveat — a security review running first would
	// screen a text that is not the one leaving.
	if mw := b.groundingHandler(ctx); mw != nil {
		handlers = append(handlers, mw)
	}
	// The outbound review sits last but for the summarizer: it rewrites the
	// finished answer, so anything that shapes the context must have run first.
	if mw := b.securityHandler(ctx); mw != nil {
		handlers = append(handlers, mw)
	}
	if mw := b.reductionHandler(ctx, isolatedSkillsDir, backend); mw != nil {
		handlers = append(handlers, mw)
	}
	if req.Handlers.SummaryHandler != nil {
		handlers = append(handlers, req.Handlers.SummaryHandler)
	}
	// The repository conventions go after the summarizer so they are injected at
	// call time and never summarized away: the content is transient by design,
	// and a run that paused for an hour would otherwise resume with the file's
	// old copy folded into a summary.
	if mw := b.agentsMDHandler(ctx, backend); mw != nil {
		handlers = append(handlers, mw)
	}
	return handlers, nil
}

// agentsMDHandler installs the AGENTS.md injection, or nil when it is off, has
// nothing to read, or there is no sandbox to read from.
//
// The backend is the same sandbox the filesystem tools use, which is what keeps
// the file inside the run's own directory: the middleware can read nothing the
// agent could not read itself.
func (b *Builder) agentsMDHandler(ctx context.Context, backend adkfilesystem.Backend) adk.ChatModelAgentMiddleware {
	opts := b.opts.AgentsMD
	if !opts.Enabled || backend == nil {
		return nil
	}
	files := opts.Files
	if len(files) == 0 {
		files = []string{DefaultAgentsMDFile}
	}
	mw, err := agentsmd.New(ctx, &agentsmd.Config{
		Backend:             backend,
		AgentsMDFiles:       files,
		AllAgentsMDMaxBytes: opts.MaxBytes,
		PerAgentsMDMaxBytes: opts.PerFileMaxBytes,
	})
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] agents.md middleware init failed", "error", err)
		return nil
	}
	return mw
}

// toolSearchHandler installs the tool-search middleware when the run has enough
// MCP tools for hiding them to pay for itself, or nil otherwise.
//
// Only MCP-published tools are hidden. The built-ins and the tools this builder
// adds stay visible: a tool whose contract is to consult a human is not one to
// put behind a search, and a knowledge-base tool that is only discoverable by
// name is not discoverable at all.
func (b *Builder) toolSearchHandler(
	ctx context.Context, req Request, tools []tool.BaseTool, toolNames []string,
) adk.ChatModelAgentMiddleware {
	dynamic := b.dynamicTools(ctx, req, tools, toolNames)
	if len(dynamic) == 0 {
		return nil
	}
	mw, err := toolsearch.New(ctx, &toolsearch.Config{DynamicTools: dynamic})
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] tool search middleware init failed", "error", err)
		return nil
	}
	return mw
}

// dynamicTools decides which tools tool search hides, and whether it is worth
// installing at all.
//
// The decision is separate from building the middleware so it can be tested
// without a model: the threshold, the MCP-only rule and the always-visible list
// are the whole policy, and all three are easy to get subtly wrong.
func (b *Builder) dynamicTools(
	ctx context.Context, req Request, tools []tool.BaseTool, toolNames []string,
) []tool.BaseTool {
	opts := b.opts.ToolSearch
	if !opts.Enabled || len(req.MCPTools) == 0 {
		return nil
	}

	mcp := make(map[string]struct{}, len(req.MCPTools))
	for _, name := range req.MCPTools {
		mcp[name] = struct{}{}
	}
	always := make(map[string]struct{}, len(opts.AlwaysVisible))
	for _, name := range opts.AlwaysVisible {
		always[name] = struct{}{}
	}

	out := make([]tool.BaseTool, 0, len(tools))
	for i, t := range tools {
		// tools and toolNames are built together by assembleTools, so the
		// index is the name. The guard is for the day one of them changes.
		if i >= len(toolNames) {
			break
		}
		name := toolNames[i]
		if _, isMCP := mcp[name]; !isMCP {
			continue
		}
		if _, keep := always[name]; keep {
			continue
		}
		out = append(out, t)
	}

	minTools := opts.MinTools
	if minTools <= 0 {
		minTools = DefaultToolSearchMinTools
	}
	if len(out) < minTools {
		slog.DebugContext(ctx, "[agentbuilder] tool search not installed",
			"mcp_tools", len(req.MCPTools), "dynamic_tools", len(out), "min_tools", minTools)
		return nil
	}
	return out
}

// securityHandler builds the outbound answer review, or nil when no reviewer
// model is configured.
func (b *Builder) securityHandler(ctx context.Context) adk.ChatModelAgentMiddleware {
	if b.deps.Security == nil {
		return nil
	}
	mw, err := b.deps.Security.Middleware(ctx)
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] security review middleware init failed", "error", err)
		return nil
	}
	return mw
}

// groundingHandler builds the answer-against-evidence check, or nil when no
// model is configured for it.
func (b *Builder) groundingHandler(ctx context.Context) adk.ChatModelAgentMiddleware {
	if b.deps.Grounding == nil {
		return nil
	}
	mw, err := b.deps.Grounding.Middleware(ctx)
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] grounding review middleware init failed", "error", err)
		return nil
	}
	return mw
}

// visionHandler builds the image-extraction middleware, or nil when no vision
// endpoint is configured. A failure to build it is a warning, not a build
// failure: a deployment that loses its vision model should still answer text
// questions.
func (b *Builder) visionHandler(ctx context.Context) adk.ChatModelAgentMiddleware {
	if b.deps.Vision == nil {
		return nil
	}
	mw, err := b.deps.Vision.Middleware(ctx)
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] image extraction middleware init failed", "error", err)
		return nil
	}
	return mw
}

// filesystemBackend builds the sandboxed filesystem backend, or nil when the
// skill subsystem is disabled. A nil backend is what keeps read_file /
// write_file / glob / grep / execute out of the agent's tool set entirely.
//
// The concrete type is returned rather than the filesystem.Backend interface
// on purpose: a nil *SafeLocalBackend assigned to an interface field would be
// a non-nil interface holding a nil pointer, which the deep agent would read
// as "a backend is configured" and then call.
func (b *Builder) filesystemBackend(ctx context.Context, isolatedSkillsDir string) (*SafeLocalBackend, error) {
	if isolatedSkillsDir == "" {
		return nil, nil
	}
	local, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("agentbuilder: local backend: %w", err)
	}
	return &SafeLocalBackend{Local: local, SkillsDir: isolatedSkillsDir}, nil
}
