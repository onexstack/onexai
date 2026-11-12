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
	"encoding/base64"
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

// userRunPrefix marks the run identifiers this surface mints.
//
// It is a different prefix from the management surface's `dbg-` on purpose:
// the two surfaces' approve/stop accept only identifiers they minted, so a
// console user cannot answer a pause inside an administrator's debug run (or
// inside a real chat session), and an administrator's surface cannot be used
// to drive a user's run through a path that does not check ownership.
const userRunPrefix = "usr-"

const (
	// builtinAgent is the one agent name that names no agent row: it selects the
	// engine's builtin assistant (no instruction, tools, MCPs, skills or
	// knowledge bases). The console's smart-create flow uses it for a stateless
	// text generation; named agents keep using the ordinary authorized path.
	builtinAgent = v1.NoAgent

	// maxBuiltinQuestionBytes bounds a builtin-assistant question independently
	// of the client. The console keeps its prompt well below this; the cap is a
	// backstop against a caller that does not.
	maxBuiltinQuestionBytes = 32 << 10
)

// UserAskAgent handles POST /v1/user/agent/ask — an agent run started by an
// ordinary signed-in user, streamed with the same console event shape as the
// management surface's debug run.
//
// # Why it is not the admin endpoint with a different gate
//
// The management surface runs as the **agent's owner**, because an
// administrator debugging an agent has no per-agent grant of their own. A
// normal user is the opposite case: they run as **themselves**, and the run
// gate decides whether they may use that agent. Running as the owner would let
// any user exercise the owner's grants, which is exactly the privilege this
// separation removes.
//
// The two surfaces therefore mint different run-ID prefixes and carry different
// subjects; everything after that (event mapping, sink) is shared.
//
// # The builtin branch
//
// agent "default" names no row. It runs the engine's builtin assistant (no
// instruction, tools, MCPs, skills or knowledge bases), which is what the
// console's smart-create flow needs for a single stateless generation. Those
// calls get a per-request session ID so they neither accumulate history nor
// share a conversation lease with the chat page; named agents keep their
// per-agent/per-user session exactly as before.
//
// # The hostless branch
//
// An empty agent with a composition is the console's playground: the caller is
// the execution subject (quota, audit, model ownership), the selection is empty,
// and the composition is the whole configuration. There is no agent gate and no
// host runtime/memory/delegation/webhook inheritance. ComposeSessionID replaces
// the empty agent with HostlessAgentLabel so the memory bucket is well formed.
// The management surface does not accept this form; it always requires a real
// agent because its contract is "execute as the agent owner".
func (h *Handler) UserAskAgent(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		// Unreachable behind the self-service group's authentication; kept so a
		// mount mistake fails loudly instead of running as nobody.
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the user run surface ran without an authenticated caller"))
		return
	}

	// The group refuses a missing token with 401; this keeps anonymous claims
	// from ever becoming a subject for quota, session, or create ownership.
	if claims.UserID == "" || claims.UserID == "anonymous" {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
			"请先登录后再使用智能创建",
		))
		return
	}

	var req v1.AgentAskRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateUserAskRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	ask, err := validation.ReadConsoleAsk(&req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	agents := ask.Agents // the validator trimmed them
	agentName := ""
	if len(agents) == 1 {
		agentName = agents[0]
	}
	builtin := agentName == builtinAgent
	// Validation allows an empty selection only together with a composition.
	// That is the hostless playground: the caller is the execution subject and
	// the composition is the whole configuration.
	hostless := len(agents) == 0

	subject := agent.Principal{
		ID:   claims.UserID,
		Name: cmp.Or(claims.Name, claims.UserID),
	}
	runID := newUserRunID(claims.UserID)
	composition := agentComposition(ask.Composition)

	// Per agent and per user: two users talking to the same agent do not read
	// each other's memory, and one user's conversations with two agents stay
	// separate as well. A composed run is bucketed per composition and session
	// nonce so two different combinations do not share history. An empty agent
	// is replaced by HostlessAgentLabel inside ComposeSessionID, keeping the
	// bucket stable and well-formed. The builtin branch replaces this with a
	// per-request session; see below.
	sessionID := "usr:" + agentName + ":" + claims.UserID
	if composition != nil {
		sessionID = composeSessionID("usr", agentName, claims.UserID, claims.UserID,
			ask.Composition, ask.SessionNonce)
	}

	// An empty selection is the builtin/standalone form. It must not be
	// []string{""}: the run layer would try to load an agent with an empty name.
	selection := agent.Selection{}
	if !hostless {
		selection = agent.Selection{Agents: agents}
	}

	images, err := imagesFromRequest(nil, ask.Images)
	if err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}

	askReq := &agent.AskRequest{
		SessionID: sessionID,
		Input:     agent.Input{Text: ask.Text, Images: images},
		Generation: agent.GenerationConfig{
			Reasoning: ask.Reasoning,
		},
		Selection:    selection,
		Subject:      subject,
		Actor:        &subject,
		Metering:     agent.MeteringUnspecified,
		RunID:        runID,
		Source:       SourceConsole,
		Capabilities: capabilitySelection(ask.Capabilities),
		Composition:  composition,
	}
	if composition != nil {
		askReq.Capabilities = nil
		askReq.Model = composition.Model
	}

	if builtin {
		// "default" names no row: an empty selection runs the builtin assistant.
		// Capability selection is meaningless there, and silently dropping it
		// would let a caller believe it narrowed a run it did not. Composition
		// is a different form: a hostless composition is expressed by an empty
		// agent, not by the builtin sentinel.
		if hasAnyCapability(ask.Capabilities) {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
				"内置助手不接受能力选择",
			))
			return
		}
		if composition != nil {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
				"内置助手不支持能力组合；无宿主组合调试请省略 agent",
			))
			return
		}
		if len(ask.Text) > maxBuiltinQuestionBytes {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
				"问题过长，最多 %d 字节", maxBuiltinQuestionBytes,
			))
			return
		}

		// Per request, not per user: smart-create is stateless and must not
		// accumulate conversation history or share a concurrency lease with the
		// chat page's builtin conversations.
		askReq.SessionID = "usr:" + builtinAgent + ":" + claims.UserID + ":" + runID
		askReq.Selection = agent.Selection{}
		askReq.Capabilities = nil
		askReq.Composition = nil
		askReq.Options = agent.ExecutionOptions{
			Approval: agent.Approval{Channel: agent.ApprovalChannelNone},
		}

		// The builtin branch is new browser-reachable text generation, so screen
		// it before the model is called. Named agents keep their existing path.
		if verdict, blocked := h.blockedByAudit(c, BlockedInput{
			Source:     SourceConsole,
			SessionKey: askReq.SessionID,
			SenderID:   claims.UserID,
			SenderName: subject.Name,
		}, ask.Text); blocked {
			core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", verdict.UserMessage()))
			return
		}
	}

	if hostless {
		// A hostless run is another browser-reachable surface, and unlike a
		// named agent it can compose arbitrary MCP tools. Screen the question
		// before the model is called, exactly as the builtin branch does.
		if verdict, blocked := h.blockedByAudit(c, BlockedInput{
			Source:     SourceConsole,
			SessionKey: askReq.SessionID,
			SenderID:   claims.UserID,
			SenderName: subject.Name,
		}, ask.Text); blocked {
			core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", verdict.UserMessage()))
			return
		}
	}

	events, err := h.biz.Agent().Ask(h.askContext(c), askReq)
	if err != nil {
		writeRunError(c, err)
		return
	}

	streamConsoleSSE(c, askReq.RunID, events)
}

