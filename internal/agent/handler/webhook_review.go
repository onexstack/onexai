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
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/store/where"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentauthn "github.com/onexstack/onexai/internal/agent/pkg/authn"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// TestTokenReview answers POST /v1/admin/webhooks/:name/test/tokenreviews.
//
// It calls the named Webhook exactly the way an Agent's authentication path
// would: same endpoint, same credential, same body. The answer is returned
// verbatim so an operator can see what the receiver said, including the HTTP
// status and latency — a test that only said "failed" would leave the reason in
// the service's log.
func (h *Handler) TestTokenReview(c *gin.Context) {
	if !bearer.CheckAdmin(c) {
		return
	}
	hook, err := h.reviewWebhook(c, model.WebhookTypeAuthentication)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if h.resources == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the resource store is not configured"))
		return
	}

	var review struct {
		Spec struct {
			Token     string   `json:"token"`
			Audiences []string `json:"audiences"`
		} `json:"spec"`
	}
	if err := c.ShouldBindJSON(&review); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	if review.Spec.Token == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("spec.token is required"))
		return
	}

	spec := authenticationv1.TokenReviewSpec{Token: review.Spec.Token, Audiences: review.Spec.Audiences}
	status, outcome, callErr := h.reviewCaller().TokenReview(c.Request.Context(), hook, spec)
	h.writeReview(c, status, outcome, callErr)
}

// TestSubjectAccessReview answers POST
// /v1/admin/webhooks/:name/test/subjectaccessreviews.
func (h *Handler) TestSubjectAccessReview(c *gin.Context) {
	if !bearer.CheckAdmin(c) {
		return
	}
	hook, err := h.reviewWebhook(c, model.WebhookTypeAuthorization)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if h.resources == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("the resource store is not configured"))
		return
	}

	var review authorizationv1.SubjectAccessReview
	if err := c.ShouldBindJSON(&review); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	if review.Spec.User == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("spec.user is required"))
		return
	}

	status, outcome, callErr := h.reviewCaller().SubjectAccessReview(c.Request.Context(), hook, review)
	h.writeReview(c, status, outcome, callErr)
}

// reviewWebhook loads the named Webhook and checks it is the kind the test
// endpoint speaks for.
func (h *Handler) reviewWebhook(c *gin.Context, wantType string) (*model.WebhookM, error) {
	if h.resources == nil {
		return nil, errno.ErrNotConfigured.WithMessage("the resource store is not configured")
	}
	name := c.Param("name")
	opts := where.F("name", name)
	if namespace := c.Query("namespace"); namespace != "" {
		opts = opts.F("namespace", namespace)
	}
	hook, err := h.resources.Webhooks().Get(c.Request.Context(), opts)
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("could not read webhook %q: %s", name, err.Error())
	}
	if hook == nil {
		return nil, errno.ErrNotFound.WithMessage("webhook %q does not exist", name)
	}
	if hook.EffectiveType() != wantType {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"webhook %q is a %s hook, not a %s hook", name, hook.EffectiveType(), wantType)
	}
	if !hook.Enabled() {
		return nil, errno.ErrInvalidArgument.WithMessage("webhook %q is disabled", name)
	}
	return hook, nil
}

// reviewCaller builds the outbound caller over the platform's secret store.
func (h *Handler) reviewCaller() *agentauthn.ReviewCaller {
	return agentauthn.NewReviewCaller(func(ctx context.Context, ref string) (string, bool) {
		secret, err := h.resources.Secrets().Get(ctx, where.F("secret_id", ref))
		if err != nil || secret == nil || secret.SecretKey == "" {
			return "", false
		}
		return secret.SecretKey, true
	})
}

// writeReview returns the outcome even when the call failed: the operator is
// debugging the receiver, and the receiver's answer is the evidence.
func (h *Handler) writeReview(c *gin.Context, status any, outcome *agentauthn.ReviewOutcome, callErr error) {
	body := gin.H{"status": status, "outcome": outcome}
	if outcome != nil {
		body["httpStatus"] = outcome.HTTPStatus
		body["latencyMs"] = outcome.LatencyMs
		body["raw"] = outcome.Raw
	}
	if callErr != nil {
		body["error"] = callErr.Error()
	}
	c.JSON(http.StatusOK, body)
}

func init() {
	// The unified face rather than /v1/admin: these are helpers over an
	// existing resource, and the administrative answer is made by the handler
	// itself (CheckAdmin). Mounting them on the admin face would also put them
	// outside the signed face's route set, which the route-parity test forbids
	// for good reason — an admin route with no signed counterpart is a surface
	// nobody exercises.
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		hooks := engine.Group("/agent/webhooks", mws...)
		hooks.POST("/:name/test/tokenreviews", h.TestTokenReview)
		hooks.POST("/:name/test/subjectaccessreviews", h.TestSubjectAccessReview)
	})
}
