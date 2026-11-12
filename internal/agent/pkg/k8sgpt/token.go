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

package k8sgpt

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenIssuer is the "iss" claim of a generated token.
const TokenIssuer = "onexai"

// TokenSubject is the "sub" claim of a generated token.
const TokenSubject = "onexai-k8sgpt-router"

// GenerateJWTToken signs a token the k8sgpt servers accept.
//
// It carries no expiry on purpose: the token is a long-lived credential
// configured once and sent with every call, so an expiry would only mean the
// fleet quietly stops answering at some point in the future. Revocation is
// changing the signing key, which is a deployment's decision rather than a
// clock's.
//
// An empty key is refused rather than treated as "no auth": HMAC with an empty
// secret still produces a token, and one that anybody can forge.
func GenerateJWTToken(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("k8sgpt: JWT signing key is empty")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": TokenIssuer,
		"sub": TokenSubject,
		"iat": time.Now().Unix(),
	})
	signed, err := token.SignedString([]byte(key))
	if err != nil {
		return "", fmt.Errorf("k8sgpt: sign token: %w", err)
	}
	return signed, nil
}
