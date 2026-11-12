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
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/agent/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// AgentAccessRequest is the body of POST /v1/authz/decisions.
//
// It is a deliberately small question — "may this subject use this agent" —
// rather than a general (subject, verb, resource) tuple. A general form would
// have to be answered by the same policy anyway, and its extra dimensions would
// be fields no caller sets: the verb is read for every caller that exists, and
// the resource kind is always an agent.
//
// # Why this route exists at all
//
// The authorization evaluation lives in this service and is reached only from
// inside it. A caller that has to know the answer *before* it acts — the chat
// service's /agent command, which writes a declaration it must be able to keep —
// otherwise has no choice but to reimplement the policy, and two implementations
// of one policy diverge. Fewer dimensions than a general endpoint is the price
// of not publishing a general one.
type AgentAccessRequest = validation.AgentAccessRequest

// AgentAccessResponse is the answer.
type AgentAccessResponse struct {
	// Allowed is true only on an explicit allow. A policy that says nothing —
	// NoOpinion — answers false, matching the run path's fail-closed default;
	// answering true here would promise something the gate then refuses.
	Allowed bool `json:"allowed"`
	// Reason explains the answer in the words the policy produced. It is shown
	// to whoever asked, so it is a sentence rather than a code.
	Reason string `json:"reason"`
}

// CheckAgentAccess handles POST /v1/authz/decisions.
//
// A failure inside the evaluator is answered as a server error rather than as a
// denial. The two are different facts — "this policy does not allow you" and
// "the policy could not be evaluated" — and a caller that cannot tell them apart
// will tell its user the wrong one.
func (h *Handler) CheckAgentAccess(c *gin.Context) {
	var body validation.AgentAccessRequest
	if err := core.ShouldBindJSON(c, &body, h.val.ValidateAgentAccessRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if !h.mayAskForDecision(c) {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
			"this service is not allowed to query authorization decisions"))
		return
	}

	allowed, reason, err := h.biz.Agent().CheckAgentAccess(c.Request.Context(), body.SubjectID, body.AgentName)
	if err != nil {
		core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("authorization could not be evaluated: %s", err.Error()))
		return
	}
	core.WriteResponse(c, &AgentAccessResponse{Allowed: allowed, Reason: reason}, nil)
}

// mayAskForDecision applies the optional caller allowlist
// (authz.decision-callers). An empty list keeps the endpoint open to every
// signed caller, which is the behaviour every deployment had; a non-empty list
// turns the endpoint into a question only the named services may ask, because
// each answer reveals part of the policy.
func (h *Handler) mayAskForDecision(c *gin.Context) bool {
	if len(h.decisionCallers) == 0 {
		return true
	}
	subj, ok := authz.SubjectFromContext(c.Request.Context())
	if !ok {
		return false
	}
	_, ok = h.decisionCallers[subj.PrimaryID()]
	return ok
}

// AuthzBackendListResponse is the body of GET /v1/authz/backends.
type AuthzBackendListResponse struct {
	// Backends are the registered backend names, sorted. "none" is not listed:
	// it is the sentinel an Agent uses to opt out, not a backend.
	Backends []string `json:"backends"`
	Count    int      `json:"count"`
}

// ListAuthzBackends handles GET /v1/authz/backends.
//
// It is the candidate list for an Agent's authzBackends field. The console
// reaches it with a bearer token, so it is registered on the unified group
// below rather than beside the signed decision endpoint.
func (h *Handler) ListAuthzBackends(c *gin.Context) {
	names := []string{}
	if h.authz != nil {
		names = h.authz.AgentBackendNames()
	}
	core.WriteResponse(c, &AuthzBackendListResponse{Backends: names, Count: len(names)}, nil)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		authzGroup := engine.Group("/authz")
		authzGroup.POST("/decisions", h.CheckAgentAccess)
	})

	// Reachable by either credential. The decision endpoint above stays on the
	// signed group: publishing the backend catalogue is harmless, asking the
	// policy a question is not.
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		engine.GET("/authz/backends", h.ListAuthzBackends)
	})
}
