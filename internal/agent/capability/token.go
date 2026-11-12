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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken reports a token that is malformed, unsigned by this issuer,
// or expired. The caller answers 401; the reason is deliberately not split, so
// a probe cannot tell a forged token from an expired one.
var ErrInvalidToken = errors.New("capability: invalid runtime token")

// Claims is the identity and scope of one run's capability calls.
type Claims struct {
	RunID      string   `json:"runId"`
	SessionKey string   `json:"sessionKey"`
	Agent      string   `json:"agent"`
	Subject    string   `json:"subject"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  int64    `json:"expiresAt"`

	// SubjectKind travels with the subject because a name alone is ambiguous:
	// the delegate is authorized as whoever called it, and a service account
	// and a person with the same name are two different identities.
	SubjectKind string `json:"subjectKind,omitempty"`
	// Narrowed says the name lists below are authoritative for this run.
	//
	// False keeps the pre-existing behaviour: a provider resolves the agent's
	// full effective set. True means a debug run selected a subset, so the
	// provider must serve exactly these names. The flag, not an empty list, is
	// what distinguishes "narrowed to none" from "not narrowed" — the same
	// distinction the request's message presence carries.
	Narrowed       bool     `json:"narrowed,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	CustomCommands []string `json:"customCommands,omitempty"`
	// RootRunID is the run at the top of a delegation chain, and Depth is how
	// many delegations down this run is. A run that was not delegated has an
	// empty root and depth zero; both travel from parent to child so a chain
	// can be bounded and traced.
	RootRunID   string `json:"rootRunId,omitempty"`
	ParentRunID string `json:"parentRunId,omitempty"`
	Depth       int    `json:"depth,omitempty"`
}

// HasScope reports whether the claims allow namespace. "*" allows everything.
func (c Claims) HasScope(namespace string) bool {
	for _, scope := range c.Scopes {
		if scope == "*" || scope == namespace {
			return true
		}
	}
	return false
}

// Issuer signs and verifies per-run capability tokens. The key is the trust
// boundary: a runtime that holds a token can call exactly the scopes it names
// until it expires.
type Issuer struct {
	key []byte
	now func() time.Time
}

// NewIssuer builds an issuer. An empty key generates a random one, which is
// safe for a single process but means tokens do not survive a restart; a
// deployment that issues tokens from more than one replica must configure the
// same key everywhere.
func NewIssuer(key []byte) *Issuer {
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	return &Issuer{key: append([]byte(nil), key...), now: time.Now}
}

// Issue signs claims. The TTL is bounded by the caller; a token that outlives
// its run is a credential that can be replayed after the run has ended.
func (i *Issuer) Issue(claims Claims, ttl time.Duration) (string, error) {
	if claims.RunID == "" || claims.Agent == "" {
		return "", fmt.Errorf("capability: token needs a run and an agent")
	}
	if ttl <= 0 {
		return "", fmt.Errorf("capability: token needs a positive ttl")
	}
	claims.ExpiresAt = i.now().Add(ttl).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("capability: encode claims: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + i.sign(body), nil
}

// Verify checks the signature and expiry and returns the claims.
func (i *Issuer) Verify(token string) (Claims, error) {
	body, signature, ok := strings.Cut(token, ".")
	if !ok || body == "" || signature == "" {
		return Claims{}, ErrInvalidToken
	}
	if !hmac.Equal([]byte(signature), []byte(i.sign(body))) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt <= i.now().Unix() {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func (i *Issuer) sign(body string) string {
	mac := hmac.New(sha256.New, i.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
