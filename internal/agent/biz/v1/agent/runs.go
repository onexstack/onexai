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
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/pkg/revision"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// startRun records the run before the engine is handed it. Recording is
// best-effort: a store that is away must not prevent an answer, but the
// failure is logged because it is the difference between a run that can be
// inspected later and one that cannot.
func (b *AgentBiz) startRun(
	ctx context.Context, req *agentapi.AskRequest, sel resolvedSelection,
	runtimeKind, instruction, modelName string,
) {
	if b.deps.Runs == nil {
		return
	}
	revisionID := b.ensureRevision(ctx, sel, runtimeKind, instruction, modelName)
	b.ensureSession(ctx, req, sel, runtimeKind, revisionID)
	actor := req.Subject.ID
	if req.Actor != nil && req.Actor.ID != "" {
		actor = req.Actor.ID
	}
	run := &model.RunM{
		ID:         req.RunID,
		SessionID:  req.SessionID,
		RevisionID: revisionID,
		Subject:    req.Subject.ID,
		Actor:      actor,
		Runtime:    runtimeKind,
		RequestID:  req.RunID,
		State:      model.RunStateRunning,
		StartedAt:  time.Now().UnixMilli(),
	}
	if err := b.deps.Runs.CreateRun(ctx, run); err != nil {
		slog.WarnContext(ctx, "could not record agent run", "run", req.RunID, "error", err)
	}
}

// pinnedRuntime returns the profile the session was created on. An existing
// session keeps its runtime even if the agent's spec has moved: the runtime's
// context/task state belongs to the old profile, and silently switching would
// lose the conversation.
func (b *AgentBiz) pinnedRuntime(ctx context.Context, sessionID string) (string, bool) {
	if b.deps.Runs == nil || sessionID == "" {
		return "", false
	}
	session, err := b.deps.Runs.GetSession(ctx, sessionID)
	if err != nil || session.RuntimeProfile == "" {
		return "", false
	}
	return session.RuntimeProfile, true
}

