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
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	"github.com/onexstack/onexai/internal/agent/pkg/agentstream"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// AskAgent handles POST /v1/agent/ask — a backend-neutral agent run streamed
// over SSE. Unlike the OpenAI-compatible /v1/chat/completions, it forwards the
// full event stream (reasoning, tool steps, interrupts) so the chat can
// drive a Lark card.
func (h *Handler) AskAgent(c *gin.Context) {
	var req v1.AgentAskRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentAskRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	// Default filled the run id when the caller omitted it.
	runID := req.GetRunId()

	input := req.GetInput()
	subject := req.GetSubject()
	// A request authenticated by an Agent-level TokenReview Webhook was not
	// signed by the caller, so the identity assertions in the body are not
	// trustworthy. Replace them with the identity the webhook proved; the
	// fields are otherwise accepted because the signed face's signature covers
	// them.
	if authnMethod, _ := method.From(c.Request.Context()); method.IsWebhook(authnMethod) {
		if claims, ok := bearer.ClaimsFrom(c); ok {
			subject = &v1.Principal{Id: claims.UserID, Name: claims.Name}
			req.Actor = nil
		}
	}

	// Screened before anything is created and before a single token is spent:
	// a refused message must cost nothing and leave no side effect behind.
	if verdict, blocked := h.blockedByAudit(c, BlockedInput{
		Source:     SourceChat,
		SessionKey: req.GetSessionId(),
		SenderID:   subject.GetId(),
		SenderName: subject.GetName(),
	}, input.GetText()); blocked {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", verdict.UserMessage()))
		return
	}

	images, err := imagesFromRequest(nil, input.GetImages())
	if err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}

	askReq := &agent.AskRequest{
		SessionID: req.GetSessionId(),
		RunID:     runID,
		Input: agent.Input{
			Text:   input.GetText(),
			Images: images,
		},
		Selection: agent.Selection{
			Agents:        req.GetSelection().GetAgents(),
			InvokedSkills: req.GetSelection().GetInvokedSkills(),
		},
		Model:      req.GetModel(),
		Generation: generationFromProto(req.GetGeneration()),
		Subject:    agent.Principal{ID: subject.GetId(), Name: subject.GetName()},
		Actor:      principalFromProto(req.GetActor()),
		Options:    executionOptionsFromProto(req.GetOptions()),
		Metering:   meteringFromProto(req.GetMetering().GetMode()),
		Metadata:   req.GetMetadata(),
		Source:     SourceChat,
	}

	events, err := h.biz.Agent().Ask(h.askContext(c), askReq)
	if err != nil {
		writeRunError(c, err)
		return
	}

	streamSSE(c, runID, events)
}

// writeRunError maps a failure to start or resume a run onto the status the
// caller can act on. It is shared by the signed face and the management surface
// so that one refusal does not read as two different things depending on which
// endpoint served it.
//
// Nothing has been written to the response when this is called: the stream has
// not been committed, so the status is still ours to choose.
func writeRunError(c *gin.Context, err error) {
	// A conversation that is still busy is a conflict, not a server fault:
	// 500 would tell the client to report a failure, while 409 tells it the
	// one thing it can act on — that a run is in flight and this request
	// should be tried again.
	if errors.Is(err, agentbiz.ErrConversationBusy) {
		core.WriteResponse(c, nil, errno.ErrOperationFailed.WithMessage("%s", err.Error()))
		return
	}
	// The caller is allowed to ask, just not now: 429 rather than 403.
	if errors.Is(err, agentbiz.ErrQuotaExceeded) {
		core.WriteResponse(c, nil, errno.ErrTooManyRequests.WithMessage("%s", err.Error()))
		return
	}
	// A user-configured endpoint that exists but cannot be used. The message is
	// the sentinel's static text: the endpoint name is logged, not returned.
	if errors.Is(err, agentbiz.ErrModelDisabled) || errors.Is(err, agentbiz.ErrModelUnavailable) {
		core.WriteResponse(c, nil, errno.ErrOperationFailed.WithMessage("%s", err.Error()))
		return
	}
	if errors.Is(err, agentbiz.ErrModelForbidden) {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", err.Error()))
		return
	}
	// A run-lifecycle hook refused the run for a reason that is not about
	// allowance: the caller may not, rather than not right now. The hook's own
	// message is the body — it is the business reason, written by whoever
	// administers the hook, and which endpoint said it is an operational detail.
	var refused *agentbiz.RunRefusedError
	if errors.As(err, &refused) {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", refused.Message))
		return
	}
	// A pause that is no longer waiting means ask the run again rather than try
	// shortly, which is why it is 410 and not 409.
	if errors.Is(err, agentbiz.ErrNotPending) {
		core.WriteResponse(c, nil, errno.ErrApprovalNotPending.WithMessage("%s", err.Error()))
		return
	}
	// A refusal is the caller's to act on, not a server fault: it is the
	// one answer that tells them which agent they may not use, and 403
	// says that without inviting a bug report.
	var denied *authz.ErrAgentAccessDenied
	if errors.As(err, &denied) {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", denied.Error()))
		return
	}
	// A capability selection naming something the agent does not have is also
	// the caller's to fix. The message names the rejected capabilities, because
	// "400" alone leaves the person comparing two lists by eye.
	var capErr *agentbiz.CapabilitySelectionError
	if errors.As(err, &capErr) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", capErr.Error()))
		return
	}
	// A composition naming something that cannot be used is the same kind of
	// caller-fixable problem. The message names the rejected resources, which
	// is the only way a person can tell a stale menu from a server fault.
	var compErr *agentbiz.CompositionError
	if errors.As(err, &compErr) {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", compErr.Error()))
		return
	}
	core.WriteResponse(c, nil, err)
}

