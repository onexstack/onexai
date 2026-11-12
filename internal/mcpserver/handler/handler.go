// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package handler dispatches incoming MCP JSON-RPC requests to the
// appropriate protocol handler, playing the same role for oai-mcpserver
// that internal/agent/handler plays for Gin routes.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/registry"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

const serviceVersion = "1.0.0"

// Handler dispatches MCP JSON-RPC requests against a namespace-aware tool registry.
type Handler struct {
	registry *registry.Registry
}

// NewHandler creates a new instance of Handler backed by reg.
func NewHandler(reg *registry.Registry) *Handler {
	return &Handler{registry: reg}
}

// Handle dispatches a single decoded JSON-RPC request and returns the
// response to write back. It returns nil for notifications, which per the
// JSON-RPC 2.0 specification never receive a response.
func (h *Handler) Handle(ctx context.Context, req *protocol.Request) *protocol.Response {
	ctx, span := otel.Tracer("handler").Start(ctx, "Handler.Handle")
	defer span.End()

	slog.InfoContext(ctx, "dispatching mcp request", "method", req.Method)

	switch req.Method {
	case protocol.MethodInitialize:
		return h.initialize(req)
	case protocol.MethodNotificationsInit:
		return nil
	case protocol.MethodToolsList:
		return h.listTools(req)
	case protocol.MethodToolsCall:
		return h.callTool(ctx, req)
	case protocol.MethodNamespacesList:
		return h.listNamespaces(req)
	default:
		if req.IsNotification() {
			return nil
		}
		return protocol.NewErrorResponse(req.ID, protocol.CodeMethodNotFound, errno.ErrMethodNotFound.Message, req.Method)
	}
}

func (h *Handler) initialize(req *protocol.Request) *protocol.Response {
	result := protocol.InitializeResult{
		ProtocolVersion: protocol.ProtocolVersion,
		ServerInfo: protocol.Implementation{
			Name:    "oai-mcpserver",
			Version: serviceVersion,
		},
		Capabilities: protocol.ServerCapabilities{
			Tools: &protocol.ToolsCapability{ListChanged: false},
		},
	}

	return protocol.NewResultResponse(req.ID, result)
}

func (h *Handler) listTools(req *protocol.Request) *protocol.Response {
	var params protocol.ListToolsParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			slog.Error("failed to unmarshal list tools params", "error", err)
			return protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, err.Error(), nil)
		}
	}

	tools := h.registry.List(params.Namespace)
	wireTools := make([]protocol.Tool, 0, len(tools))
	for _, t := range tools {
		wireTools = append(wireTools, protocol.Tool{
			Name:        t.QualifiedName(),
			Namespace:   t.Namespace,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	return protocol.NewResultResponse(req.ID, protocol.ListToolsResult{Tools: wireTools})
}

func (h *Handler) listNamespaces(req *protocol.Request) *protocol.Response {
	return protocol.NewResultResponse(req.ID, protocol.ListNamespacesResult{Namespaces: h.registry.Namespaces()})
}

func (h *Handler) callTool(ctx context.Context, req *protocol.Request) *protocol.Response {
	var params protocol.CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, err.Error(), nil)
	}

	namespace, name := splitQualifiedName(params.Name)
	tool, ok := h.registry.Get(namespace, name)
	if !ok {
		return protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, errno.ErrToolNotFound.Message, params.Name)
	}

	rawArgs, err := json.Marshal(params.Arguments)
	if err != nil {
		return protocol.NewErrorResponse(req.ID, protocol.CodeInvalidParams, err.Error(), nil)
	}
	slog.InfoContext(ctx, "calling tool", "namespace", tool.Namespace, "name", tool.Name)

	out, err := tool.Handler(ctx, rawArgs)
	if err != nil {
		slog.ErrorContext(ctx, "tool call failed", "namespace", tool.Namespace, "name", tool.Name, "error", err)
		return protocol.NewResultResponse(req.ID, protocol.CallToolResult{
			Content: []protocol.ContentBlock{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}

	text, err := toResultText(out)
	if err != nil {
		slog.ErrorContext(ctx, "failed to convert tool result to text", "error", err)
		return protocol.NewErrorResponse(req.ID, protocol.CodeInternalError, err.Error(), nil)
	}

	return protocol.NewResultResponse(req.ID, protocol.CallToolResult{
		Content: []protocol.ContentBlock{{Type: "text", Text: text}},
	})
}

// splitQualifiedName splits a wire tool name of the form "<namespace>_<name>"
// into its two parts. If qualified contains no separator, it is treated
// entirely as name and namespace is empty — the opposite of strings.Cut's
// own "no match" convention, which returns the whole input as the prefix.
func splitQualifiedName(qualified string) (namespace, name string) {
	if namespace, name, found := strings.Cut(qualified, "_"); found {
		return namespace, name
	}
	return "", qualified
}

// toResultText renders a tool handler's return value as the text payload of
// a CallToolResult content block. Strings pass through as-is; everything
// else is rendered as pretty-printed JSON.
func toResultText(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
