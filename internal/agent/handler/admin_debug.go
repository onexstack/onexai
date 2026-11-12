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

package handler

import (
	"cmp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/agent/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// debugRunPrefix marks the run identifiers this surface mints.
//
// # Why a prefix rather than a registry
//
// /approve and /stop take a run identifier and nothing else, and the resume a
// stop or an answer drives executes tools. On the signed face that is safe
// enough: the caller is a trusted service, and a run it names is one it has a
// reason to name. On this face the caller is a browser, and the identifiers in
// the deployment also belong to real users' conversations — answering a pause
// there would mean approving a tool call inside somebody else's session.
//
// So the boundary is drawn by refusing any identifier this surface did not
// mint. A prefix is what makes that checkable without state: the only way a
// `dbg-` identifier exists is that AdminAskAgent created it, because the
// identifier is minted there and never taken from the request. A registry would
// answer the same question and then forget it on restart, which would read as
// "the run you just started does not exist".
const debugRunPrefix = "dbg-"

// AdminAskAgent handles POST /v1/admin/agent/ask — a run started from the
// management surface, streamed the same way the signed face streams one.
//
// # Two identities, and why they are not one
//
// A debug run carries two identities:
//
//   - the **subject** the run executes as. Authorization evaluates it against
//     the workspace and agent gates, and the tools the run calls attribute
//     their work to it. It is the agent's owner. An administrator has no
//     per-agent rule of their own — the platform provisions one rule per user,
//     naming the agent that user may run — so a run evaluated as the
//     administrator is refused by the agent gate before the model is reached.
//   - the **actor**, which is the administrator who started it. It is what the
//     audit row records (see interactionlog.Entry), because "who ran this" is
//     the fact the record exists for.
//
// Keeping them apart is the whole design: the subject is an implementation
// detail of authorization, the actor is accountability. A handler that
// collapsed them would either break the feature (the administrator is refused)
// or the record (the agent's owner is blamed for the administrator's run).
//
// The agent is required here. A hostless playground run has no owner to execute
// as, so it belongs to the self-service surface (`/v1/user/agent/ask`), where
// the caller is the subject by construction. Accepting it here would make one
// message carry two different identity rules.
func (h *Handler) AdminAskAgent(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		// Unreachable behind the admin group's middleware, which refuses a
		// caller with no claims before this runs. Kept because a handler that
		// assumes its middleware ran is one mount away from reading a nil
		// identity, and this one records who acted.
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the management surface ran without an authenticated caller"))
		return
	}

	var req v1.AgentAskRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAdminAskRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	ask, err := validation.ReadConsoleAsk(&req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	agentName := ask.Agents[0]

	// The subject the run executes as is the agent's owner. An administrator has
	// no per-agent rule of their own, so a run evaluated as the administrator is
	// refused by the agent gate before the model is reached. See the method
	// comment for the two identities.
	owner, err := h.biz.AgentV1().Owner(c.Request.Context(), agentName)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	// Minted, not read from the request: see debugRunPrefix.
	runID := debugRunPrefix + uuid.NewString()

	images, err := imagesFromRequest(nil, ask.Images)
	if err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}

	composition := agentComposition(ask.Composition)
	// The memory bucket is per agent *and* per administrator, so two
	// administrators debugging the same agent do not read each other's turns.
	// A composed run gets its own bucket per composition and session nonce:
	// two different combinations must not share history. Upstream memory is
	// bucketed by this key and by nothing else.
	sessionID := "dbg:" + agentName + ":" + claims.UserID
	if composition != nil {
		sessionID = composeSessionID("dbg", agentName, owner, claims.UserID,
			ask.Composition, ask.SessionNonce)
	}

	askReq := &agent.AskRequest{
		SessionID: sessionID,
		Input:     agent.Input{Text: ask.Text, Images: images},
		Generation: agent.GenerationConfig{
			Reasoning: ask.Reasoning,
		},
		// Named explicitly. An empty set is not "the default agent" upstream —
		// it is no agent at all, and that run has no system prompt, no
		// knowledge base and no tools.
		Selection: agent.Selection{Agents: []string{agentName}},
		// The subject: the agent's owner, for the reason in the method comment.
		Subject: agent.Principal{ID: owner, Name: owner},
		// The token carries a display name only if its login provider reported
		// one; an identifier is a worse name than a person's, but a better one
		// than an empty string in a column whose purpose is to say who asked.
		Actor:        &agent.Principal{ID: claims.UserID, Name: cmp.Or(claims.Name, claims.UserID)},
		Metering:     agent.MeteringNone,
		RunID:        runID,
		Source:       SourceConsole,
		Capabilities: capabilitySelection(ask.Capabilities),
		Composition:  composition,
	}
	if composition != nil {
		// The composition is a replacement, never a narrowing. Validation
		// rejects both, and this keeps the run consistent even if a future
		// caller bypasses it.
		askReq.Capabilities = nil
		askReq.Model = composition.Model
	}

	events, err := h.biz.Agent().Ask(h.askContext(c), askReq)
	if err != nil {
		writeRunError(c, err)
		return
	}

	streamConsoleSSE(c, runID, events)
}

