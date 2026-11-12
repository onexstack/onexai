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

package larkoauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StateTTL is how long an authorization round-trip may take. Feishu's code is
// valid for five minutes; ten gives the browser time to come back without
// leaving a signed redirect usable for long.
const StateTTL = 10 * time.Minute

// StateSigner signs the OAuth state parameter.
//
// # Why the state is signed rather than stored
//
// The callback is handled by the SPA, which has no server session to look a
// nonce up in. A signed value carries the two facts the callback needs — "this
// round-trip was started by us" and "where to land afterwards" — without a
// table, and an attacker cannot forge one without the JWT key. The key is the
// same one that signs bearer tokens; using it here does not weaken either,
// because the two signatures are over different payloads and neither is
// accepted as the other.
type StateSigner struct {
	secret []byte
}

// NewStateSigner returns a signer over secret.
func NewStateSigner(secret string) *StateSigner {
	return &StateSigner{secret: []byte(secret)}
}

type statePayload struct {
	// Redirect is the console path to land on after login.
	Redirect string `json:"r,omitempty"`
	// ExpiresAt is a unix second timestamp.
	ExpiresAt int64 `json:"e"`
}

// Sign returns the state for a round-trip that should land on redirect.
//
// redirect must be an in-app path. Accepting an absolute URL would turn the
// callback into an open redirect: an attacker could send a victim through
// Feishu and have this service vouch for a return to their own site.
func (s *StateSigner) Sign(redirect string) (string, error) {
	clean, err := sanitizeRedirect(redirect)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(statePayload{
		Redirect:  clean,
		ExpiresAt: time.Now().Add(StateTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode oauth state: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), nil
}

// Verify checks the signature and expiry and returns the redirect path.
func (s *StateSigner) Verify(state string) (string, error) {
	body, signature, ok := strings.Cut(state, ".")
	if !ok || body == "" || signature == "" {
		return "", fmt.Errorf("飞书登录状态无效，请重新登录")
	}
	expected := s.sign(body)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return "", fmt.Errorf("飞书登录状态无效，请重新登录")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("飞书登录状态无效，请重新登录")
	}
	var payload statePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("飞书登录状态无效，请重新登录")
	}
	if time.Now().Unix() > payload.ExpiresAt {
		return "", fmt.Errorf("飞书登录已超时，请重新登录")
	}
	redirect, err := sanitizeRedirect(payload.Redirect)
	if err != nil {
		return "", err
	}
	return redirect, nil
}

func (s *StateSigner) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// sanitizeRedirect keeps the value to a same-origin path.
func sanitizeRedirect(redirect string) (string, error) {
	redirect = strings.TrimSpace(redirect)
	if redirect == "" {
		return "/", nil
	}
	if !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		return "", fmt.Errorf("登录后跳转地址必须是站内路径")
	}
	return redirect, nil
}
