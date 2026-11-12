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

package taskstore

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
)

// maxBody bounds one task snapshot. A task carries messages and artifacts, so
// the bound is larger than the capability surface's.
const maxBody = 4 << 20

// HTTPHandler serves the task contract on the runtime-only listener.
type HTTPHandler struct {
	Service *Service
	Issuer  *capability.Issuer
}

// ServeHTTP dispatches one task request. The bearer token is the runtime's
// per-run capability token; a task may only be created in its session.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := h.claims(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/runtime/tasks")
	path = strings.Trim(path, "/")
	switch {
	case path == "" && r.Method == http.MethodPost:
		h.create(w, r, claims)
	case path == "" && r.Method == http.MethodGet:
		h.list(w, r, claims)
	case path != "" && strings.HasSuffix(path, "/settle") && r.Method == http.MethodPost:
		h.settle(w, r, claims, strings.TrimSuffix(path, "/settle"))
	case path != "" && r.Method == http.MethodPut:
		h.update(w, r, claims, path)
	case path != "" && r.Method == http.MethodGet:
		h.get(w, r, claims, path)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) claims(r *http.Request) (capability.Claims, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" || h.Issuer == nil {
		return capability.Claims{}, capability.ErrInvalidToken
	}
	return h.Issuer.Verify(token)
}

func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request, claims capability.Claims) {
	var body struct {
		SessionID string `json:"sessionId"`
		TaskJSON  string `json:"taskJson"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if claims.SessionKey != "" && body.SessionID != claims.SessionKey {
		writeError(w, http.StatusForbidden, "the task belongs to another session")
		return
	}
	task, err := h.Service.Create(r.Context(), body.SessionID, body.TaskJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, ginTask(task))
}

func (h *HTTPHandler) update(w http.ResponseWriter, r *http.Request, claims capability.Claims, taskID string) {
	var body struct {
		ExpectedVersion int64  `json:"expectedVersion"`
		TaskJSON        string `json:"taskJson"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.authorizeTask(r, claims, taskID) {
		writeError(w, http.StatusForbidden, "the task belongs to another session")
		return
	}
	task, err := h.Service.Update(r.Context(), taskID, body.TaskJSON, body.ExpectedVersion)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ginTask(task))
}

func (h *HTTPHandler) settle(w http.ResponseWriter, r *http.Request, claims capability.Claims, taskID string) {
	var body struct {
		ExpectedVersion int64  `json:"expectedVersion"`
		TaskJSON        string `json:"taskJson"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.authorizeTask(r, claims, taskID) {
		writeError(w, http.StatusForbidden, "the task belongs to another session")
		return
	}
	task, err := h.Service.Settle(r.Context(), taskID, body.TaskJSON, body.ExpectedVersion)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ginTask(task))
}

func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request, claims capability.Claims, taskID string) {
	if !h.authorizeTask(r, claims, taskID) {
		writeError(w, http.StatusForbidden, "the task belongs to another session")
		return
	}
	task, err := h.Service.Get(r.Context(), taskID)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ginTask(task))
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request, claims capability.Claims) {
	sessionID := r.URL.Query().Get("sessionId")
	if claims.SessionKey != "" && sessionID != claims.SessionKey {
		writeError(w, http.StatusForbidden, "the session does not match the token")
		return
	}
	tasks, err := h.Service.List(r.Context(), sessionID)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, ginTask(task))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (h *HTTPHandler) authorizeTask(r *http.Request, claims capability.Claims, taskID string) bool {
	if claims.SessionKey == "" {
		return true
	}
	task, err := h.Service.Get(r.Context(), taskID)
	return err == nil && task.SessionID == claims.SessionKey
}

func ginTask(task *Task) map[string]any {
	return map[string]any{
		"id":        task.ID,
		"sessionId": task.SessionID,
		"state":     task.State,
		"version":   task.Version,
		"taskJson":  task.TaskJSON,
	}
}

func decode(r *http.Request, out any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return errors.New("request too large")
	}
	return json.Unmarshal(body, out)
}

func writeTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runstore.ErrVersionConflict):
		writeError(w, http.StatusConflict, "version conflict")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "task not found")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