// AdminApproveAgent handles POST /v1/admin/agent/approve — answers a pause on a
// debug run and streams the continuation.
func (h *Handler) AdminApproveAgent(c *gin.Context) {
	if _, ok := bearer.ClaimsFrom(c); !ok {
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the management surface ran without an authenticated caller"))
		return
	}

	var req v1.AgentApproveRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentApproveRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !isDebugRun(req.GetRunID()) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
			"run %q is not a debug run; this surface answers only the runs it started", req.GetRunID(),
		))
		return
	}

	events, err := h.biz.Agent().Approve(c.Request.Context(), &agent.ApproveRequest{
		RunID:     req.GetRunID(),
		RequestID: req.GetRequestID(),
		Decision:  req.GetDecision(),
		Answers:   req.GetAnswers(),
		Reason:    req.GetReason(),
	})
	if err != nil {
		writeRunError(c, err)
		return
	}

	streamConsoleSSE(c, req.GetRunID(), events)
}

// AdminStopAgent handles POST /v1/admin/agent/stop — cancels a debug run.
func (h *Handler) AdminStopAgent(c *gin.Context) {
	if _, ok := bearer.ClaimsFrom(c); !ok {
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the management surface ran without an authenticated caller"))
		return
	}

	var req v1.AgentStopRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentStopRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !isDebugRun(req.GetRunID()) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
			"run %q is not a debug run; this surface stops only the runs it started", req.GetRunID(),
		))
		return
	}

	if err := h.biz.Agent().Stop(c.Request.Context(), req.GetRunID()); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, &v1.AgentStopResponse{}, nil)
}

// capabilitySelection maps the request's narrowing, preserving presence.
//
// A nil message is the full effective set; a present message is the exact set
// to run, including the case where every list is empty. Validation happens
// after the selection is resolved, against the same records the run will use:
// checking here through a second read would introduce a gap between the answer
// and the run instead of closing one.
func capabilitySelection(in *v1.CapabilitySelection) *agent.CapabilitySelection {
	if in == nil {
		return nil
	}
	return &agent.CapabilitySelection{
		Skills:         in.GetSkills(),
		MCPs:           in.GetMcps(),
		KnowledgeBases: in.GetKnowledgeBases(),
		CustomCommands: in.GetCustomCommands(),
	}
}

// isDebugRun reports whether runID names a run this surface started.
func isDebugRun(runID string) bool {
	return strings.HasPrefix(runID, debugRunPrefix)
}

func init() {
	RegisterAdmin(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// /agent, not /agents: the run surface and the resource surface are
		// different things and the signed face already keeps them apart the same
		// way (/v1/agents vs /v1/agent/ask).
		run := engine.Group("/agent")
		run.POST("/ask", h.AdminAskAgent)
		run.POST("/approve", h.AdminApproveAgent)
		run.POST("/stop", h.AdminStopAgent)
	})
}