// ensureRevision compiles the run's inputs and returns the revision id. The
// same input yields the same digest and id, so the second run of a session
// reuses the row rather than growing the table.
func (b *AgentBiz) ensureRevision(
	ctx context.Context, sel resolvedSelection, runtimeKind, instruction, modelName string,
) string {
	if b.deps.Runs == nil {
		return ""
	}
	profile, _ := sel.set.runtimeProfile()
	compiled, err := revision.Compile(revision.Input{
		Namespace:      revisionNamespace(sel),
		Agents:         sel.set.names,
		Model:          modelName,
		Instruction:    instruction,
		MCPs:           sel.spec.MCPs,
		Skills:         sel.spec.Skills,
		KnowledgeBases: sel.spec.KnowledgeBases,
		Approval:       sel.spec.Approval,
		CustomCommands: sel.spec.CustomCommands,
		Webhooks:       sel.spec.Webhooks,
		OutputSchema:   sel.spec.OutputSchema,
		OutputStrict:   sel.spec.OutputSchemaStrict,
		MemoryEnabled:  sel.spec.MemoryEnabled,
		MemoryModel:    sel.spec.MemoryEmbeddingModel,
		RuntimeProfile: profile,
		RuntimeKind:    runtimeKind,
	})
	if err != nil {
		slog.WarnContext(ctx, "could not compile agent revision", "error", err)
		return ""
	}
	agentName := ""
	if len(sel.set.names) > 0 {
		agentName = sel.set.names[0]
	}
	if existing, err := b.deps.Runs.GetRevisionByDigest(ctx, agentName, compiled.Digest); err == nil {
		return existing.ID
	}
	row := &model.RevisionM{
		ID:        compiled.ID,
		Namespace: revisionNamespace(sel),
		Agent:     agentName,
		Digest:    compiled.Digest,
		SpecJSON:  compiled.Spec,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := b.deps.Runs.CreateRevision(ctx, row); err != nil {
		slog.WarnContext(ctx, "could not record agent revision", "agent", agentName, "error", err)
		return ""
	}
	return compiled.ID
}

func revisionNamespace(sel resolvedSelection) string {
	for _, record := range sel.set.records {
		if record != nil && record.Namespace != "" {
			return record.Namespace
		}
	}
	return ""
}

// ensureSession creates the session row on first use. A session is pinned to
// the runtime and profile it started on; changing either requires a new
// session, which is why this row records both.
func (b *AgentBiz) ensureSession(
	ctx context.Context, req *agentapi.AskRequest, sel resolvedSelection, runtimeKind, revisionID string,
) {
	if b.deps.Runs == nil || req.SessionID == "" {
		return
	}
	if _, err := b.deps.Runs.GetSession(ctx, req.SessionID); err == nil {
		return
	}
	agentName := ""
	if len(sel.set.names) > 0 {
		agentName = sel.set.names[0]
	}
	profile, _ := sel.set.runtimeProfile()
	now := time.Now().UnixMilli()
	session := &model.SessionM{
		ID:             req.SessionID,
		Agent:          agentName,
		RuntimeKind:    runtimeKind,
		RuntimeProfile: profile,
		Owner:          req.Subject.ID,
		RevisionID:     revisionID,
		State:          model.SessionStateReady,
		LastActiveAt:   now,
		CreatedAt:      now,
	}
	if err := b.deps.Runs.CreateSession(ctx, session); err != nil {
		slog.WarnContext(ctx, "could not record agent session", "session", req.SessionID, "error", err)
	}
}

// recordApprovals persists one row per pending call, so the approval center can
// list and expire them without reading a Redis checkpoint.
func (b *AgentBiz) recordApprovals(ctx context.Context, sp suspend, it *agentapi.Interrupt) {
	if b.deps.Runs == nil || it == nil {
		return
	}
	ttl := b.deps.ApprovalTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now()
	rows := make([]*model.ApprovalM, 0, len(it.Requests))
	for _, request := range it.Requests {
		rows = append(rows, &model.ApprovalM{
			ID:        approvalID(sp.RunID, it.RequestID, request.Address),
			RunID:     sp.RunID,
			RequestID: it.RequestID,
			SessionID: sp.SessionID,
			Subject:   sp.UserID,
			Agent:     firstAgentName(sp.Agents),
			Tool:      request.ToolName,
			Args:      request.Arguments,
			Status:    "pending",
			ExpiresAt: now.Add(ttl).UnixMilli(),
			CreatedAt: now.UnixMilli(),
			UpdatedAt: now.UnixMilli(),
		})
	}
	if err := b.deps.Runs.CreateApprovals(ctx, rows); err != nil {
		slog.WarnContext(ctx, "could not record pending approvals", "run", sp.RunID, "error", err)
	}
}

// decideApproval closes the rows answered by this approval.
func (b *AgentBiz) decideApproval(ctx context.Context, req *agentapi.ApproveRequest) {
	if b.deps.Runs == nil {
		return
	}
	// The row has to say what the run did, and the engine runs the tool for
	// every decision except an explicit deny (see resumeTargets). Recording
	// "denied" for an answer the model went on to use — a question interrupt
	// carries no decision at all — would make the approval center disagree with
	// the transcript.
	status := "approved"
	if req.Decision == agentapi.DecisionDeny {
		status = "denied"
	}
	if err := b.deps.Runs.DecideApproval(ctx, req.RunID, req.RequestID, status, req.Reason); err != nil {
		slog.WarnContext(ctx, "could not close pending approvals", "run", req.RunID, "error", err)
	}
}

func approvalID(runID, requestID, address string) string {
	sum := sha1.Sum([]byte(runID + "|" + requestID + "|" + address))
	return "ap-" + hex.EncodeToString(sum[:])
}

func firstAgentName(agents []string) string {
	if len(agents) == 0 {
		return ""
	}
	return agents[0]
}

// commitRunState updates a non-terminal run in place, preserving the fields the
// store's version check needs.
func (b *AgentBiz) commitRunState(ctx context.Context, runID, state, logMessage string) {
	if b.deps.Runs == nil || runID == "" {
		return
	}
	run, err := b.deps.Runs.GetRun(ctx, runID)
	if err != nil {
		return
	}
	if err := b.deps.Runs.UpdateRun(ctx, &model.RunM{
		ID: run.ID, State: state, TaskID: run.TaskID,
		Error: run.Error, UsageJSON: run.UsageJSON,
	}, run.Version); err != nil {
		slog.WarnContext(ctx, logMessage, "run", runID, "error", err)
	}
}

// markRunRunning is the resume half: the row already exists from the paused
// half, and its state moves back to running.
func (b *AgentBiz) markRunRunning(ctx context.Context, runID string) {
	b.commitRunState(ctx, runID, model.RunStateRunning, "could not mark resumed run running")
}

// markRunSuspended records a pause. The run is not terminal, so it is updated
// in place rather than settled.
func (b *AgentBiz) markRunSuspended(ctx context.Context, runID string) {
	b.commitRunState(ctx, runID, model.RunStateInterrupted, "could not record run suspension")
}

// finishRun settles the run and publishes its terminal event atomically.
func (b *AgentBiz) finishRun(ctx context.Context, runID, answer, status, errMessage string, usage *agentapi.Usage) {
	if b.deps.Runs == nil || runID == "" {
		return
	}
	// Settling must happen even when the run was stopped or timed out, and the
	// context that carried the stream is exactly the one cancelled at that
	// moment. A write on it fails silently, leaving the row running forever and
	// the session uncollectable; detach it the way the invocation record and
	// the interaction log already do.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	run, err := b.deps.Runs.GetRun(ctx, runID)
	if err != nil {
		return
	}
	state := runStateFor(status)
	payload, _ := json.Marshal(map[string]any{
		"status": status,
		"answer": answer,
		"error":  errMessage,
	})
	event := &model.RunEventM{
		Type:      "final",
		Payload:   string(payload),
		CreatedAt: time.Now().UnixMilli(),
	}
	if usage != nil {
		run.UsageJSON = encodeUsage(usage)
	}
	run.State = state
	run.Error = errMessage
	run.FinishedAt = time.Now().UnixMilli()
	if err := b.deps.Runs.SettleRun(ctx, run, run.Version, []*model.RunEventM{event}); err != nil {
		slog.WarnContext(ctx, "could not settle agent run", "run", runID, "error", err)
	}
	if session, err := b.deps.Runs.GetSession(ctx, run.SessionID); err == nil {
		session.LastActiveAt = run.FinishedAt
		if err := b.deps.Runs.UpdateSession(ctx, session); err != nil {
			slog.WarnContext(ctx, "could not update session activity", "session", session.ID, "error", err)
		}
	}
}

func runStateFor(status string) string {
	switch status {
	case interactionlog.StatusSuccess:
		return model.RunStateCompleted
	case interactionlog.StatusStopped:
		return model.RunStateCanceled
	default:
		return model.RunStateFailed
	}
}

func encodeUsage(usage *agentapi.Usage) string {
	if usage == nil {
		return ""
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(encoded)
}
