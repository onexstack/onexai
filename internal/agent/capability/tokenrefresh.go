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
	"net/http"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
)

// RunLookup answers whether a run is still in flight.
//
// It is one method rather than the run store because that is the whole question:
// a token may be renewed while its run lives and not a moment after.
type RunLookup interface {
	GetRun(ctx context.Context, id string) (*model.RunM, error)
}

// TokenRefreshHandler renews a run's capability token.
//
// # Why renewal exists at all
//
// A token is deliberately short-lived: one that outlives its run is a credential
// that can be replayed after the run has ended. But a run's length is not known
// when it starts, and a long one would otherwise have every tool call start
// failing at the hour mark — mid-answer, with no way back. Renewal resolves both
// halves: the token stays short, and a run that is still going can ask for
// another.
//
// # Why it cannot be used to extend a finished run
//
// The run row is the authority. Once it is settled — completed, failed,
// canceled — the answer is 403 and the token dies on its own schedule. Nothing
// the runtime sends can revive it, because the claims are taken from the token
// it presented rather than from the request body.
type TokenRefreshHandler struct {
	Issuer *Issuer
	Runs   RunLookup
	// TTL is how long a renewed token lasts. Zero means one hour, which is the
	// same budget the run path issues with.
	TTL time.Duration
}

// DefaultTokenRefreshTTL is the renewed lifetime a deployment gets without
// saying otherwise.
const DefaultTokenRefreshTTL = time.Hour

// ServeHTTP renews the bearer token's claims while its run is in flight.
func (h *TokenRefreshHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Issuer == nil || h.Runs == nil {
		writeTokenError(w, http.StatusNotFound, "token renewal is not configured")
		return
	}
	raw := bearerToken(r)
	if raw == "" {
		writeTokenError(w, http.StatusUnauthorized, "a capability token is required")
		return
	}
	claims, err := h.Issuer.Verify(raw)
	if err != nil {
		// An expired token cannot be renewed: renewal is a way to keep a live
		// run going, not a way to bring one back.
		writeTokenError(w, http.StatusUnauthorized, "the capability token is not valid")
		return
	}
	run, err := h.Runs.GetRun(r.Context(), claims.RunID)
	if err != nil || run == nil {
		writeTokenError(w, http.StatusForbidden, "the run is not in flight")
		return
	}
	if run.State != model.RunStateRunning {
		writeTokenError(w, http.StatusForbidden, "the run is not in flight")
		return
	}
	ttl := h.TTL
	if ttl <= 0 {
		ttl = DefaultTokenRefreshTTL
	}
	token, err := h.Issuer.Issue(Claims{
		RunID:       claims.RunID,
		SessionKey:  claims.SessionKey,
		Agent:       claims.Agent,
		Subject:     claims.Subject,
		SubjectKind: claims.SubjectKind,
		Scopes:      claims.Scopes,
		RootRunID:   claims.RootRunID,
		ParentRunID: claims.ParentRunID,
		Depth:       claims.Depth,
		// A refreshed token that dropped the narrowing would silently widen the
		// rest of the run back to the agent's full effective set.
		Narrowed:       claims.Narrowed,
		Skills:         claims.Skills,
		KnowledgeBases: claims.KnowledgeBases,
		CustomCommands: claims.CustomCommands,
	}, ttl)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "the token could not be issued")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":     token,
		"expiresAt": time.Now().Add(ttl).Unix(),
	})
}

// bearerToken reads the token from the Authorization header.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

// writeTokenError answers in the shape the rest of this surface uses.
//
// The reason is deliberately coarse: a caller that is probing learns whether its
// own token worked, and nothing about any other run.
func writeTokenError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": reason})
}
