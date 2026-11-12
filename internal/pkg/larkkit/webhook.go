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

package larkkit

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
)

// VerifyWebhook authenticates a Lark webhook request and returns the plaintext
// event body (decrypted when an encrypt key is configured).
//
// The two-mode scheme is the one the Lark docs describe:
//   - encryptKey set: the X-Lark-Signature header must equal
//     SHA256(timestamp + nonce + encryptKey + rawBody), computed over the raw
//     (still-encrypted) body. On success the body is decrypted with AES-256-CBC.
//   - encryptKey empty: the plaintext body carries a VerificationToken in its
//     "token" field that must match the configured value.
//
// When neither is configured the request is rejected. Failing open would defeat
// the entire purpose of the check, and a public webhook URL that accepts any
// well-formed body is the one configuration an operator must not be able to
// reach by leaving a field blank.
//
// This is the same check chat performs inline in its bot handler. It lives
// here so the sync service does not carry a second copy of a security decision;
// chat can adopt it whenever that file is next opened.
func VerifyWebhook(c *gin.Context, body []byte, encryptKey, verificationToken string) ([]byte, error) {
	if encryptKey != "" {
		want := c.GetHeader(larkevent.EventSignature)
		if want == "" {
			return nil, fmt.Errorf("missing %s header", larkevent.EventSignature)
		}
		got := larkevent.Signature(
			c.GetHeader(larkevent.EventRequestTimestamp),
			c.GetHeader(larkevent.EventRequestNonce),
			encryptKey,
			string(body),
		)
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return nil, fmt.Errorf("signature mismatch")
		}

		// The body is encrypted only when an encrypt key is configured. Detect it
		// by shape and decrypt; a plaintext body passes through unchanged.
		var enc larkevent.EventEncryptMsg
		if err := json.Unmarshal(body, &enc); err == nil && enc.Encrypt != "" {
			plain, err := larkevent.EventDecrypt(enc.Encrypt, encryptKey)
			if err != nil {
				return nil, fmt.Errorf("decrypt event body: %w", err)
			}
			return plain, nil
		}
		return body, nil
	}

	if verificationToken != "" {
		var probe struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil, fmt.Errorf("parse verification token: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(probe.Token), []byte(verificationToken)) != 1 {
			return nil, fmt.Errorf("verification token mismatch")
		}
		return body, nil
	}

	return nil, fmt.Errorf("lark webhook security is not configured (encrypt-key or verification-token required)")
}
