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

	"github.com/onexstack/onexai/internal/chat/api"
	chatbiz "github.com/onexstack/onexai/internal/chat/biz"
	bizcommand "github.com/onexstack/onexai/internal/chat/biz/v1/command"
	userbiz "github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// fakeCommandBiz is the read-only catalogue the handler test drives.
type fakeCommandBiz struct {
	rows []*v1.Command
}

func (f *fakeCommandBiz) Get(_ context.Context, req *api.GetCommandRequest) (*v1.GetCommandResponse, error) {
	for _, row := range f.rows {
		if row.GetName() == req.Name {
			return &v1.GetCommandResponse{Command: row}, nil
		}
	}
	return nil, errno.ErrNotFound.WithMessage("command %q not found", req.Name)
}

func (f *fakeCommandBiz) List(_ context.Context, req *v1.ListCommandRequest) (*v1.ListCommandResponse, error) {
	offset, limit := int(req.GetOffset()), int(req.GetLimit())
	out := f.rows
	if offset > 0 && offset < len(out) {
		out = out[offset:]
	} else if offset >= len(out) {
		out = nil
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return &v1.ListCommandResponse{Commands: out, Count: int64(len(f.rows))}, nil
}

type fakeCommandIBiz struct {
	chatbiz.IBiz
	command bizcommand.CommandBiz
}

func (f *fakeCommandIBiz) CommandV1() bizcommand.CommandBiz { return f.command }

func (f *fakeCommandIBiz) UserV1() userbiz.UserBiz { return nil }

func newCommandEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &Handler{
		biz: &fakeCommandIBiz{command: &fakeCommandBiz{rows: []*v1.Command{
			{Name: "bind", Namespace: "default", Group: "core", Source: "builtin"},
			{Name: "skill", Namespace: "default", Group: "core", Source: "builtin"},
		}}},
		JWTSecret: ticketTestSecret,
	}
	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))
	return engine
}

func getCommandResource(t *testing.T, engine *gin.Engine, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestCommandCatalogueIsReadable(t *testing.T) {
	engine := newCommandEngine(t)
	token := ticketToken(t, bearer.Claims{UserID: "user-1", Provider: "test"})

	rec := getCommandResource(t, engine, "/v1/chat/commands", token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed v1.ListCommandResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	assert.Equal(t, int64(2), listed.GetCount())
	assert.Len(t, listed.GetCommands(), 2)

	rec = getCommandResource(t, engine, "/v1/chat/commands?offset=1&limit=1", token)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	assert.Equal(t, int64(2), listed.GetCount())
	require.Len(t, listed.GetCommands(), 1)
	assert.Equal(t, "skill", listed.GetCommands()[0].GetName())

	rec = getCommandResource(t, engine, "/v1/chat/commands/bind?namespace=default", token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got v1.GetCommandResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "bind", got.GetCommand().GetName())

	rec = getCommandResource(t, engine, "/v1/chat/commands/missing", token)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCommandCatalogueRequiresAuthenticationAndHasNoWritePath(t *testing.T) {
	engine := newCommandEngine(t)

	rec := getCommandResource(t, engine, "/v1/chat/commands", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// There is deliberately no write route: commandreg would undo it.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/commands", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ticketToken(t, bearer.Claims{UserID: "admin", IsAdmin: true}))
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
