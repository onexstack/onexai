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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRevisionRoutesAreServed checks the two routes exist on the signed face.
// They are read-only views of what has run, so they belong there rather than on
// the management surface.
func TestRevisionRoutesAreServed(t *testing.T) {
	routes := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyTo(g) })

	assert.Contains(t, routes, "GET /agent/revisions")
	assert.Contains(t, routes, "GET /agent/revisions/:id")
}

// TestRevisionReadsNeedARunStore pins the answer a deployment without a database
// gets: a clear 404, not a panic and not a 500.
func TestRevisionReadsNeedARunStore(t *testing.T) {
	h := NewHandler(Deps{Biz: newTestBiz(nil)})

	c, recorder := newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/agent/revisions", nil)
	h.ListAgentRevision(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "run store is not configured")

	c, recorder = newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/agent/revisions/rev-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "rev-1"}}
	h.GetAgentRevision(c)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "run store is not configured")
}
