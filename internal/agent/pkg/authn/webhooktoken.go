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
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/errno"
)

// WebhookBearer authenticates a Kubernetes-style webhook caller against the
// shared token in auth.webhook-token.
//
// The endpoints it guards are the Kubernetes review APIs
// (authentication.k8s.io/v1 TokenReview and authorization.k8s.io/v1
// SubjectAccessReview). A Kubernetes webhook client authenticates with a
// kubeconfig, whose token travels as "Authorization: Bearer <token>"; that is
// the credential this middleware checks.
//
// An empty configured token means the deployment has not enabled the endpoints:
// refusing with 503 is deliberate. Treating empty as "no auth" would expose a
// token-verification oracle and a policy-introspection endpoint to anyone who
// can reach the port.
func WebhookBearer(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage(
				"auth.webhook-token is not configured; the Kubernetes review endpoints are disabled"))
			c.Abort()
			return
		}

		provided, ok := webhookBearerToken(c)
		if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("invalid webhook credential"))
			c.Abort()
			return
		}

		c.Next()
	}
}

func webhookBearerToken(c *gin.Context) (string, bool) {
	after, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok {
		return "", false
	}
	token := strings.TrimSpace(after)
	if token == "" {
		return "", false
	}
	return token, true
}
