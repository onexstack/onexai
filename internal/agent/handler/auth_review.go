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
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/onexstack/onexai/internal/agent/biz/v1/authreview"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// maxReviewBodyBytes bounds a review request. The Kubernetes objects are small;
// a caller that sends more is not making a review request.
const maxReviewBodyBytes = 1 << 20

// TokenReview handles POST /v1/authn/tokenreviews (console).
func (h *Handler) TokenReview(c *gin.Context) {
	h.handleTokenReview(c, http.StatusOK)
}

// TokenReviewWebhook handles POST /apis/authentication.k8s.io/v1/tokenreviews.
// The Kubernetes create verb answers 201.
func (h *Handler) TokenReviewWebhook(c *gin.Context) {
	h.handleTokenReview(c, http.StatusCreated)
}

// SubjectAccessReview handles POST /v1/authz/subjectaccessreviews (console).
//
// A signed-in non-administrator may only ask about itself: the handler replaces
// the submitted user and groups with the identity the middleware authenticated.
// A service caller (AK/SK, no JWT claims) is trusted with an arbitrary subject,
// because that is the same trust the signed decision endpoint already carries.
func (h *Handler) SubjectAccessReview(c *gin.Context) {
	h.handleSubjectAccessReview(c, http.StatusOK, true)
}

// SubjectAccessReviewWebhook handles
// POST /apis/authorization.k8s.io/v1/subjectaccessreviews. The caller has
// already been authenticated by the webhook credential, so it may review any
// subject.
func (h *Handler) SubjectAccessReviewWebhook(c *gin.Context) {
	h.handleSubjectAccessReview(c, http.StatusCreated, false)
}

// AuthWebhookInfo handles GET /v1/authn/webhook-info.
type AuthWebhookInfoResponse struct {
	authreview.Info
	AuthzBackends []string `json:"authzBackends"`
}

func (h *Handler) AuthWebhookInfo(c *gin.Context) {
	if h.authReview == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("access review is not configured"))
		return
	}
	backends := []string{}
	if h.authz != nil {
		backends = h.authz.AgentBackendNames()
	}
	core.WriteResponse(c, AuthWebhookInfoResponse{
		Info:          h.authReview.Info(),
		AuthzBackends: backends,
	}, nil)
}

func (h *Handler) handleTokenReview(c *gin.Context, status int) {
	if h.authReview == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("access review is not configured"))
		return
	}
	var req authenticationv1.TokenReview
	if err := bindReview(c, &req); err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}
	c.JSON(status, h.authReview.TokenReview(c.Request.Context(), &req))
}

func (h *Handler) handleSubjectAccessReview(c *gin.Context, status int, enforceSelf bool) {
	if h.authReview == nil {
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("access review is not configured"))
		return
	}
	var req authorizationv1.SubjectAccessReview
	if err := bindReview(c, &req); err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}

	if enforceSelf {
		if claims, ok := bearer.ClaimsFrom(c); ok && !claims.IsAdmin {
			req.Spec.User, req.Spec.Groups = h.authReview.SelfSubject(c.Request.Context(), claims)
		}
	}

	c.JSON(status, h.authReview.SubjectAccessReview(c.Request.Context(), &req))
}

func bindReview(c *gin.Context, dst any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReviewBodyBytes)
	return c.ShouldBindJSON(dst)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		engine.POST("/authn/tokenreviews", h.TokenReview)
		engine.GET("/authn/webhook-info", h.AuthWebhookInfo)
		engine.POST("/authz/subjectaccessreviews", h.SubjectAccessReview)
	})
}
