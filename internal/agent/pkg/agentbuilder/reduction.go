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
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/adk"
	adkfilesystem "github.com/cloudwego/eino/adk/filesystem"
	adkfsmiddleware "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/tokenizer"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// offloadDirName is the directory the tool-output reducer writes under, inside
// the agent set's sandbox. It is inside because the sandbox is the only place
// this service may write: SafeLocalBackend refuses every path outside it.
const offloadDirName = ".offload"

// offloadSweepInterval bounds how often old offload files are collected. The
// sweep runs on build, which is once per run without a runner cache, so the
// interval is what keeps it from walking the directory on every turn.
const offloadSweepInterval = time.Hour

// sweepOffload removes offloaded files whose run is older than OffloadTTL.
//
// Deletion is by age rather than by run completion: a run that paused for
// approval may still read its files when it resumes, possibly on another pod,
// so "the run ended" is not a fact this process can rely on. Age is the same
// bound the checkpoint itself has, which is what makes the two consistent —
// past that the pause can no longer be answered either.
func (b *Builder) sweepOffload(isolatedSkillsDir string) {
	if isolatedSkillsDir == "" || b.opts.OffloadTTL <= 0 {
		return
	}

	b.sweepMu.Lock()
	if !b.sweptAt.IsZero() && time.Since(b.sweptAt) < offloadSweepInterval {
		b.sweepMu.Unlock()
		return
	}
	b.sweptAt = time.Now()
	b.sweepMu.Unlock()

	root := filepath.Join(isolatedSkillsDir, offloadDirName)
	cutoff := time.Now().Add(-b.opts.OffloadTTL)

	for _, phase := range []string{"trunc", "clear"} {
		dir := filepath.Join(root, phase)
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A missing directory is the ordinary case before the first
			// offload, and an unreadable one is not worth failing a run over.
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || !entry.IsDir() {
				continue
			}
			if info.ModTime().After(cutoff) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				slog.Warn("[agentbuilder] could not remove an expired offload directory",
					"path", filepath.Join(dir, entry.Name()), "error", err)
			}
		}
	}
}

// reductionHandler bounds the context from the tool side: it truncates an
// oversized tool output the moment it returns (offloading the full text to the
// sandbox) and, before each model call over budget, replaces the oldest tool
// results with placeholders.
//
// It complements the engine's truncation guard rather than replacing it. Both
// apply the same per-output cap, so where the reducer runs first the guard sees
// an already-small message and does nothing; what the reducer adds is the
// second half — a place to put the full text, and a way to shed tool results
// that are still in the context. That half needs a filesystem backend, which is
// why this returns nil without one.
//
// On a deployment whose agent has no skills directory there is no sandbox, so
// the engine's guard is the only bound; losing the capability costs the run
// nothing it had before.
func (b *Builder) reductionHandler(
	ctx context.Context, isolatedSkillsDir string, backend adkfilesystem.Backend,
) adk.ChatModelAgentMiddleware {
	if backend == nil || isolatedSkillsDir == "" {
		return nil
	}
	if b.opts.MaxToolOutputChars <= 0 && b.opts.OffloadClearTokens <= 0 {
		return nil
	}

	root := filepath.Join(isolatedSkillsDir, offloadDirName)
	cfg := &reduction.Config{
		Backend:          backend,
		ReadFileToolName: adkfsmiddleware.ToolNameReadFile,
		RootDir:          root,
		// One directory per phase and per run: a resumed half writes into the
		// directory the paused half used, and collection can delete a whole
		// run's files at once.
		GenTruncOffloadFilePath: func(ctx context.Context, detail *reduction.ToolDetail) (string, error) {
			return runOffloadPath(ctx, root, "trunc", detail), nil
		},
		GenClearOffloadFilePath: func(ctx context.Context, detail *reduction.ToolDetail) (string, error) {
			return runOffloadPath(ctx, root, "clear", detail), nil
		},
		// The value is in bytes (eino clamps the cut to a rune boundary); the
		// engine's guard counts runes. Passing the same number keeps the
		// reducer at or below the guard — bytes are never fewer than runes —
		// so the guard never truncates a second time and destroys the offload
		// notice. The cost is that CJK output is cut earlier than the
		// configured character count suggests; nothing is lost, because the
		// full text is on disk and the notice names the tool that reads it.
		MaxLengthForTrunc: b.opts.MaxToolOutputChars,
		// read_file is how the model retrieves what was offloaded, so its own
		// output must never be truncated — that would make a large file
		// unreadable in exactly the case the reducer exists for.
		TruncExcludeTools:         []string{adkfsmiddleware.ToolNameReadFile},
		MaxTokensForClear:         b.opts.OffloadClearTokens,
		ClearRetentionSuffixLimit: 1,
		// The repository's counter, so the clear threshold means the same thing
		// as the summarizer's trigger it is derived from.
		TokenCounter: func(_ context.Context, msgs []*schema.Message, tools []*schema.ToolInfo) (int64, error) {
			n, err := tokenizer.CountSchemaMessages(msgs, tools)
			return int64(n), err
		},
	}

	mw, err := reduction.New(ctx, cfg)
	if err != nil {
		// The guard still bounds every tool output, so losing the reducer
		// degrades the context budget rather than breaking the run.
		slog.WarnContext(ctx, "[agentbuilder] tool-output reducer disabled", "error", err)
		return nil
	}
	return mw
}

// runOffloadPath returns the file a tool result is offloaded to: one directory
// per run, so a sweep can remove a run's files together and two concurrent runs
// cannot collide on a call ID.
func runOffloadPath(ctx context.Context, root, phase string, detail *reduction.ToolDetail) string {
	runID := agentapi.RunIDFromContext(ctx)
	if runID == "" {
		// No run identity (a handler driven directly, a test): fall through to
		// the shared directory rather than inventing one, so nothing is lost.
		runID = "unknown-run"
	}
	callID := "unknown-call"
	if detail != nil && detail.ToolContext != nil && detail.ToolContext.CallID != "" {
		callID = sanitizeSegment(detail.ToolContext.CallID)
	}
	return filepath.Join(root, phase, sanitizeSegment(runID), callID)
}

// sanitizeSegment keeps a run ID usable as a single path segment. Run IDs are
// UUIDs or caller-supplied strings; a "/" in one would escape the run's
// directory, which is the one thing this path must not allow.
func sanitizeSegment(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unknown-run"
	}
	return string(out)
}