// UserApproveAgent handles POST /v1/user/agent/approve — answers a pause on a
// user's run and streams the continuation.
func (h *Handler) UserApproveAgent(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the user run surface ran without an authenticated caller"))
		return
	}

	var req v1.AgentApproveRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentApproveRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !ownsUserRun(req.GetRunID(), claims.UserID) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
			"run %q is not a run this caller started", req.GetRunID(),
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

// UserStopAgent handles POST /v1/user/agent/stop — cancels a run the caller
// started.
func (h *Handler) UserStopAgent(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil,
			errno.ErrInternal.WithMessage("the user run surface ran without an authenticated caller"))
		return
	}

	var req v1.AgentStopRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentStopRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !ownsUserRun(req.GetRunID(), claims.UserID) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage(
			"run %q is not a run this caller started", req.GetRunID(),
		))
		return
	}

	if err := h.biz.Agent().Stop(c.Request.Context(), req.GetRunID()); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, &v1.AgentStopResponse{}, nil)
}

// hasAnyCapability reports whether the request actually names a capability.
// A non-nil but empty selection narrows nothing, so it is accepted on the
// builtin branch rather than rejected for shape alone.
func hasAnyCapability(caps *v1.CapabilitySelection) bool {
	if caps == nil {
		return false
	}
	return len(caps.GetSkills()) > 0 ||
		len(caps.GetMcps()) > 0 ||
		len(caps.GetKnowledgeBases()) > 0 ||
		len(caps.GetCustomCommands()) > 0
}

// newUserRunID mints a run identifier carrying its owner.
//
// The owner is encoded rather than looked up because approve/stop are stateless:
// they receive a run ID and nothing else, and a run registry would forget its
// entries on restart — turning "answer the pause you just saw" into "the run
// does not exist". base64url has no separator, so the first dot is an
// unambiguous boundary even though user IDs may contain dashes.
func newUserRunID(userID string) string {
	owner := base64.RawURLEncoding.EncodeToString([]byte(userID))
	return userRunPrefix + owner + "." + uuid.NewString()
}

// ownsUserRun reports whether runID was minted for userID.
func ownsUserRun(runID, userID string) bool {
	rest, ok := strings.CutPrefix(runID, userRunPrefix)
	if !ok {
		return false
	}
	encoded, _, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	return string(raw) == userID
}

func init() {
	RegisterSelfService(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		run := engine.Group("/user/agent")
		run.POST("/ask", h.UserAskAgent)
		run.POST("/approve", h.UserApproveAgent)
		run.POST("/stop", h.UserStopAgent)
	})
}
