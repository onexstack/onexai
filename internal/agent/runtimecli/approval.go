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

package runtimecli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// approvalDecision is what a human answered.
type approvalDecision struct {
	allow  bool
	reason string
}

// pendingApproval is one tool call waiting for a human.
type pendingApproval struct {
	id     string
	tool   string
	input  map[string]any
	answer chan approvalDecision
}

// ApprovalBroker hosts the MCP tool a CLI calls when a tool needs a decision.
//
// # Why the CLI has to call us
//
// A CLI agent cannot pause itself: it either runs a tool or refuses it. Claude
// Code offers `--permission-prompt-tool`, an MCP tool it consults exactly when
// it would otherwise prompt; that tool's answer is the decision. So the pause
// is implemented as a *blocking tool call* — the CLI waits inside its own tool
// loop while a human decides.
//
// # What this is not
//
// The process stays alive for the duration of the decision, so a pause does not
// survive a restart, and a deployment that restarts the runtime loses it. That
// is the honest limit of this mechanism; the in-process eino runtime has a
// checkpoint-persisted pause instead.
type ApprovalBroker struct {
	token    string
	listener net.Listener
	server   *http.Server

	mu      sync.Mutex
	pending map[string]*pendingApproval
}

// ApprovalToolName is the MCP tool the CLI is pointed at.
const ApprovalToolName = "approve"

// NewApprovalBroker starts the broker on a loopback port.
func NewApprovalBroker() (*ApprovalBroker, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("runtimecli: approval listener: %w", err)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("runtimecli: approval token: %w", err)
	}
	broker := &ApprovalBroker{
		token:    hex.EncodeToString(raw),
		listener: listener,
		pending:  make(map[string]*pendingApproval),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", broker.serveMCP)
	mux.HandleFunc("/decide", broker.serveDecide)
	broker.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = broker.server.Serve(listener) }()
	broker.announce()
	return broker, nil
}

// announce tells the operator where to answer.
//
// The token goes into a private file rather than the log: a log line is what
// ends up pasted into an issue, and this token is the right to approve tool
// calls. Logging the path is enough for whoever runs the deployment to find it.
func (b *ApprovalBroker) announce() {
	record := map[string]any{
		"decide_url": strings.TrimSuffix(b.URL(), "/mcp") + "/decide",
		"token":      b.token,
		"pid":        os.Getpid(),
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("onexai-approval-%d.json", os.Getpid()))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		slog.Warn("runtimecli: could not record the approval endpoint", "error", err)
		return
	}
	slog.Info("runtimecli: tool approvals wait for an answer", "decide_url", record["decide_url"], "credentials_file", path)
}

// URL is the MCP endpoint the CLI is pointed at.
func (b *ApprovalBroker) URL() string {
	return "http://" + b.listener.Addr().String() + "/mcp"
}

// Token is the bearer credential the CLI presents.
func (b *ApprovalBroker) Token() string { return b.token }

// Close stops the listener and releases every waiting call as a refusal.
func (b *ApprovalBroker) Close() error {
	b.mu.Lock()
	for _, call := range b.pending {
		select {
		case call.answer <- approvalDecision{allow: false, reason: "the runtime is shutting down"}:
		default:
		}
	}
	b.pending = map[string]*pendingApproval{}
	b.mu.Unlock()
	return b.server.Close()
}

// Pending returns the id of the call currently waiting, if any.
func (b *ApprovalBroker) Pending() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.pending {
		return id, true
	}
	return "", false
}

// Decide answers a waiting call. It is the human's side of the pause.
func (b *ApprovalBroker) Decide(id string, allow bool, reason string) bool {
	b.mu.Lock()
	call, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case call.answer <- approvalDecision{allow: allow, reason: reason}:
		return true
	default:
		return false
	}
}

// serveDecide is the human-facing endpoint: POST /decide {id, allow, reason}.
//
// It requires the broker's token, which the operator is given out of band: an
// unauthenticated decision endpoint would let anything that can reach the port
// approve its own tool calls.
func (b *ApprovalBroker) serveDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+b.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		ID     string `json:"id"`
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !b.Decide(body.ID, body.Allow, body.Reason) {
		http.Error(w, "no such pending approval", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveMCP is the minimal MCP surface Claude's permission tool needs:
// initialize, tools/list and tools/call.
func (b *ApprovalBroker) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	switch request.Method {
	case "initialize":
		writeRPCResult(w, request.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "onexai-approvals", "version": "v1"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeRPCResult(w, request.ID, map[string]any{"tools": []map[string]any{{
			"name":        ApprovalToolName,
			"description": "Ask a human whether a tool call may run.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"tool_name"},
				"properties": map[string]any{
					"tool_name":   map[string]any{"type": "string"},
					"input":       map[string]any{"type": "object"},
					"tool_use_id": map[string]any{"type": "string"},
				},
			},
		}}})
	case "tools/call":
		b.callTool(w, r, request.ID, request.Params)
	default:
		writeRPCError(w, request.ID, -32601, "method not found")
	}
}

// callTool blocks until a human answers or the caller goes away.
func (b *ApprovalBroker) callTool(w http.ResponseWriter, r *http.Request, id, params json.RawMessage) {
	var call struct {
		Name      string `json:"name"`
		Arguments struct {
			ToolName  string         `json:"tool_name"`
			Input     map[string]any `json:"input"`
			ToolUseID string         `json:"tool_use_id"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || call.Name != ApprovalToolName {
		writeRPCError(w, id, -32602, "unknown tool")
		return
	}
	requestID := call.Arguments.ToolUseID
	if requestID == "" {
		requestID = call.Arguments.ToolName
	}
	answer := make(chan approvalDecision, 1)
	b.mu.Lock()
	b.pending[requestID] = &pendingApproval{
		id:     requestID,
		tool:   call.Arguments.ToolName,
		input:  call.Arguments.Input,
		answer: answer,
	}
	b.mu.Unlock()
	slog.Info("runtimecli: a tool call is waiting for a decision",
		"approval_id", requestID, "tool", call.Arguments.ToolName)
	defer func() {
		b.mu.Lock()
		delete(b.pending, requestID)
		b.mu.Unlock()
	}()

	select {
	case decision := <-answer:
		writeRPCResult(w, id, approvalResult(decision))
	case <-r.Context().Done():
		// The CLI went away (cancel, or its own timeout). Nothing to answer.
	case <-time.After(approvalWait):
		// Nobody decided. Refusing is the only safe answer: a tool call that
		// nobody approved must not run because the clock ran out.
		writeRPCResult(w, id, approvalResult(approvalDecision{
			allow: false, reason: "no human answered in time",
		}))
	}
}

// approvalWait bounds how long a call waits for a human.
const approvalWait = 10 * time.Minute

// approvalResult renders the decision in the shape Claude expects from a
// permission-prompt tool.
func approvalResult(decision approvalDecision) map[string]any {
	payload := map[string]any{"behavior": "deny"}
	if decision.allow {
		payload["behavior"] = "allow"
	}
	if decision.reason != "" {
		payload["message"] = decision.reason
	}
	encoded, _ := json.Marshal(payload)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// waitForDecision is a convenience for tests and callers that want a channel.
func (b *ApprovalBroker) waitForDecision(ctx context.Context, id string) (approvalDecision, bool) {
	b.mu.Lock()
	call, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return approvalDecision{}, false
	}
	select {
	case decision := <-call.answer:
		return decision, true
	case <-ctx.Done():
		return approvalDecision{}, false
	}
}
