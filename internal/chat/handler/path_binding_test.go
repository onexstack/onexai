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
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/api"
)

// TestPathParamsBindFromURI pins the contract between the route parameter names
// and the `uri:"..."` tags on the hand-written chat request types. A missing or
// misspelt tag leaves the field empty and only surfaces later as a
// "xxx is required" 400, so it is checked here instead.
func TestPathParamsBindFromURI(t *testing.T) {
	tests := []struct {
		caseName string
		params   gin.Params
		rq       any
		field    string
		want     string
	}{
		{"GetOncallGroupRequest", gin.Params{{Key: "name", Value: "g"}}, &api.GetOncallGroupRequest{}, "Name", "g"},
		{"DeleteOncallGroupRequest", gin.Params{{Key: "name", Value: "g"}}, &api.DeleteOncallGroupRequest{}, "Name", "g"},
		{"ListOncallOverridesRequest", gin.Params{{Key: "name", Value: "g"}}, &api.ListOncallOverridesRequest{}, "Group", "g"},
		{"DeleteOncallOverrideRequest.group", gin.Params{{Key: "name", Value: "g"}}, &api.DeleteOncallOverrideRequest{}, "Group", "g"},
		{"DeleteOncallOverrideRequest.id", gin.Params{{Key: "overrideID", Value: "o"}}, &api.DeleteOncallOverrideRequest{}, "OverrideID", "o"},
		{"ListOncallUnavailabilitiesRequest", gin.Params{{Key: "name", Value: "g"}}, &api.ListOncallUnavailabilitiesRequest{}, "Group", "g"},
		{"DeleteOncallUnavailabilityRequest.group", gin.Params{{Key: "name", Value: "g"}}, &api.DeleteOncallUnavailabilityRequest{}, "Group", "g"},
		{"DeleteOncallUnavailabilityRequest.id", gin.Params{{Key: "unavailabilityID", Value: "u"}}, &api.DeleteOncallUnavailabilityRequest{}, "UnavailabilityID", "u"},
		{"ListOncallHolidaysRequest", gin.Params{{Key: "name", Value: "g"}}, &api.ListOncallHolidaysRequest{}, "Group", "g"},
		{"DeleteOncallHolidayRequest.group", gin.Params{{Key: "name", Value: "g"}}, &api.DeleteOncallHolidayRequest{}, "Group", "g"},
		{"DeleteOncallHolidayRequest.id", gin.Params{{Key: "holidayID", Value: "h"}}, &api.DeleteOncallHolidayRequest{}, "HolidayID", "h"},
		{"GetOncallEscalationRequest", gin.Params{{Key: "name", Value: "g"}}, &api.GetOncallEscalationRequest{}, "Group", "g"},
		{"PeerAgentRequest.backend", gin.Params{{Key: "backend", Value: "feishu"}}, &api.PeerAgentRequest{}, "Backend", "feishu"},
		{"PeerAgentRequest.scope", gin.Params{{Key: "scope", Value: "tenant"}}, &api.PeerAgentRequest{}, "Scope", "tenant"},
		{"PeerAgentRequest.peerID", gin.Params{{Key: "peerID", Value: "p"}}, &api.PeerAgentRequest{}, "PeerID", "p"},
	}

	for _, tt := range tests {
		t.Run(tt.caseName, func(t *testing.T) {
			c, _ := newChatRecorder(t)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Params = tt.params

			require.NoError(t, c.ShouldBindUri(tt.rq))
			assert.Equal(t, tt.want, chatStringField(t, tt.rq, tt.field))
		})
	}
}

func newChatRecorder(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	return c, recorder
}

func chatStringField(t *testing.T, rq any, field string) string {
	t.Helper()
	v := reflect.ValueOf(rq).Elem().FieldByName(field)
	require.True(t, v.IsValid(), "field %q does not exist on %T", field, rq)
	require.Equal(t, reflect.String, v.Kind(), "field %q is not a string", field)
	return v.String()
}