// ApproveAgent handles POST /v1/agent/approve — answers a run paused on a
// human-approval interrupt and streams the run's continuation.
//
// The response is the same SSE stream as /agent/ask, because answering resumes
// the run: the tools it paused on execute now, and whatever the model says and
// does afterwards reaches the caller the same way it did the first time.
func (h *Handler) ApproveAgent(c *gin.Context) {
	var req v1.AgentApproveRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentApproveRequest); err != nil {
		core.WriteResponse(c, nil, err)
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

	streamSSE(c, req.GetRunID(), events)
}

// streamSSE commits the response to an event stream and pumps events into it,
// in the shape the chat service reads (AgentStreamEvent, numeric type).
func streamSSE(c *gin.Context, runID string, events <-chan agent.Event) {
	streamWithSink(c, runID, events, false)
}

// streamConsoleSSE is the same stream in the shape the management console reads
// (string-typed events). See agentstream.NewConsoleSSESink for why the two
// shapes exist rather than one.
func streamConsoleSSE(c *gin.Context, runID string, events <-chan agent.Event) {
	streamWithSink(c, runID, events, true)
}

// streamWithSink commits the response to an event stream and pumps events into
// it. Once the headers are written the status is no longer ours to change, so a
// failure after this point can only be logged.
func streamWithSink(c *gin.Context, runID string, events <-chan agent.Event, console bool) {
	if err := openSSE(c); err != nil {
		slog.WarnContext(c.Request.Context(),
			"could not clear the SSE connection deadline; a run longer than the server timeout may be cut off",
			"error", err)
	}

	var sink agentstream.StreamSink
	if console {
		sink = agentstream.NewConsoleSSESink(c.Writer, c.Writer, runID)
	} else {
		sink = agentstream.NewJSONSSESink(c.Writer, c.Writer, runID)
	}
	if err := agentstream.Pump(c.Request.Context(), events, sink); err != nil {
		slog.WarnContext(c.Request.Context(), "could not write the agent event stream",
			"run", runID, "error", err)
	}
}

// StopAgent handles POST /v1/agent/stop — cancels an in-flight agent run. It is
// the server-side counterpart of the streaming card's "停止生成" (Stop) button.
func (h *Handler) StopAgent(c *gin.Context) {
	var req v1.AgentStopRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateAgentStopRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.Agent().Stop(c.Request.Context(), req.GetRunID()); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, &v1.AgentStopResponse{}, nil)
}

// detachConnDeadlines clears the connection's read and write deadlines for the
// rest of this request.
//
// The server's ReadTimeout/WriteTimeout are one absolute deadline for the whole
// request, and net/http closes the connection on the first write past it. That
// is the right bound for a normal request and the wrong one for SSE: a run is
// bounded by agent.run-timeout, which is longer than the HTTP timeout, so the
// connection would be severed mid-stream and the client would read a truncated
// body — io.ErrUnexpectedEOF, which surfaces to the caller as the bare and
// uninformative "unexpected EOF". Clearing the read deadline matters too: a
// ReadTimeout expiry cancels the request context.
//
// Best-effort. A writer that cannot report its connection (httptest.Recorder)
// returns an error, and the caller carries on with the server's default.
func detachConnDeadlines(w http.ResponseWriter) error {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	return rc.SetWriteDeadline(time.Time{})
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		agentGroup := engine.Group("/agent")
		agentGroup.POST("/ask", h.AskAgent)
		agentGroup.POST("/approve", h.ApproveAgent)
		agentGroup.POST("/stop", h.StopAgent)
	})
}
