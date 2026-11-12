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

package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

const (
	testIssuer   = "https://auth.example.com"
	testAudience = "onexai"
)

func newSigner(t *testing.T, audiences ...string) *Signer {
	t.Helper()
	if len(audiences) == 0 {
		audiences = []string{testAudience}
	}
	signer, err := New(Config{IssuerURL: testIssuer, Audiences: audiences})
	require.NoError(t, err)
	return signer
}

func TestSignAndVerify(t *testing.T) {
	signer := newSigner(t)
	token, err := signer.SignClaims(&bearer.Claims{
		UserID:   "go.kong",
		Name:     "Kong",
		Provider: "feishu",
		IsAdmin:  true,
		IsSRE:    true,
	})
	require.NoError(t, err)

	verified, err := signer.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, "go.kong", verified.Subject)
	assert.Contains(t, verified.Groups, "system:authenticated")
	assert.Contains(t, verified.Groups, "system:masters")
	assert.Contains(t, verified.Groups, "onexai:admin")
	assert.Contains(t, verified.Groups, "onexai:sre")
	assert.Equal(t, "Kong", verified.Claims["preferred_username"])
	assert.Equal(t, "feishu", verified.Claims["onexai.io/provider"])
}

func TestVerifyRejectsAnotherIssuer(t *testing.T) {
	signer := newSigner(t)
	other, err := New(Config{IssuerURL: "https://other.example.com", Audiences: []string{testAudience}, PrivateKeyPEM: signerPrivatePEM(t, signer)})
	require.NoError(t, err)
	token, err := other.SignClaims(&bearer.Claims{UserID: "go.kong"})
	require.NoError(t, err)

	_, err = signer.Verify(token)
	assert.ErrorContains(t, err, "issuer")
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	signer := newSigner(t)
	token := signMapClaims(t, signer, jwt.MapClaims{
		"iss": testIssuer,
		"aud": []string{"another"},
		"sub": "go.kong",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	_, err := signer.Verify(token)
	assert.ErrorContains(t, err, "audience")
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	signer := newSigner(t)
	token, err := signer.SignClaims(&bearer.Claims{
		UserID: "go.kong",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
	require.NoError(t, err)

	_, err = signer.Verify(token)
	assert.Error(t, err)
}

func TestDiscoveryAndJWKS(t *testing.T) {
	signer := newSigner(t)
	discovery := signer.Discovery()
	assert.Equal(t, testIssuer, discovery["issuer"])
	assert.Equal(t, testIssuer+"/keys", discovery["jwks_uri"])

	jwks := signer.JWKS()
	keys, ok := jwks["keys"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, keys, 1)
	assert.Equal(t, "RSA", keys[0]["kty"])
	assert.Equal(t, "RS256", keys[0]["alg"])
	assert.Equal(t, signer.KeyID(), keys[0]["kid"])
	assert.NotEmpty(t, keys[0]["n"])
	assert.Equal(t, "AQAB", keys[0]["e"])
}

func TestHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	signer := newSigner(t)

	engine := gin.New()
	engine.GET("/.well-known/openid-configuration", signer.DiscoveryHandler())
	engine.GET("/keys", signer.JWKSHandler())

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var discovery map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &discovery))
	assert.Equal(t, testIssuer, discovery["issuer"])

	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/keys", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var jwks map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &jwks))
	assert.NotEmpty(t, jwks["keys"])
}

func TestNewLoadsAPKCS8Key(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	signer, err := New(Config{IssuerURL: testIssuer, Audiences: []string{testAudience}, PrivateKeyPEM: keyPEM})
	require.NoError(t, err)
	token, err := signer.SignClaims(&bearer.Claims{UserID: "go.kong"})
	require.NoError(t, err)
	_, err = signer.Verify(token)
	require.NoError(t, err)
}

func TestNewRejectsInvalidInput(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)

	_, err = New(Config{IssuerURL: testIssuer})
	assert.Error(t, err)

	_, err = New(Config{IssuerURL: testIssuer, Audiences: []string{testAudience}, PrivateKeyPEM: []byte("not a key")})
	assert.Error(t, err)
}

func signerPrivatePEM(t *testing.T, signer *Signer) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(signer.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func signMapClaims(t *testing.T, signer *Signer, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = signer.KeyID()
	signed, err := token.SignedString(signer.key)
	require.NoError(t, err)
	return signed
}
