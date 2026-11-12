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

package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
)

// maxBody bounds one JSON-RPC request. Capability arguments are small; a
// caller that needs to move a file uses the sandbox tools.
const maxBody = 1 << 20

// Handler serves the MCP capability endpoint.
type Handler struct {
	Service *Service
}

// ServeHTTP authenticates the runtime token and dispatches one JSON-RPC call.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	claims, err := h.claims(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request protocol.Request
	if err := json.Unmarshal(body, &request); err != nil {
		writeResponse(w, protocol.NewErrorResponse(nil, protocol.CodeParseError, "parse error", nil))
		return
	}
	response := h.Handle(r.Context(), claims, &request)
	if response == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeResponse(w, response)
}

func (h *Handler) claims(r *http.Request) (Claims, error) {
	authorization := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || token == "" {
		return Claims{}, ErrInvalidToken
	}
	return h.Service.Issuer().Verify(token)
}

// Handle dispatches one decoded request. A notification returns nil.
func (h *Handler) Handle(ctx context.Context, claims Claims, request *protocol.Request) *protocol.Response {
	switch request.Method {
	case protocol.MethodInitialize:
		return protocol.NewResultResponse(request.ID, protocol.InitializeResult{
			ProtocolVersion: protocol.ProtocolVersion,
			ServerInfo:      protocol.Implementation{Name: "onexai-capability", Version: "1.0.0"},
			Capabilities: protocol.ServerCapabilities{
				Tools: &protocol.ToolsCapability{ListChanged: false},
			},
		})
	case protocol.MethodNotificationsInit:
		return nil
	case protocol.MethodToolsList:
		return h.listTools(claims, request)
	case protocol.MethodToolsCall:
		return h.callTool(ctx, claims, request)
	default:
		if request.IsNotification() {
			return nil
		}
		return protocol.NewErrorResponse(request.ID, protocol.CodeMethodNotFound,
			fmt.Sprintf("unknown method %q", request.Method), nil)
	}
}

func (h *Handler) listTools(claims Claims, request *protocol.Request) *protocol.Response {
	tools := h.Service.VisibleTools(claims)
	result := make([]protocol.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, protocol.Tool{
			Name:        tool.QualifiedName(),
			Namespace:   tool.Namespace,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return protocol.NewResultResponse(request.ID, protocol.ListToolsResult{Tools: result})
}

func (h *Handler) callTool(ctx context.Context, claims Claims, request *protocol.Request) *protocol.Response {
	var params protocol.CallToolParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return protocol.NewErrorResponse(request.ID, protocol.CodeInvalidParams, err.Error(), nil)
	}
	if params.Name == "" {
		return protocol.NewErrorResponse(request.ID, protocol.CodeInvalidParams, "tool name is required", nil)
	}
	args, err := json.Marshal(params.Arguments)
	if err != nil {
		return protocol.NewErrorResponse(request.ID, protocol.CodeInvalidParams, err.Error(), nil)
	}
	value, err := h.Service.Call(ctx, claims, params.Name, args)
	if err != nil {
		slog.WarnContext(ctx, "capability call failed", "tool", params.Name, "run", claims.RunID, "error", err)
		return protocol.NewResultResponse(request.ID, protocol.CallToolResult{
			Content: []protocol.ContentBlock{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return protocol.NewResultResponse(request.ID, protocol.CallToolResult{
			Content: []protocol.ContentBlock{{Type: "text", Text: "encode result: " + err.Error()}},
			IsError: true,
		})
	}
	return protocol.NewResultResponse(request.ID, protocol.CallToolResult{
		Content: []protocol.ContentBlock{{Type: "text", Text: string(encoded)}},
	})
}

func writeResponse(w http.ResponseWriter, response *protocol.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		slog.Warn("capability: write response", "error", err)
	}
}
