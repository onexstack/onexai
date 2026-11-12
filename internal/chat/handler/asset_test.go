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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chatbiz "github.com/onexstack/onexai/internal/chat/biz"
	chatsvc "github.com/onexstack/onexai/internal/chat/biz/v1/chat"
	userbiz "github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// fakeChatBiz implements only the two methods this surface uses; the rest of
// the interface is left nil, so a route that grew a dependency on another part
// of the service panics here rather than in production.
type fakeChatBiz struct {
	chatsvc.ChatBiz
	uploads  []*agentv1.CreateAssetRequest
	asset    *agentv1.Asset
	content  []byte
	fetchErr error
}

func (f *fakeChatBiz) UploadAsset(_ context.Context, req *agentv1.CreateAssetRequest) (*agentv1.Asset, error) {
	f.uploads = append(f.uploads, req)
	if f.asset != nil {
		return f.asset, nil
	}
	return &agentv1.Asset{AssetId: "img_stored", Url: "/media/x", MimeType: "image/png"}, nil
}

func (f *fakeChatBiz) FetchAssetContent(_ context.Context, _ string) ([]byte, string, error) {
	if f.fetchErr != nil {
		return nil, "", f.fetchErr
	}
	return f.content, "image/png", nil
}

type fakeIBiz struct {
	chatbiz.IBiz
	chat *fakeChatBiz
}

func (f *fakeIBiz) ChatV1() chatsvc.ChatBiz { return f.chat }

// UserV1 is nil: these tests exercise routes whose auth middleware asks for an
// account status, and a nil answer is what a deployment without the user store
// returns.
func (f *fakeIBiz) UserV1() userbiz.UserBiz { return nil }

func newAssetEngine(t *testing.T) (*gin.Engine, *fakeChatBiz) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	chat := &fakeChatBiz{}
	engine := gin.New()
	engine.Use(gin.Recovery())
	h := &Handler{biz: &fakeIBiz{chat: chat}, JWTSecret: ticketTestSecret}
	h.ApplyTo(engine.Group("/v1"))
	return engine, chat
}

func postAsset(engine *gin.Engine, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/assets", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// TestUploadAssetIsAuthenticated pins the door. An upload with no token must be
// refused before anything is stored — the route lives on the browser-facing
// group precisely because the identity it stamps comes from the token.
func TestUploadAssetIsAuthenticated(t *testing.T) {
	engine, chat := newAssetEngine(t)
	require.True(t, routeExists(engine, http.MethodPost, "/v1/assets"),
		"POST /v1/assets is not registered")

	rec := postAsset(engine, "", `{"kind":"image","data":"AAAA"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, chat.uploads, "an unauthenticated upload reached the store")
}

// TestUploadAssetStampsTheCallersIdentity is the reason this route is a proxy
// rather than a pass-through.
//
// The backend trusts its callers about ownership: they are services with a
// signed request and it has nothing else to go on. This service is the one that
// knows who is actually asking, so it overwrites whatever the body claimed. A
// passthrough would let any caller store an image under somebody else's
// identity — and, because ownership is what a later read is checked against,
// read it back as them.
func TestUploadAssetStampsTheCallersIdentity(t *testing.T) {
	engine, chat := newAssetEngine(t)
	token := ticketToken(t, bearer.Claims{UserID: "go.kong", Provider: "local"})

	rec := postAsset(engine, token,
		`{"kind":"image","filename":"a.png","ownerID":"somebody.else","data":"AAAA","source":"lark"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, chat.uploads, 1)
	got := chat.uploads[0]
	assert.Equal(t, "go.kong", got.GetOwnerID(),
		"the owner must come from the token, not from the body")
	assert.Equal(t, "image", got.GetKind())
	assert.Equal(t, "a.png", got.GetFilename())
	// The source a client set is kept: it describes where the bytes came from,
	// which the token says nothing about.
	assert.Equal(t, "lark", got.GetSource())

	var resp struct {
		Asset *agentv1.Asset `json:"asset"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Asset)
	assert.Equal(t, "img_stored", resp.Asset.GetAssetId())
}

// TestUploadAssetDefaultsTheSource pins that a caller which says nothing about
// where its bytes came from is recorded as an upload rather than as a blank.
func TestUploadAssetDefaultsTheSource(t *testing.T) {
	engine, chat := newAssetEngine(t)
	token := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})

	require.Equal(t, http.StatusOK, postAsset(engine, token, `{"kind":"image","data":"AAAA"}`).Code)
	require.Len(t, chat.uploads, 1)
	assert.Equal(t, "upload", chat.uploads[0].GetSource())
}

// TestUploadAssetRejectsAMalformedBody pins that a body the backend could not
// use is refused here, where the reason is still this service's to give.
func TestUploadAssetRejectsAMalformedBody(t *testing.T) {
	engine, chat := newAssetEngine(t)
	token := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})

	rec := postAsset(engine, token, `{"kind":"image","data":`)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.Empty(t, chat.uploads)
}

// TestGetAssetContentServesBytes pins the read half of the same window: a
// client rendering a message that carries a reference resolves it here.
func TestGetAssetContentServesBytes(t *testing.T) {
	engine, chat := newAssetEngine(t)
	chat.content = []byte("PNGBYTES")
	token := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})

	req := httptest.NewRequest(http.MethodGet, "/v1/assets/img_a/content", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PNGBYTES", rec.Body.String())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))

	// And it is behind the same door as the upload.
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/assets/img_a/content", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
