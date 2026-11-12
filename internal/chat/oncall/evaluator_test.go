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

package oncall

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/model"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}

func testLayer(
	name string, order int64, start time.Time, duration int64, users ...string,
) model.OncallScheduleLayerAggregate {
	participants := make([]*model.OncallScheduleParticipantM, 0, len(users))
	for i, user := range users {
		participants = append(participants, &model.OncallScheduleParticipantM{
			UserName:   user,
			OrderIndex: int64(i),
		})
	}
	return model.OncallScheduleLayerAggregate{
		Layer: model.OncallScheduleLayerM{
			Name:                 name,
			OrderIndex:           order,
			RotationType:         model.RotationWeekly,
			RotationStart:        start,
			ShiftDurationSeconds: duration,
		},
		Participants: participants,
	}
}

func testAggregate(layers ...model.OncallScheduleLayerAggregate) *model.OncallScheduleAggregate {
	return &model.OncallScheduleAggregate{
		Schedule: &model.OncallScheduleM{Timezone: "UTC"},
		Layers:   layers,
	}
}

func TestCurrentAtWeeklyRotation(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice", "bob"))

	current, ok := CurrentAt(agg, mustTime(t, "2026-11-06T10:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "alice", current.UserName)
	assert.Equal(t, "Primary", current.LayerName)
	assert.Equal(t, "2026-11-02T00:00:00Z", current.Since.Format(time.RFC3339))
	require.NotNil(t, current.Until)
	assert.Equal(t, "2026-11-09T00:00:00Z", current.Until.Format(time.RFC3339))

	current, ok = CurrentAt(agg, mustTime(t, "2026-11-09T00:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "bob", current.UserName)

	current, ok = CurrentAt(agg, mustTime(t, "2026-11-16T00:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "alice", current.UserName)
}

func TestCurrentAtBeforeRotationStart(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice"))

	_, ok := CurrentAt(agg, mustTime(t, "2026-11-01T00:00:00Z"))
	assert.False(t, ok)
}

func TestCurrentAtOverrideWins(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice"))
	agg.Overrides = []*model.OncallScheduleOverrideM{
		{
			OverrideUser: "carol",
			StartTime:    mustTime(t, "2026-11-03T00:00:00Z"),
			EndTime:      mustTime(t, "2026-11-04T00:00:00Z"),
		},
	}

	current, ok := CurrentAt(agg, mustTime(t, "2026-11-03T12:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "carol", current.UserName)
	assert.True(t, current.IsOverride)
	assert.Equal(t, "2026-11-03T00:00:00Z", current.Since.Format(time.RFC3339))

	// The end is exclusive: the rotation resumes at exactly the end.
	current, ok = CurrentAt(agg, mustTime(t, "2026-11-04T00:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "alice", current.UserName)
	assert.False(t, current.IsOverride)
}

func TestCurrentAtLatestOverrideWins(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice"))
	agg.Overrides = []*model.OncallScheduleOverrideM{
		{
			OverrideUser: "carol",
			StartTime:    mustTime(t, "2026-11-03T00:00:00Z"),
			EndTime:      mustTime(t, "2026-11-06T00:00:00Z"),
		},
		{
			OverrideUser: "dave",
			StartTime:    mustTime(t, "2026-11-04T00:00:00Z"),
			EndTime:      mustTime(t, "2026-11-05T00:00:00Z"),
		},
	}

	current, ok := CurrentAt(agg, mustTime(t, "2026-11-04T12:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "dave", current.UserName)
}

func TestCurrentAtSkipsUnavailableParticipantInSameSlot(t *testing.T) {
	start := mustTime(t, "2026-11-01T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 86400, "alice", "bob"))
	agg.Schedule.Timezone = "Asia/Shanghai"
	agg.Unavailabilities = []*model.OncallScheduleUnavailabilityM{
		{UserName: "bob", StartDate: "2026-11-02", EndDate: "2026-11-02"},
	}

	// 2026-11-02T03:00Z is 11:00 in Asia/Shanghai, slot 1 (bob), who is
	// unavailable; alice is the next participant in the same slot.
	current, ok := CurrentAt(agg, mustTime(t, "2026-11-02T03:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "alice", current.UserName)
	assert.Equal(t, "2026-11-02T00:00:00Z", current.Since.Format(time.RFC3339))
	assert.Equal(t, "2026-11-03T00:00:00Z", current.Until.Format(time.RFC3339))
}

func TestUnavailabilityUsesScheduleTimezoneDayBoundary(t *testing.T) {
	start := mustTime(t, "2026-11-01T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 86400, "alice", "bob"))
	agg.Schedule.Timezone = "Asia/Shanghai"
	agg.Unavailabilities = []*model.OncallScheduleUnavailabilityM{
		{UserName: "alice", StartDate: "2026-11-02", EndDate: "2026-11-02"},
	}

	// 15:30Z is 23:30 on 11-01 in Shanghai, so alice is still available.
	current, ok := CurrentAt(agg, mustTime(t, "2026-11-01T15:30:00Z"))
	require.True(t, ok)
	assert.Equal(t, "alice", current.UserName)

	// 16:30Z is 00:30 on 11-02 in Shanghai, so alice is unavailable and bob
	// takes the same slot.
	current, ok = CurrentAt(agg, mustTime(t, "2026-11-01T16:30:00Z"))
	require.True(t, ok)
	assert.Equal(t, "bob", current.UserName)
}

func TestEmptyLayerFallsBackToNextLayer(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(
		testLayer("Primary", 0, start, 7*86400),
		testLayer("Backup", 1, start, 7*86400, "bob"),
	)

	current, ok := CurrentAt(agg, mustTime(t, "2026-11-03T00:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "bob", current.UserName)
	assert.Equal(t, "Backup", current.LayerName)
}

func TestWatchersAtIncludesBackupsInLayerOrder(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(
		testLayer("Primary", 0, start, 7*86400, "alice"),
		testLayer("Backup", 1, start, 7*86400, "bob"),
	)

	watchers := WatchersAt(agg, mustTime(t, "2026-11-03T00:00:00Z"))
	require.Len(t, watchers, 2)
	assert.Equal(t, "alice", watchers[0].UserName)
	assert.Equal(t, "bob", watchers[1].UserName)
}

func TestTimelineMergesAdjacentSegments(t *testing.T) {
	start := mustTime(t, "2026-11-01T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 86400, "alice", "bob"))

	segments := Timeline(agg, mustTime(t, "2026-11-01T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z"))
	require.Len(t, segments, 3)
	assert.Equal(t, "alice", segments[0].UserName)
	assert.Equal(t, "bob", segments[1].UserName)
	assert.Equal(t, "alice", segments[2].UserName)
	for i := 1; i < len(segments); i++ {
		assert.Equal(t, segments[i-1].End, segments[i].Start)
	}
}

func TestTimelineUsesNobodyWhenUnstaffed(t *testing.T) {
	agg := testAggregate(testLayer("Primary", 0, mustTime(t, "2026-11-02T00:00:00Z"), 86400))

	segments := Timeline(agg, mustTime(t, "2026-11-01T00:00:00Z"), mustTime(t, "2026-11-02T00:00:00Z"))
	require.Len(t, segments, 1)
	assert.Equal(t, Nobody, segments[0].UserName)
}

func TestLayerTimelinesIgnoreOverrides(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice"))
	agg.Overrides = []*model.OncallScheduleOverrideM{
		{
			OverrideUser: "carol",
			StartTime:    mustTime(t, "2026-11-03T00:00:00Z"),
			EndTime:      mustTime(t, "2026-11-04T00:00:00Z"),
		},
	}

	layers, effective := LayerTimelines(agg, mustTime(t, "2026-11-02T00:00:00Z"), mustTime(t, "2026-11-05T00:00:00Z"))
	require.Contains(t, layers, "Primary")
	for _, segment := range layers["Primary"] {
		assert.Equal(t, "alice", segment.UserName)
	}

	foundOverride := false
	for _, segment := range effective {
		if segment.IsOverride {
			foundOverride = true
			assert.Equal(t, "carol", segment.UserName)
		}
	}
	assert.True(t, foundOverride)
}

func TestNextBoundaryPrefersRotationBoundary(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice", "bob"))

	next, ok := NextBoundary(agg, mustTime(t, "2026-11-06T10:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "2026-11-09T00:00:00Z", next.Format(time.RFC3339))
}

func TestNextBoundaryIncludesOverrideEnd(t *testing.T) {
	start := mustTime(t, "2026-11-02T00:00:00Z")
	agg := testAggregate(testLayer("Primary", 0, start, 7*86400, "alice"))
	agg.Overrides = []*model.OncallScheduleOverrideM{
		{
			OverrideUser: "carol",
			StartTime:    mustTime(t, "2026-11-03T00:00:00Z"),
			EndTime:      mustTime(t, "2026-11-04T00:00:00Z"),
		},
	}

	next, ok := NextBoundary(agg, mustTime(t, "2026-11-03T12:00:00Z"))
	require.True(t, ok)
	assert.Equal(t, "2026-11-04T00:00:00Z", next.Format(time.RFC3339))
}

func TestUnknownTimezoneFallsBackToUTC(t *testing.T) {
	assert.Equal(t, time.UTC, Location("not/a-zone"))
	assert.Equal(t, time.UTC, Location(""))
}
