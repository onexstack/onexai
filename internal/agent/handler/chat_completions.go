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
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/agent/pkg/agentstream"
	"github.com/onexstack/onexai/internal/agent/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ChatCompletions handles POST /v1/chat/completions — the OpenAI-compatible
// completion endpoint. It supports both synchronous JSON and SSE streaming.
func (h *Handler) ChatCompletions(c *gin.Context) {
	var req v1.ChatCompletionRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateChatCompletionRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	question, images, _ := lastUserMessage(req.GetMessages())

	// The validator checked that the agent resolves and Default filled the
	// session id; the rest of the handler uses the request as delivered.
	sessionID := req.GetSessionID()

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	// Screened before the run starts, for the same reason as the chat surface:
	// a refused message must cost nothing.
	if verdict, blocked := h.blockedByAudit(c, BlockedInput{
		Source: SourceAPI, SessionKey: sessionID, SenderID: req.GetSenderID(),
	}, question); blocked {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("%s", verdict.UserMessage()))
		return
	}

	// Only v1.NoAgent — the explicit request for the engine's builtin assistant
	// — yields an empty selection. An empty name never reaches here:
	// ChatCompletions rejects it, and the caller's `model` is a model endpoint,
	// not a stand-in for an agent.
	agents := []string{req.GetAgent()}
	if req.GetAgent() == v1.NoAgent {
		agents = nil
	}

	askReq := &agent.AskRequest{
		SessionID: sessionID,
		Input:     agent.Input{Text: question, Images: images},
		Subject:   agent.Principal{ID: req.GetSenderID()},
		Selection: agent.Selection{Agents: agents},
		// The requested endpoint is preferred over the agent's own model. A name
		// that matches no row is treated as "not configured" rather than an error
		// (see resolveRequestedModel), so a stock OpenAI client's model name falls
		// through to the agent's model and then the deployment default.
		Model:    req.GetModel(),
		Metering: agent.MeteringRuntime,
		Options: agent.ExecutionOptions{
			Approval: agent.Approval{Channel: agent.ApprovalChannelNone},
		},
		Source: SourceAPI,
	}

	events, err := h.biz.Agent().Ask(h.askContext(c), askReq)
	if err != nil {
		// The same mapping as /v1/agent/ask: a forbidden model endpoint is 403,
		// a disabled one 409, and a busy conversation a conflict. A model name
		// the caller supplied is the one new source of those refusals here.
		writeRunError(c, err)
		return
	}

	// What the response calls this run. The requested model is echoed when there
	// is one — an OpenAI client keys off it — and the agent name stands in when
	// the caller named none. The engine's fallback (an unknown name resolves to
	// the agent's or the deployment's model) is not visible from the event
	// stream, so this is the request value rather than a guess at the result.
	modelLabel := req.GetModel()
	if modelLabel == "" {
		modelLabel = req.GetAgent()
	}

	if !req.GetStream() {
		collected := agentstream.Collect(c.Request.Context(), events)
		answer, images, runErr, paused := collected.Answer, collected.Images, collected.Err, collected.Paused
		if collected.Stopped && answer == "" {
			// A stopped run has no final answer, but the text it managed to
			// produce is what the caller asked to stop — deliver it, with the
			// finish reason saying it is not the whole answer.
			answer = collected.Streamed
		}
		if runErr != nil {
			slog.WarnContext(c.Request.Context(), "chat completion failed", "error", runErr)
			core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("%s", runErr.Error()))
			return
		}
		if paused {
			// A run that paused has no answer to give, and it is still parked:
			// reporting success would tell the caller a request that ran no tools
			// and produced no text had completed.
			core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("%s", pausedAnswer))
			return
		}
		// A stopped run keeps its partial answer but is not "stop": the model
		// did not finish. The streaming half reports the same thing.
		finishReason := "stop"
		if collected.Stopped {
			finishReason = "error"
		}
		resp := &v1.ChatCompletionResponse{
			Id:      responseID,
			Object:  "chat.completion",
			Created: createdAt,
			Model:   modelLabel,
			Choices: []*v1.ChatCompletionChoice{
				{
					Index: 0,
					Message: &v1.ChatCompletionMessage{
						Role:      "assistant",
						Content:   answer,
						ImageRefs: agent.RefsToProto(images),
					},
					FinishReason: finishReason,
				},
			},
			Usage:     &v1.Usage{},
			SessionID: sessionID,
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	h.handleStreamChatCompletion(c, events, responseID, modelLabel, createdAt, sessionID)
}

// pausedAnswer is what an OpenAI-compatible caller is told when its run paused
// for a decision. It names the one thing the caller can act on: approval is
// configured, and this endpoint cannot ask for it.
const pausedAnswer = "该请求需要人工确认，/v1/chat/completions 无法发起审批；" +
	"请改用支持审批的入口，或设置 agent.compat-approval-mode"

// handleStreamChatCompletion streams agent events as OpenAI-compatible SSE.
func (h *Handler) handleStreamChatCompletion(
	c *gin.Context,
	events <-chan agent.Event,
	responseID, model string,
	createdAt int64,
	sessionID string,
) {
	if err := openSSE(c); err != nil {
		slog.WarnContext(c.Request.Context(),
			"could not clear the SSE connection deadline; a run longer than the server timeout may be cut off",
			"error", err)
	}

	sink := agentstream.NewOpenAIChunkSink(c.Writer, c.Writer, agentstream.ChunkMeta{
		ID:        responseID,
		Model:     model,
		CreatedAt: createdAt,
		SessionID: sessionID,
	}, pausedAnswer)

	pumpErr := agentstream.Pump(c.Request.Context(), events, sink)
	// Pump returns when the event channel closes. A stream that ended without a
	// terminal event still has to close — otherwise a paused run, whose question
	// has no representation in this protocol, would read as a truncated answer.
	closeErr := sink.Close()
	// One log line, not two: Close returns the same sticky write error Pump saw,
	// and a broken connection is one event rather than two.
	switch {
	case pumpErr != nil:
		slog.WarnContext(c.Request.Context(), "could not write the chat completion stream", "error", pumpErr)
	case closeErr != nil:
		slog.WarnContext(c.Request.Context(), "could not close the chat completion stream", "error", closeErr)
	}
}

// lastUserMessage returns the content and images of the last user-role message.
//
// The last one wins rather than the first: this mirrors the OpenAI contract,
// where the model answers the newest turn and the earlier ones are context. A
// list with no user message at all is not an error here — the caller reports
// that, because it is the same answer whether the images were the problem.
func lastUserMessage(messages []*v1.ChatCompletionMessage) (string, []media.Ref, error) {
	question := ""
	var images []media.Ref
	for _, m := range messages {
		if m.GetRole() != agent.RoleUser {
			continue
		}
		question = m.GetContent()
		refs, err := imagesFromRequest(m.GetImages(), m.GetImageRefs())
		if err != nil {
			return "", nil, err
		}
		images = refs
	}
	return question, images, nil
}

// ClearSession handles DELETE /v1/chat/sessions/:sessionID — resets the
// conversation memory for the given session.
func (h *Handler) ClearSession(c *gin.Context) {
	var req validation.ClearSessionRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateClearSessionRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.Agent().Clear(c.Request.Context(), req.SessionID); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{}, nil)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		chatGroup := engine.Group("/chat")
		chatGroup.POST("/completions", h.ChatCompletions)
		chatGroup.DELETE("/sessions/:sessionID", h.ClearSession)
	})
}
