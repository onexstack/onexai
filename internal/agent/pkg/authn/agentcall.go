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

package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	authenticationv1 "k8s.io/api/authentication/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// AgentCall dependencies. They are function values rather than the store
// interface so this package stays a middleware package and a test can supply a
// map of rows.
type AgentCallOptions struct {
	// LoadAgent resolves an agent reference to its row.
	LoadAgent func(ctx context.Context, ref string) (*model.AgentM, error)
	// ListWebhooks returns every Webhook row; the caller filters by name.
	ListWebhooks func(ctx context.Context) ([]*model.WebhookM, error)
	// ResolveSecret resolves a Webhook's secretRef.
	ResolveSecret SecretValueFunc
	// DefaultAgent is the A2A surface's fallback agent when the request names
	// none. Empty means "no fallback".
	DefaultAgent string
}

// AgentCall authenticates Agent invocation entries with the Agent's own
// authentication Webhooks.
//
// # Why a wrapper and not a route group
//
// The target Agent is in the body of /agent/ask and /chat/a2a, and in the path
// of /agent/agents/:name/invoke. Authentication runs before any handler, so the
// wrapper reads the body (and restores it) or the path parameter, then decides
// which chain should authenticate the request. Routes stay where they are:
// moving them would mean duplicating their middleware chains and risking the
// duplicate-route panic gin raises.
//
// # What it replaces
//
// When an Agent names authentication Webhooks, a Bearer request is decided only
// by those TokenReview hooks; the platform's JWT/OIDC chain is not consulted,
// because the point of an Agent-level hook is that the Agent names its own
// identity source. An AK/SK request is still verified by the platform: the
// signature is a service credential the Webhook protocol cannot express.
type AgentCall struct {
	opts     AgentCallOptions
	reviewer *ReviewCaller
}

// NewAgentCall builds the middleware dependency.
func NewAgentCall(opts AgentCallOptions) *AgentCall {
	return &AgentCall{opts: opts, reviewer: NewReviewCaller(opts.ResolveSecret)}
}

// Wrap returns a middleware that either delegates to platform or authenticates
// with the target Agent's Webhooks. platform must write its own failure
// response and advance the chain on success (they all do).
func (a *AgentCall) Wrap(platform gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a == nil || platform == nil || !isAgentCallPath(c) {
			platform(c)
			return
		}

		names := a.resolveAgents(c)
		if len(names) == 0 {
			platform(c)
			return
		}

		agents, err := a.loadAgents(c.Request.Context(), names)
		if err != nil {
			slog.ErrorContext(c.Request.Context(), "agent authn: could not load the target agents", "error", err)
			core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("could not resolve the target agent"))
			c.Abort()
			return
		}
		hookNames := authnWebhookNames(agents)
		if len(hookNames) == 0 {
			platform(c)
			return
		}

		// The Agent has its own authentication. AK/SK stays on the platform
		// path (dual channel); a Bearer token is decided only by the Webhooks.
		if hasAKSKHeaders(c) {
			platform(c)
			return
		}
		token := bearer.Token(c)
		if token == "" {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage(
				"this agent requires a bearer token from one of its authentication webhooks"))
			c.Abort()
			return
		}

		hooks, err := a.loadAuthnHooks(c.Request.Context(), hookNames)
		if err != nil {
			slog.ErrorContext(c.Request.Context(), "agent authn: could not load authentication webhooks", "error", err)
			core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("could not resolve the agent's authentication webhooks"))
			c.Abort()
			return
		}
		if len(hooks) == 0 {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage(
				"this agent names authentication webhooks that are missing or disabled"))
			c.Abort()
			return
		}

		for _, hook := range hooks {
			status, _, err := a.reviewer.TokenReview(c.Request.Context(), hook, authenticationv1.TokenReviewSpec{
				Token:     token,
				Audiences: []string(hook.Audiences),
			})
			if err != nil {
				slog.WarnContext(c.Request.Context(), "agent authn: webhook call failed",
					"webhook", hook.Name, "error", err)
				continue
			}
			if status == nil || !status.Authenticated || status.User.Username == "" {
				reason := ""
				if status != nil {
					reason = status.Error
				}
				slog.InfoContext(c.Request.Context(), "agent authn: webhook rejected the token",
					"webhook", hook.Name, "reason", reason)
				continue
			}
			applyTokenReviewIdentity(c, hook, status)
			c.Next()
			return
		}

		core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage(
			"the agent's authentication webhooks rejected the bearer token"))
		c.Abort()
	}
}

// isAgentCallPath reports whether a route is an Agent invocation entry.
func isAgentCallPath(c *gin.Context) bool {
	path := c.FullPath()
	switch {
	case path == "/v1/agent/ask":
		return true
	case path == "/v1/chat/a2a", strings.HasPrefix(path, "/v1/chat/a2a/rest"):
		return true
	case strings.HasPrefix(path, "/v1/agent/agents/") &&
		(strings.HasSuffix(path, "/invoke") || strings.HasSuffix(path, "/invoke/stream")):
		return true
	}
	return false
}

// resolveAgents reads the selection the request targets.
func (a *AgentCall) resolveAgents(c *gin.Context) []string {
	path := c.FullPath()
	switch {
	case path == "/v1/agent/ask":
		return a.agentsFromAsk(c)
	case path == "/v1/chat/a2a", strings.HasPrefix(path, "/v1/chat/a2a/rest"):
		return a.agentsFromA2A(c)
	case strings.HasPrefix(path, "/v1/agent/agents/"):
		if name := strings.TrimSpace(c.Param("name")); name != "" {
			return []string{name}
		}
	}
	return nil
}

