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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/biz"
	"github.com/onexstack/onexai/internal/chat/biz/v1/dutyschedule"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncallgroup"
	"github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	chatstore "github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/pkg/dbschema"
)

type fakeScheduleIBiz struct {
	biz.IBiz
	duty  dutyschedule.DutyScheduleBiz
	group oncallgroup.OncallGroupBiz
}

func (f *fakeScheduleIBiz) DutyScheduleV1() dutyschedule.DutyScheduleBiz { return f.duty }

func (f *fakeScheduleIBiz) OncallGroupV1() oncallgroup.OncallGroupBiz { return f.group }

// UserV1 is nil so the auth middleware's account-status lookup answers
// "unknown" instead of dereferencing the nil embedded interface.
func (f *fakeScheduleIBiz) UserV1() user.UserBiz { return nil }

// TestOncallScheduleHTTP drives the real handler, biz and SQL store through the
// router. It covers binding, validation, the response envelope and the
// admin-only write gate that the unit tests each see only in isolation.
func TestOncallScheduleHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := openHandlerTestDB(t)
	ctx := context.Background()
	st := chatstore.NewStore(db)
	require.NoError(t, st.OncallGroups().Create(ctx, &model.OncallGroupM{
		Name:        "go",
		DisplayName: "Go 服务",
		Status:      "enabled",
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}))
	h := &Handler{
		JWTSecret: ticketTestSecret,
		biz: &fakeScheduleIBiz{
			duty:  dutyschedule.New(st, nil, nil, nil),
			group: oncallgroup.New(st),
		},
		val: validation.New(st),
	}
	engine := gin.New()
	engine.Use(gin.Recovery())
	h.ApplyTo(engine.Group("/v1"))

	admin := ticketToken(t, bearer.Claims{UserID: "a1", Provider: "local", IsAdmin: true})

	body := `{
	  "timezone":"UTC",
	  "layers":[{
	    "name":"Primary",
	    "rotationType":"weekly",
	    "rotationStart":"2026-11-02T00:00:00Z",
	    "shiftDurationSeconds":604800,
	    "participants":[
	      {"userName":"alice@example.com","orderIndex":0},
	      {"userName":"bob@example.com","orderIndex":1}
	    ]
	  }]
	}`
	rec := oncallJSON(engine, http.MethodPut, "/v1/oncall-groups/go/schedule", admin, body)
	require.Equal(t, http.StatusOK, rec.Code, body+": "+rec.Body.String())
	var saved api.OncallScheduleResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
	assert.True(t, saved.Configured)
	assert.Equal(t, "native", saved.Source)

	rec = oncallJSON(engine, http.MethodGet,
		"/v1/oncall-groups/go/schedule/oncall?at=2026-11-06T10:00:00Z", admin, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var current api.OncallCurrentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &current))
	require.NotNil(t, current.Current)
	assert.Equal(t, "alice@example.com", current.Current.UserName)
	assert.Equal(t, "UTC", current.Timezone)

	rec = oncallJSON(engine, http.MethodGet, "/v1/oncall-current", admin, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list api.ListOncallCurrentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)

	rec = oncallJSON(engine, http.MethodGet,
		"/v1/oncall-groups/go/schedule/timeline?from=2026-11-02T00:00:00Z&to=2026-11-16T00:00:00Z",
		admin, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var timeline api.OncallTimelineResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &timeline))
	require.NotEmpty(t, timeline.Segments)
	assert.Equal(t, "alice@example.com", timeline.Segments[0].UserName)

	overrideBody := `{"overrideUser":"carol@example.com","startTime":"2026-11-03T00:00:00Z","endTime":"2026-11-04T00:00:00Z"}`
	rec = oncallJSON(engine, http.MethodPost, "/v1/oncall-groups/go/schedule/overrides", admin, overrideBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var override api.OncallScheduleOverride
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &override))
	require.NotEmpty(t, override.ID)
	assert.Equal(t, "a1", override.CreatedBy)

	rec = oncallJSON(engine, http.MethodGet,
		"/v1/oncall-groups/go/schedule/oncall?at=2026-11-03T12:00:00Z", admin, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &current))
	require.NotNil(t, current.Current)
	assert.Equal(t, "carol@example.com", current.Current.UserName)
	assert.True(t, current.Current.IsOverride)

	// A member may read the same endpoint.
	member := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})
	rec = oncallJSON(engine, http.MethodGet, "/v1/oncall-groups/go/schedule/oncall", member, "")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The write gate rejects a member before validation or the handler.
	rec = oncallJSON(engine, http.MethodPut, "/v1/oncall-groups/go/schedule", member, body)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Invalid definitions are rejected by the validator.
	bad := `{"timezone":"Mars/Olympus","layers":[]}`
	rec = oncallJSON(engine, http.MethodPut, "/v1/oncall-groups/go/schedule", admin, bad)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// A duplicate group name is a 409 with an actionable message, not the
	// InternalError.DBWrite 500 the unique index used to produce. The existing
	// row has no namespace — exactly the production shape the namespace-filtered
	// list hid.
	require.NoError(t, st.OncallGroups().Create(ctx, &model.OncallGroupM{
		Name:        "k8s",
		DisplayName: "K8s 值班组",
		Status:      "enabled",
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}))
	rec = oncallJSON(engine, http.MethodPost, "/v1/oncall-groups", admin,
		`{"name":"k8s","displayName":"K8s 值班组","namespace":"helper-public"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var conflict struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflict))
	assert.Equal(t, "AlreadyExists", conflict.Reason)
	assert.Contains(t, conflict.Message, `"k8s"`)
	assert.Contains(t, conflict.Message, "未设置")

	// A fresh name still creates, and the namespace from the body is stored.
	rec = oncallJSON(engine, http.MethodPost, "/v1/oncall-groups", admin,
		`{"name":"k8s-verify","displayName":"验证","namespace":"helper-public"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created, err := st.OncallGroups().GetByName(ctx, "k8s-verify")
	require.NoError(t, err)
	assert.Equal(t, "helper-public", created.Namespace)
}

func oncallJSON(
	engine *gin.Engine, method, path, token, body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func openHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbschema.Configure(dbschema.MySQL)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") || strings.Contains(err.Error(), "stub") {
			t.Skipf("sqlite requires cgo: %v", err)
		}
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.OncallGroupM{},
		&model.OncallScheduleM{},
		&model.OncallScheduleLayerM{},
		&model.OncallScheduleParticipantM{},
		&model.OncallScheduleOverrideM{},
		&model.OncallScheduleUnavailabilityM{},
		&model.OncallScheduleHolidayM{},
		&model.OncallEscalationTierM{},
	))
	return db
}
