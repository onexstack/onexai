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

// ControlTokenHeader is the header a control-plane caller presents.
const ControlTokenHeader = "X-Onex-Control-Token"

// controlPrefixes are the two resources that can hand out or rewrite every
// other permission. A leaked run credential should not be able to mint another
// credential or edit the rule that decides who reads a knowledge base.
//
// The paths follow the grouped resource surface (/v1/<group>/<plural>).
var controlPrefixes = []string{"/v1/agent/authorizationrules", "/v1/core/secrets"}

// ControlToken gates the control-plane resources behind a second credential.
//
// Empty required means no gate, which is the behaviour every deployment had
// before the option existed. The comparison is constant-time for the same
// reason the AK/SK digest is: a timing signal on a shared secret is a free
// oracle.
func ControlToken(required string) gin.HandlerFunc {
	if required == "" {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if !checkControlToken(c, required) {
			return
		}
		c.Next()
	}
}

// checkControlToken verifies the control token when the request path needs it.
// It writes the failure response and aborts the request, and never advances the
// chain.
func checkControlToken(c *gin.Context, required string) bool {
	if required == "" || !controlPath(c.Request.URL.Path) {
		return true
	}
	presented := c.GetHeader(ControlTokenHeader)
	if subtle.ConstantTimeCompare([]byte(presented), []byte(required)) != 1 {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
			"%s is required for secrets and authorization rules", ControlTokenHeader))
		c.Abort()
		return false
	}
	return true
}

// controlPath reports whether a path belongs to the control plane. It matches
// on a path segment boundary so /v1/secretsomething is not mistaken for
// /v1/secrets.
func controlPath(path string) bool {
	for _, prefix := range controlPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
