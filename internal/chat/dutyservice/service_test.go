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

package dutyservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/chat/biz/v1/dutyschedule"
	"github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	chatstore "github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// openTestDB opens the one in-memory database this package's tests share.
//
// chatstore.NewStore is a process-wide singleton (it sets its package-level S
// once), so a second call would hand back the first test's db after that test
// closed it. The DB is therefore opened once for the whole test function and
// the subtests use distinct group names.
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

func TestServiceOverSQL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := chatstore.NewStore(db)
	newGroup := func(name, scheduleID string) {
		require.NoError(t, st.OncallGroups().Create(ctx, &model.OncallGroupM{
			Name:               name,
			DisplayName:        strings.ToUpper(name) + " 服务",
			Status:             "enabled",
			Agents:             `["@oncall"]`,
			ScheduleID:         scheduleID,
			AllocationStrategy: "primary",
			CreatedAt:          time.Now().UnixMilli(),
			UpdatedAt:          time.Now().UnixMilli(),
		}))
	}

	// The empty-table case must run first: it is the state before any group
	// row exists, and it is the case that delegates entirely to the legacy
	// store (an orphan Regen schedule).
	t.Run("empty group table falls back to legacy", func(t *testing.T) {
		legacy := duty.New()
		legacy.RegisterGroup(&v1.OnDutyGroup{GroupId: "orphan", GroupName: "Orphan"})
		legacy.SetOncall("orphan", []*v1.Watcher{{UserId: "u1", UserName: "orphan@example.com"}})

		svc := New(st.OncallGroups(), st.OncallSchedules(), legacy, dutyschedule.New(st, nil, nil, nil), nil)
		active, err := svc.ActiveGroupInfos(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, "orphan", active[0].ID)
	})

	t.Run("native roster is read live", func(t *testing.T) {
		newGroup("go", "")
		// A daily rotation that started an hour ago puts alice on call now,
		// regardless of when the test runs.
		start := time.Now().UTC().Add(-time.Hour)
		require.NoError(t, st.OncallSchedules().SaveDefinition(ctx, &model.OncallScheduleDefinition{
			Schedule: &model.OncallScheduleM{
				GroupName: "go",
				Timezone:  "UTC",
				CreatedAt: time.Now().UnixMilli(),
				UpdatedAt: time.Now().UnixMilli(),
			},
			Layers: []*model.OncallScheduleLayerM{{
				GroupName:            "go",
				Name:                 "Primary",
				OrderIndex:           0,
				RotationType:         model.RotationDaily,
				RotationStart:        start,
				ShiftDurationSeconds: 86400,
			}},
			Participants: map[int][]*model.OncallScheduleParticipantM{
				0: {
					{UserName: "alice@example.com", OrderIndex: 0},
					{UserName: "bob@example.com", OrderIndex: 1},
				},
			},
		}))

		biz := dutyschedule.New(st, nil, nil, nil)
		svc := New(st.OncallGroups(), st.OncallSchedules(), nil, biz,
			userdir.Static{"alice@example.com": "u-alice"})

		group, ok, err := svc.GetGroupInfo(ctx, "GO")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "go", group.ID)
		assert.Equal(t, "native", group.ScheduleSource)
		require.Len(t, group.Watchers, 1)
		assert.Equal(t, "alice@example.com", group.Watchers[0].UserName)
		assert.Equal(t, "u-alice", group.Watchers[0].UserId)

		meta, ok, err := svc.GetGroupMeta(ctx, "go")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []string{"@oncall"}, meta.Agents)
		assert.Empty(t, meta.Watchers, "GetGroupMeta must not evaluate the rotation")

		current, err := svc.GetCurrent(ctx, "go", start.Add(25*time.Hour))
		require.NoError(t, err)
		require.NotNil(t, current.Current)
		assert.Equal(t, "bob@example.com", current.Current.UserName)

		// DisplayName differs from the stored name; the explicit-instant list
		// path must still resolve the schedule by the stored name.
		listed, err := svc.ListCurrent(ctx, []string{"go"}, start.Add(25*time.Hour))
		require.NoError(t, err)
		require.Len(t, listed.Items, 1)
		require.NotNil(t, listed.Items[0].Current)
		assert.Equal(t, "bob@example.com", listed.Items[0].Current.UserName)
	})

	t.Run("regen group falls back to the legacy store", func(t *testing.T) {
		newGroup("legacy", "regen-1")

		legacy := duty.New()
		legacy.RegisterGroup(&v1.OnDutyGroup{GroupId: "legacy", GroupName: "Legacy"})
		legacy.SetOncall("legacy", []*v1.Watcher{{UserId: "u1", UserName: "legacy@example.com"}})

		svc := New(st.OncallGroups(), st.OncallSchedules(), legacy, dutyschedule.New(st, nil, nil, nil), nil)
		group, ok, err := svc.GetGroupInfo(ctx, "legacy")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "regen", group.ScheduleSource)
		require.Len(t, group.Watchers, 1)
		assert.Equal(t, "legacy@example.com", group.Watchers[0].UserName)
	})
}

func TestLegacyOnlyCannotServeSchedules(t *testing.T) {
	svc := NewLegacyOnly(duty.New())
	_, err := svc.GetSchedule(context.Background(), "go")
	require.Error(t, err)
	assert.True(t, errors.Is(err, duty.ErrScheduleUnavailable))
}

func TestFilterGroupsRejectsBadPattern(t *testing.T) {
	svc := NewLegacyOnly(nil)
	_, ok, err := svc.FilterGroups(context.Background(), "[")
	require.NoError(t, err)
	assert.False(t, ok)
}

// Compile-time guard: the service must keep implementing the command-facing
// interface even after edits to either side.
var _ duty.Service = (*service)(nil)
