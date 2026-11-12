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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// Authenticated endpoints.
		authed := engine.Group("", h.AuthMiddleware())
		authed.POST("/commands/exec", h.ExecCommand)
		authed.GET("/commands/tree", h.ListCommandTree)
		authed.POST("/cli/:cmdname", h.ExecNamedCommand)
	})
}

// ExecCommand handles POST /v1/commands/exec.
func (h *Handler) ExecCommand(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	var req v1.ExecCommandRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateExecCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	result := h.biz.BotV1().ExecuteAPICommand(c.Request.Context(), req.GetConvId(), req.GetCommand(), claims.UserID, claims.IsSRE)
	core.WriteResponse(c, toExecResponse(result), nil)
}

// ListCommandTree handles GET /v1/commands/tree — returns the full command
// tree (commands, subcommands, flags) for TUI popup + TAB completion.
func (h *Handler) ListCommandTree(c *gin.Context) {
	tree := h.biz.BotV1().CommandTree(c.Request.Context())
	core.WriteResponse(c, &v1.ListCommandTreeResponse{Commands: tree}, nil)
}

// ExecNamedCommand handles POST /v1/cli/:cmdname — executes a specific command
// by name with the request body mapped to flags.
func (h *Handler) ExecNamedCommand(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	var rq validation.ExecNamedCommandRequest
	if err := core.ShouldBindUri(c, &rq, h.val.ValidateExecNamedCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	cmdName := rq.CmdName

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}

	var sb strings.Builder
	sb.WriteString("/")
	sb.WriteString(cmdName)
	for k, v := range body {
		sb.WriteString(fmt.Sprintf(" --%s %v", k, v))
	}

	result := h.biz.BotV1().ExecuteAPICommand(c.Request.Context(), "", sb.String(), claims.UserID, claims.IsSRE)
	core.WriteResponse(c, toExecResponse(result), nil)
}

// toExecResponse converts a CommandResult into the proto ExecCommandResponse.
func toExecResponse(result *runtime.CommandResult) *v1.ExecCommandResponse {
	resp := &v1.ExecCommandResponse{Text: result.PlainText, Error: result.Err}
	if result.Raw != nil {
		if b, err := json.Marshal(result.Raw); err == nil {
			resp.Raw = string(b)
		}
	}
	if result.Markdown != nil {
		resp.Markdown = &v1.MarkdownContent{
			Title:   result.Markdown.Title,
			Content: result.Markdown.Content,
			Color:   result.Markdown.Color,
		}
	}
	return resp
}
