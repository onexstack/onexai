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

package dutyschedule

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	chatstore "github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestScheduleLifecycle drives the biz layer over a real SQL store. It covers
// the path the console takes: create a definition, read the current roster,
// preview, add an override and an unavailability, then save an escalation
// policy. The evaluator and store have their own unit tests; this one checks
// the conversions between them and the REST types.
func TestScheduleLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := chatstore.NewStore(db)

	require.NoError(t, st.OncallGroups().Create(ctx, &model.OncallGroupM{
		Name:        "go",
		DisplayName: "Go 服务",
		Status:      "enabled",
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}))
	biz := New(st, nil, nil, nil)

	// Before a definition exists the group is reported as unconfigured.
	initial, err := biz.Get(ctx, &api.GetOncallScheduleRequest{Group: "go"})
	require.NoError(t, err)
	assert.False(t, initial.Configured)
	assert.Equal(t, "none", initial.Source)

	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	saved, err := biz.SaveDefinition(ctx, &api.SaveOncallScheduleRequest{
		Group: "go",
		OncallScheduleDefinition: api.OncallScheduleDefinition{
			Timezone: "UTC",
			Layers: []api.OncallScheduleLayer{{
				Name:                 "Primary",
				RotationType:         model.RotationWeekly,
				RotationStart:        start,
				ShiftDurationSeconds: 7 * 86400,
				Participants: []api.OncallScheduleParticipant{
					{UserName: "alice@example.com"},
					{UserName: "bob@example.com"},
				},
			}},
		},
	})
	require.NoError(t, err)
	assert.True(t, saved.Configured)
	assert.Equal(t, "native", saved.Source)
	require.NotNil(t, saved.Schedule)
	require.Len(t, saved.Schedule.Layers, 1)
	assert.Equal(t, "alice@example.com", saved.Schedule.Layers[0].Participants[0].UserName)

	// 2026-11-06 is in alice's week (slot 0).
	current, err := biz.Current(ctx, &api.GetOncallCurrentRequest{
		Group:  "go",
		AtTime: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, current.Current)
	assert.Equal(t, "alice@example.com", current.Current.UserName)
	assert.Equal(t, "UTC", current.Timezone)
	assert.False(t, current.Current.IsOverride)

	// 2026-11-09 starts bob's week.
	current, err = biz.Current(ctx, &api.GetOncallCurrentRequest{
		Group:  "go",
		AtTime: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, current.Current)
	assert.Equal(t, "bob@example.com", current.Current.UserName)

	list, err := biz.ListCurrent(ctx, &api.ListOncallCurrentRequest{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "go", list.Items[0].Group)

	timeline, err := biz.Timeline(ctx, &api.GetOncallTimelineRequest{
		Group:    "go",
		FromTime: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		ToTime:   time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotEmpty(t, timeline.Segments)
	assert.Equal(t, "alice@example.com", timeline.Segments[0].UserName)
	assert.Equal(t, "bob@example.com", timeline.Segments[len(timeline.Segments)-1].UserName)

	layers, err := biz.LayerTimelines(ctx, &api.GetOncallLayerTimelinesRequest{
		Group:    "go",
		FromTime: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		ToTime:   time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Contains(t, layers.Layers, "Primary")
	assert.NotEmpty(t, layers.Effective)

	preview, err := biz.Preview(ctx, &api.PreviewOncallScheduleRequest{
		Group: "go",
		From:  time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		OncallScheduleDefinition: api.OncallScheduleDefinition{
			Timezone: "UTC",
			Layers: []api.OncallScheduleLayer{{
				Name:                 "Preview",
				RotationType:         model.RotationDaily,
				RotationStart:        start,
				ShiftDurationSeconds: 86400,
				Participants:         []api.OncallScheduleParticipant{{UserName: "preview@example.com"}},
			}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, preview.Segments)
	assert.Equal(t, "preview@example.com", preview.Segments[0].UserName)
	// Preview must not persist.
	afterPreview, err := biz.Get(ctx, &api.GetOncallScheduleRequest{Group: "go"})
	require.NoError(t, err)
	require.NotNil(t, afterPreview.Schedule)
	require.Len(t, afterPreview.Schedule.Layers, 1)
	assert.Equal(t, "Primary", afterPreview.Schedule.Layers[0].Name)

	override, err := biz.CreateOverride(ctx, &api.CreateOncallOverrideRequest{
		Group:        "go",
		OverrideUser: "carol@example.com",
		StartTime:    time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		CreatedBy:    "tester",
	})
	require.NoError(t, err)
	require.NotEmpty(t, override.ID)

	current, err = biz.Current(ctx, &api.GetOncallCurrentRequest{
		Group:  "go",
		AtTime: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, current.Current)
	assert.Equal(t, "carol@example.com", current.Current.UserName)
	assert.True(t, current.Current.IsOverride)

	_, err = biz.CreateUnavailability(ctx, &api.CreateOncallUnavailabilityRequest{
		Group:     "go",
		UserName:  "bob@example.com",
		StartDate: "2026-11-09",
		EndDate:   "2026-11-09",
	})
	require.NoError(t, err)
	current, err = biz.Current(ctx, &api.GetOncallCurrentRequest{
		Group:  "go",
		AtTime: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, current.Current)
	assert.Equal(t, "alice@example.com", current.Current.UserName, "the unavailable bob is skipped in the same slot")

	escalation, err := biz.SaveEscalation(ctx, &api.SaveOncallEscalationRequest{
		Group: "go",
		OncallEscalationPolicy: api.OncallEscalationPolicy{
			Enabled:      true,
			AutoEscalate: true,
			Tiers: []api.OncallEscalationTier{
				{TimeoutSeconds: 300, TargetType: model.EscalationTargetSchedule},
				{TimeoutSeconds: 600, TargetType: model.EscalationTargetUsers, UserNames: []string{"ops@example.com"}},
			},
		},
	})
	require.NoError(t, err)
	assert.True(t, escalation.Configured)
	require.NotNil(t, escalation.Policy)
	require.Len(t, escalation.Policy.Tiers, 2)
	assert.Equal(t, []string{"ops@example.com"}, escalation.Policy.Tiers[1].UserNames)

	// Deleting the override restores the rotation.
	_, err = biz.DeleteOverride(ctx, &api.DeleteOncallOverrideRequest{Group: "go", OverrideID: override.ID})
	require.NoError(t, err)
	current, err = biz.Current(ctx, &api.GetOncallCurrentRequest{
		Group:  "go",
		AtTime: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, current.Current)
	assert.Equal(t, "alice@example.com", current.Current.UserName)
}

func openTestDB(t *testing.T) *gorm.DB {
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
