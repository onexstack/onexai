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

// Package oidc issues the platform's OIDC-compatible access tokens.
//
// Kubernetes' structured authentication (apiserver.config.k8s.io/v1
// AuthenticationConfiguration) only understands OIDC/JWT issuers: a discovery
// document, a JWKS endpoint and claims mapped to username/groups. The platform
// used an HS256 shared secret, which no Kubernetes authenticator can express.
// This package is the bridge: it signs RS256 tokens with a discoverable key and
// publishes the two documents an OIDC consumer fetches.
//
// It does not implement the authorization-code flow. The platform's login
// providers (local password, Lark OAuth) remain the place a person proves who
// they are; this package only turns the resulting identity into a token another
// service can verify with the Kubernetes machinery.
package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

// Config configures a Signer.
type Config struct {
	// IssuerURL is the value that goes into `iss` and the discovery document.
	// It must be the URL a consumer can reach this process at.
	IssuerURL string
	// Audiences are the acceptable `aud` values.
	Audiences []string
	// KeyID identifies the signing key in a multi-key JWKS. Empty derives one
	// from the public key's SHA-256 thumbprint.
	KeyID string
	// PrivateKeyPEM is an RSA private key in PKCS#1 or PKCS#8 PEM form. Empty
	// generates an ephemeral key, which is useful for local development and
	// invalidates every issued token on restart.
	PrivateKeyPEM []byte
	// TokenTTL bounds an issued token. Zero means one hour.
	TokenTTL time.Duration
}

// Signer signs and publishes the platform's OIDC identity.
type Signer struct {
	issuer    string
	audiences []string
	keyID     string
	key       *rsa.PrivateKey
	ttl       time.Duration
}

// New builds a Signer from cfg.
func New(cfg Config) (*Signer, error) {
	if strings.TrimSpace(cfg.IssuerURL) == "" {
		return nil, errors.New("oidc: issuer url is required")
	}
	if len(cfg.Audiences) == 0 {
		return nil, errors.New("oidc: at least one audience is required")
	}
	key, err := loadOrGenerateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	keyID := cfg.KeyID
	if keyID == "" {
		keyID = keyIDFor(&key.PublicKey)
	}
	ttl := cfg.TokenTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Signer{
		issuer:    strings.TrimRight(cfg.IssuerURL, "/"),
		audiences: append([]string(nil), cfg.Audiences...),
		keyID:     keyID,
		key:       key,
		ttl:       ttl,
	}, nil
}

// Issuer returns the issuer URL this signer publishes.
func (s *Signer) Issuer() string { return s.issuer }

// KeyID returns the signing key id.
func (s *Signer) KeyID() string { return s.keyID }

// SignClaims turns a platform identity into an OIDC access token.
//
// The claim names are the ones a Kubernetes JWT authenticator maps by default:
// `sub` is the username, `groups` is the group list, and `preferred_username`
// is a display name. The onexai.io/* extras are carried for a reader that wants
// the platform's own attributes.
func (s *Signer) SignClaims(claims *bearer.Claims) (string, error) {
	if claims == nil || claims.UserID == "" {
		return "", errors.New("oidc: claims carry no user identity")
	}

	now := time.Now()
	expiresAt := now.Add(s.ttl)
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	groups := []string{"system:authenticated"}
	if claims.IsAdmin {
		groups = append(groups, "system:masters", "onexai:admin")
	}
	if claims.IsSRE {
		groups = append(groups, "onexai:sre")
	}

	payload := jwt.MapClaims{
		"iss":                s.issuer,
		"aud":                s.audiences,
		"sub":                claims.UserID,
		"iat":                now.Unix(),
		"nbf":                now.Unix(),
		"exp":                expiresAt.Unix(),
		"preferred_username": claims.Name,
		"groups":             groups,
	}
	addClaim(payload, "onexai.io/name", claims.Name)
	addClaim(payload, "onexai.io/provider", claims.Provider)
	addClaim(payload, "onexai.io/open-id", claims.OpenID)
	addClaim(payload, "onexai.io/account-role", claims.AccountRole)
	if claims.ExternalAdmin {
		payload["onexai.io/external-admin"] = "true"
	}
	if claims.ExternalSRE {
		payload["onexai.io/external-sre"] = "true"
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, payload)
	token.Header["kid"] = s.keyID
	return token.SignedString(s.key)
}

// Verified is the identity a token carries.
type Verified struct {
	Subject string
	Groups  []string
	Claims  map[string]any
}

// Verify checks a token this signer issued. It is used by tests and by callers
// that validate locally; a production consumer should use the Kubernetes OIDC
// authenticator against Discovery and JWKS instead.
func (s *Signer) Verify(tokenStr string) (*Verified, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, jwt.ErrSignatureInvalid
		}
		return &s.key.PublicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("oidc: verify token: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("oidc: token is not valid")
	}

	if issuer, _ := claims["iss"].(string); issuer != s.issuer {
		return nil, fmt.Errorf("oidc: issuer %q does not match %q", issuer, s.issuer)
	}
	if !audienceMatches(claims["aud"], s.audiences) {
		return nil, errors.New("oidc: audience does not match")
	}
	if exp, ok := numericClaim(claims["exp"]); ok && time.Now().After(time.Unix(exp, 0)) {
		return nil, errors.New("oidc: token is expired")
	}

	verified := &Verified{Claims: map[string]any(claims)}
	verified.Subject, _ = claims["sub"].(string)
	verified.Groups = stringList(claims["groups"])
	return verified, nil
}

// Discovery returns the OpenID Provider configuration document.
func (s *Signer) Discovery() map[string]any {
	return map[string]any{
		"issuer":                                s.issuer,
		"jwks_uri":                              s.issuer + "/keys",
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups"},
		"claims_supported":                      []string{"sub", "iss", "aud", "exp", "iat", "groups", "preferred_username"},
	}
}

// JWKS returns the public key set.
func (s *Signer) JWKS() map[string]any {
	pub := &s.key.PublicKey
	return map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": s.keyID,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
}

// DiscoveryHandler serves /.well-known/openid-configuration.
func (s *Signer) DiscoveryHandler() gin.HandlerFunc {
	return func(c *gin.Context) { c.JSON(http.StatusOK, s.Discovery()) }
}

// JWKSHandler serves /keys.
func (s *Signer) JWKSHandler() gin.HandlerFunc {
	return func(c *gin.Context) { c.JSON(http.StatusOK, s.JWKS()) }
}

// PublicKeyPEM returns the verification key in PEM form, for a consumer that
// prefers a static key over discovery.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func addClaim(payload jwt.MapClaims, key, value string) {
	if value != "" {
		payload[key] = value
	}
}

func loadOrGenerateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	if len(privateKeyPEM) == 0 {
		return rsa.GenerateKey(rand.Reader, 2048)
	}
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("oidc: private key is not PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("oidc: private key is not RSA")
		}
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("oidc: private key is not PKCS#1 or PKCS#8 RSA")
}

func keyIDFor(pub *rsa.PublicKey) string {
	sum := sha256.Sum256(pub.N.Bytes())
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

func audienceMatches(value any, audiences []string) bool {
	got := stringList(value)
	for _, candidate := range got {
		for _, want := range audiences {
			if candidate == want {
				return true
			}
		}
	}
	return false
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func numericClaim(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case jsonNumber:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// jsonNumber is the small part of encoding/json.Number numericClaim needs,
// named locally so this file does not import encoding/json just for a type.
type jsonNumber interface {
	Int64() (int64, error)
}
