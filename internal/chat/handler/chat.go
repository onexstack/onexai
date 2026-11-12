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
	"context"
	"encoding/json"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/safego"
	"github.com/onexstack/onexai/pkg/wshub"
)

// WebSocket handles GET /v1/ws — upgrades the connection and registers the
// client with the WS Hub.
func (h *Handler) WebSocket(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	if _, err := h.hub.Upgrade(c.Writer, c.Request, claims.UserID, claims.OpenID); err != nil {
		slog.WarnContext(c.Request.Context(), "failed to upgrade websocket connection", "error", err)
	}
}

// ListConversation handles GET /v1/conversations.
func (h *Handler) ListConversation(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	convs, err := h.biz.ChatV1().ListConversation(c.Request.Context(), claims.UserID)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, convs, nil)
}

// WSFrameRouter dispatches an inbound WS frame to the appropriate handler.
// Called from Hub.OnFrame.
func (h *Handler) WSFrameRouter(clientID string, frame wshub.ClientFrame) {
	ctx := context.Background()
	switch frame.Type {
	case wshub.FrameTypePing:
		h.hub.SendTo(clientID, wshub.ServerFrame{
			Type:      wshub.FrameTypePong,
			RequestID: frame.RequestID,
		})
	case wshub.FrameTypeSend:
		var payload wshub.SendPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		convID := frame.ConvID
		// An image with no words is a message too: a screenshot is a question
		// in itself, and requiring text beside it would refuse the commonest
		// thing a person does with one.
		if convID != "" && (payload.Content != "" || len(payload.Images) > 0) {
			select {
			case h.sendSlots <- struct{}{}:
				safego.Go(ctx, "ws send "+clientID, func() {
					defer func() { <-h.sendSlots }()
					h.biz.ChatV1().SendToIM(ctx, clientID, convID, payload.Content, payload.Images, payload.ClientMsgID)
				})
			default:
				h.hub.SendTo(clientID, wshub.ServerFrame{
					Type:      wshub.FrameTypeError,
					RequestID: frame.RequestID,
					Payload:   map[string]string{"message": "too many concurrent messages; retry shortly"},
				})
			}
		}
	case wshub.FrameTypeSubscribe:
		var payload wshub.SubscribePayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return
		}
		convID := payload.ConvID
		if convID == "" {
			convID = frame.ConvID
		}
		h.hub.Subscribe(clientID, convID)
	}
}

// GetHistory handles GET /v1/history/:convID — returns recent messages.
func (h *Handler) GetHistory(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ChatV1().GetHistoryView, h.val.ValidateGetHistoryRequest)
}

// ListMember handles GET /v1/members/:convID — returns conversation members
// for @-mention autocomplete. Returns an empty list when unsupported.
func (h *Handler) ListMember(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ChatV1().ListMember, h.val.ValidateListMemberRequest)
}

// ListAgent handles GET /v1/agents — returns all agents from oai-agent-apiserver
// (robots + platforms), with the built-in OneX agent ordered first.
func (h *Handler) ListAgent(c *gin.Context) {
	if _, ok := bearer.ClaimsFrom(c); !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.ChatV1().ListAgent(c.Request.Context())
	core.WriteResponse(c, resp, err)
}

// GetImage handles GET /v1/images/:msgID — returns the raw image bytes for an
// image message, proxied from the IM backend.
func (h *Handler) GetImage(c *gin.Context) {
	var req validation.GetImageRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateGetImageRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	data, contentType, err := h.biz.ChatV1().DownloadImage(c.Request.Context(), req.MsgID)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Data(200, contentType, data)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// Authenticated endpoints.
		authed := engine.Group("", h.AuthMiddleware())
		authed.GET("/ws", h.WebSocket)
		authed.GET("/conversations", h.ListConversation)
		authed.GET("/history/:convID", h.GetHistory)
		authed.GET("/members/:convID", h.ListMember)
		authed.GET("/search", h.SearchMessages)
		authed.GET("/agents", h.ListAgent)
		authed.GET("/images/:msgID", h.GetImage)
	})
}

// SearchMessages handles GET /v1/search?q=&limit= — cross-conversation message
// search over the local cache.
func (h *Handler) SearchMessages(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery, h.biz.ChatV1().SearchMessages, h.val.ValidateSearchMessagesRequest)
}
