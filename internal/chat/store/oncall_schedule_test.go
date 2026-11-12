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

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/model"
)

func newOncallScheduleTestStore(t *testing.T) *oncallScheduleStore {
	t.Helper()
	db := newStoreTestDB(t,
		&model.OncallScheduleM{},
		&model.OncallScheduleLayerM{},
		&model.OncallScheduleParticipantM{},
		&model.OncallScheduleOverrideM{},
		&model.OncallScheduleUnavailabilityM{},
		&model.OncallScheduleHolidayM{},
		&model.OncallEscalationTierM{},
	)
	return newOncallScheduleStore(&store{db: db})
}

func testDefinition(group string, layers int) *model.OncallScheduleDefinition {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	def := &model.OncallScheduleDefinition{
		Schedule: &model.OncallScheduleM{
			GroupName: group,
			Timezone:  "Asia/Shanghai",
			CreatedAt: time.Now().UnixMilli(),
			UpdatedAt: time.Now().UnixMilli(),
		},
		Participants: map[int][]*model.OncallScheduleParticipantM{},
	}
	for i := 0; i < layers; i++ {
		def.Layers = append(def.Layers, &model.OncallScheduleLayerM{
			GroupName:            group,
			Name:                 "Layer " + itoaTest(i),
			OrderIndex:           int64(i),
			RotationType:         model.RotationWeekly,
			RotationStart:        start,
			ShiftDurationSeconds: 7 * 86400,
		})
		for j := 0; j < 2; j++ {
			def.Participants[i] = append(def.Participants[i], &model.OncallScheduleParticipantM{
				UserName:   "user-" + itoaTest(i) + "-" + itoaTest(j),
				OrderIndex: int64(j),
			})
		}
	}
	return def
}

func itoaTest(i int) string {
	return string(rune('0' + i))
}

func TestSaveDefinitionAndGetAggregate(t *testing.T) {
	st := newOncallScheduleTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.SaveDefinition(ctx, testDefinition("go", 2)))

	agg, err := st.GetAggregate(ctx, "go")
	require.NoError(t, err)
	require.NotNil(t, agg)
	require.NotNil(t, agg.Schedule)
	assert.Equal(t, "Asia/Shanghai", agg.Schedule.Timezone)
	require.Len(t, agg.Layers, 2)
	assert.Equal(t, "Layer 0", agg.Layers[0].Layer.Name)
	require.Len(t, agg.Layers[0].Participants, 2)
	assert.Equal(t, "user-0-0", agg.Layers[0].Participants[0].UserName)
}

func TestSaveDefinitionReplacesLayersButKeepsOperationalRows(t *testing.T) {
	st := newOncallScheduleTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.SaveDefinition(ctx, testDefinition("go", 2)))
	require.NoError(t, st.CreateOverride(ctx, &model.OncallScheduleOverrideM{
		GroupName:    "go",
		OverrideUser: "carol",
		StartTime:    time.Now().UTC(),
		EndTime:      time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, st.CreateUnavailability(ctx, &model.OncallScheduleUnavailabilityM{
		GroupName: "go", UserName: "user-0-0", StartDate: "2026-11-10", EndDate: "2026-11-11",
	}))

	require.NoError(t, st.SaveDefinition(ctx, testDefinition("go", 1)))

	agg, err := st.GetAggregate(ctx, "go")
	require.NoError(t, err)
	require.Len(t, agg.Layers, 1)
	assert.Len(t, agg.Overrides, 1)
	assert.Len(t, agg.Unavailabilities, 1)
}

func TestSaveEscalationAndListTiers(t *testing.T) {
	st := newOncallScheduleTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.SaveDefinition(ctx, testDefinition("go", 1)))
	schedule, err := st.GetAggregate(ctx, "go")
	require.NoError(t, err)

	updated := *schedule.Schedule
	updated.EscalationEnabled = true
	updated.EscalationAuto = true
	updated.EscalationFromLevel = 2
	require.NoError(t, st.SaveEscalation(ctx, &updated, []*model.OncallEscalationTierM{
		{TierIndex: 0, TimeoutSeconds: 300, TargetType: model.EscalationTargetSchedule},
		{TierIndex: 1, TimeoutSeconds: 600, TargetType: model.EscalationTargetUsers, UserNames: `["ops"]`},
	}))

	agg, err := st.GetAggregate(ctx, "go")
	require.NoError(t, err)
	assert.True(t, agg.Schedule.EscalationEnabled)
	require.Len(t, agg.Tiers, 2)
	assert.Equal(t, []string{"ops"}, agg.Tiers[1].UserList())
}

func TestDeleteForGroupRemovesEveryChild(t *testing.T) {
	st := newOncallScheduleTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.SaveDefinition(ctx, testDefinition("go", 2)))
	require.NoError(t, st.CreateOverride(ctx, &model.OncallScheduleOverrideM{
		GroupName:    "go",
		OverrideUser: "carol",
		StartTime:    time.Now().UTC(),
		EndTime:      time.Now().UTC().Add(time.Hour),
	}))

	require.NoError(t, st.DeleteForGroup(ctx, "go"))
	agg, err := st.GetAggregate(ctx, "go")
	require.NoError(t, err)
	assert.Nil(t, agg)

	overrides, err := st.ListOverrides(ctx, "go")
	require.NoError(t, err)
	assert.Empty(t, overrides)
}

func TestDeleteOverrideMissingIsNotFound(t *testing.T) {
	st := newOncallScheduleTestStore(t)
	err := st.DeleteOverride(context.Background(), "go", "missing")
	assert.ErrorIs(t, err, ErrRecordNotFound)
}