// agentsFromAsk reads selection.agents and restores the body.
func (a *AgentCall) agentsFromAsk(c *gin.Context) []string {
	body, ok := readAndRestoreBody(c)
	if !ok {
		return nil
	}
	var envelope struct {
		Selection struct {
			Agents []string `json:"agents"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	return trimAgentNames(envelope.Selection.Agents)
}

// a2aEnvelope covers both shapes the surface accepts: JSON-RPC wraps the call
// in params, while the REST transport posts the request object directly.
type a2aEnvelope struct {
	Params   *a2aMetadata   `json:"params"`
	Metadata map[string]any `json:"metadata"`
	Message  *struct {
		Metadata map[string]any `json:"metadata"`
	} `json:"message"`
}

type a2aMetadata struct {
	Metadata map[string]any `json:"metadata"`
	Message  *struct {
		Metadata map[string]any `json:"metadata"`
	} `json:"message"`
}

func (a *AgentCall) agentsFromA2A(c *gin.Context) []string {
	body, ok := readAndRestoreBody(c)
	if !ok {
		return a.fallbackAgent()
	}
	var envelope a2aEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return a.fallbackAgent()
	}

	var requestMetadata, messageMetadata map[string]any
	if envelope.Params != nil {
		requestMetadata = envelope.Params.Metadata
		if envelope.Params.Message != nil {
			messageMetadata = envelope.Params.Message.Metadata
		}
	} else {
		requestMetadata = envelope.Metadata
		if envelope.Message != nil {
			messageMetadata = envelope.Message.Metadata
		}
	}
	if names := a2a.SelectedAgents(requestMetadata, messageMetadata); len(names) > 0 {
		return names
	}
	return a.fallbackAgent()
}

func (a *AgentCall) fallbackAgent() []string {
	if a == nil || a.opts.DefaultAgent == "" {
		return nil
	}
	return []string{a.opts.DefaultAgent}
}

// readAndRestoreBody buffers the body so the selection can be read before the
// handler binds it. The bytes are restored exactly; ContentLength is unchanged
// because the body is unchanged.
func readAndRestoreBody(c *gin.Context) ([]byte, bool) {
	if c.Request == nil || c.Request.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "agent authn: could not read the request body", "error", err)
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// loadAgents resolves references to rows. A name that resolves to nothing is
// skipped: the run itself will report the missing agent.
func (a *AgentCall) loadAgents(ctx context.Context, names []string) ([]*model.AgentM, error) {
	out := make([]*model.AgentM, 0, len(names))
	for _, name := range names {
		row, err := a.opts.LoadAgent(ctx, name)
		if err != nil {
			return nil, err
		}
		if row != nil {
			out = append(out, row)
		}
	}
	return out, nil
}

// authnWebhookNames unions every selected Agent's authentication hooks. An
// Agent without any contributes nothing; an Agent with them is checked again at
// admission, so a merged run cannot escape one Agent's identity requirement.
func authnWebhookNames(agents []*model.AgentM) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, agent := range agents {
		for _, name := range agent.AuthnWebhooks {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

// loadAuthnHooks resolves names to enabled authentication Webhooks, in the
// order the Agent named them. A missing or wrongly-typed hook is skipped; if
// that leaves nothing, the caller fails closed.
func (a *AgentCall) loadAuthnHooks(ctx context.Context, names []string) ([]*model.WebhookM, error) {
	rows, err := a.opts.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*model.WebhookM, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	out := make([]*model.WebhookM, 0, len(names))
	for _, name := range names {
		hook, ok := byName[name]
		if !ok {
			slog.WarnContext(ctx, "agent authn: an agent names a webhook that does not exist", "webhook", name)
			continue
		}
		if !hook.Enabled() || hook.EffectiveType() != model.WebhookTypeAuthentication {
			slog.WarnContext(ctx, "agent authn: an agent names a webhook that is not an enabled authentication hook",
				"webhook", name, "type", hook.EffectiveType(), "status", hook.Status)
			continue
		}
		out = append(out, hook)
	}
	return out, nil
}

// applyTokenReviewIdentity turns the webhook's answer into the request identity.
//
// The identity is deliberately narrow: it can call the Agent and it is the
// subject the Agent's authorization hooks see. It never grants platform
// administrator/SRE roles — an Agent-level identity source must not be able to
// widen access to the platform's own management surface.
func applyTokenReviewIdentity(c *gin.Context, hook *model.WebhookM, status *authenticationv1.TokenReviewStatus) {
	username := status.User.Username
	claims := &bearer.Claims{
		UserID:   username,
		Name:     username,
		Provider: method.WebhookPrefix + hook.Name,
	}
	bearer.SetClaims(c, claims)

	subject := authz.SubjectFromUser(username, username)
	if len(status.User.Groups) > 0 {
		subject.Extra = map[string]string{"group_codes": strings.Join(status.User.Groups, ",")}
	}
	ctx := authz.ContextWithSubject(c.Request.Context(), subject)
	ctx = method.With(ctx, method.WebhookPrefix+hook.Name)
	c.Request = c.Request.WithContext(ctx)
}

// trimAgentNames drops blank entries, preserving order.
func trimAgentNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
