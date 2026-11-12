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

package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchCard serves the published card and decodes it the way a client would.
func fetchCard(t *testing.T, opts Options, headers map[string]string) map[string]any {
	t.Helper()

	handlers, err := NewHandlers(nil, opts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, PathCard, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handlers.Card.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var card map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &card))
	return card
}

// interfaceURLs reads the URLs the card advertises.
func interfaceURLs(t *testing.T, card map[string]any) []string {
	t.Helper()

	interfaces, ok := card["supportedInterfaces"].([]any)
	require.True(t, ok, "the card carries no interfaces")

	var urls []string
	for _, entry := range interfaces {
		fields, ok := entry.(map[string]any)
		require.True(t, ok)
		url, ok := fields["url"].(string)
		require.True(t, ok)
		urls = append(urls, url)
	}
	return urls
}

// TestCardDecodesForAStrictClient pins the two fields a strict client refuses:
// skills must be an array rather than null, and the card must say which
// credential the endpoints require.
func TestCardDecodesForAStrictClient(t *testing.T) {
	card := fetchCard(t, Options{Version: "v9.9.9"}, nil)

	skills, ok := card["skills"].([]any)
	require.True(t, ok, "skills = %v, want an array (null is refused by strict clients)", card["skills"])
	assert.Empty(t, skills)

	schemes, ok := card["securitySchemes"].(map[string]any)
	require.True(t, ok, "the card must declare the scheme the endpoints enforce")
	assert.Contains(t, schemes, akskScheme)

	requirements, ok := card["securityRequirements"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, requirements)

	assert.Equal(t, "v9.9.9", card["version"])
}

// TestPathCardIsTheProtocolWellKnownPath pins discovery to the A2A spec
// constant and to the origin root. It must not be nested under the transport
// mount: a client fetches it before it knows any transport URL.
func TestPathCardIsTheProtocolWellKnownPath(t *testing.T) {
	assert.Equal(t, a2asrv.WellKnownAgentCardPath, PathCard)
	assert.Equal(t, "/.well-known/agent-card.json", PathCard)
	assert.False(t, strings.HasPrefix(PathCard, Path))
}

// TestCardAdvertisesTheSurfacePathByDefault covers the deployment that named no
// public URL: the card is path-relative, which a same-origin client can use.
func TestCardAdvertisesTheSurfacePathByDefault(t *testing.T) {
	card := fetchCard(t, Options{}, nil)

	assert.Equal(t, []string{Path, PathREST}, interfaceURLs(t, card))
}

// TestCardAdvertisesTheConfiguredPublicURL covers the deployment that decided
// its address.
func TestCardAdvertisesTheConfiguredPublicURL(t *testing.T) {
	card := fetchCard(t, Options{PublicURL: "https://onexai.example.com/v1/chat/a2a/"}, nil)

	assert.Equal(t, []string{
		"https://onexai.example.com/v1/chat/a2a",
		"https://onexai.example.com/v1/chat/a2a/rest",
	}, interfaceURLs(t, card))
}

// TestCardDerivesItsURLOnlyFromATrustedProxy pins the opt-in: the forwarded
// headers are believed when the deployment says a proxy sets them, and ignored
// otherwise, because the card route is unauthenticated.
func TestCardDerivesItsURLOnlyFromATrustedProxy(t *testing.T) {
	headers := map[string]string{
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "peers.example.com",
	}

	trusted := fetchCard(t, Options{TrustedProxy: true}, headers)
	assert.Equal(t, []string{
		"https://peers.example.com" + Path,
		"https://peers.example.com" + PathREST,
	}, interfaceURLs(t, trusted))

	untrusted := fetchCard(t, Options{}, headers)
	assert.Equal(t, []string{Path, PathREST}, interfaceURLs(t, untrusted))
}

// TestCardKeepsTheConfiguredURLEvenBehindATrustedProxy: an explicit address is
// the deployment's decision, so headers do not override it.
func TestCardKeepsTheConfiguredURLEvenBehindATrustedProxy(t *testing.T) {
	card := fetchCard(t, Options{PublicURL: "https://onexai.example.com/v1/chat/a2a", TrustedProxy: true},
		map[string]string{"X-Forwarded-Host": "attacker.example.com"})

	assert.Equal(t, []string{
		"https://onexai.example.com/v1/chat/a2a",
		"https://onexai.example.com/v1/chat/a2a/rest",
	}, interfaceURLs(t, card))
}

// TestNewHandlersRefusesARelativePublicURL: a card whose URL cannot be resolved
// fails at the peer's first call, where it reads as this service being down.
func TestNewHandlersRefusesARelativePublicURL(t *testing.T) {
	_, err := NewHandlers(nil, Options{PublicURL: "/v1/chat/a2a"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}
