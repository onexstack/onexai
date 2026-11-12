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

package escalation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/chat/userdir"
)

type fakeScheduleStore struct {
	store.OncallScheduleStore
	aggs map[string]*model.OncallScheduleAggregate
}

func (f *fakeScheduleStore) GetAggregate(_ context.Context, group string) (*model.OncallScheduleAggregate, error) {
	return f.aggs[group], nil
}

type fakeStore struct {
	store.IStore
	schedules store.OncallScheduleStore
}

func (f *fakeStore) OncallSchedules() store.OncallScheduleStore { return f.schedules }

func TestResolveTargetsScheduleAndUsersAreDeduped(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	agg := &model.OncallScheduleAggregate{
		Schedule: &model.OncallScheduleM{GroupName: "go", Timezone: "UTC"},
		Layers: []model.OncallScheduleLayerAggregate{{
			Layer: model.OncallScheduleLayerM{
				Name:                 "Primary",
				RotationStart:        start,
				ShiftDurationSeconds: 7 * 86400,
			},
			Participants: []*model.OncallScheduleParticipantM{{UserName: "alice"}},
		}},
	}

	engine := New(&fakeStore{schedules: &fakeScheduleStore{
		aggs: map[string]*model.OncallScheduleAggregate{"go": agg},
	}}, nil, userdir.Static{"alice": "ou_alice", "bob": "ou_bob"}, time.Minute, false)

	tier := &model.OncallEscalationTierM{
		TargetType: model.EscalationTargetBoth,
		UserNames:  `["alice","bob"]`,
	}
	targets := engine.resolveTargets(context.Background(), agg, tier, now)
	require.Len(t, targets, 2)
	assert.Equal(t, "alice", targets[0].UserName)
	assert.Equal(t, "ou_alice", targets[0].UserID)
	assert.Equal(t, "schedule", targets[0].Source)
	assert.Equal(t, "bob", targets[1].UserName)
	assert.Equal(t, "users", targets[1].Source)
}

func TestStartDoesNothingWhenWorkerIsDisabled(t *testing.T) {
	engine := New(&fakeStore{}, nil, nil, time.Minute, false)
	// No store methods are reached: a nil store would panic otherwise.
	engine.Start(context.Background(), &model.TicketM{
		TicketID: "t-1",
		GroupID:  "go",
		Level:    1,
	})
}

func TestTimeoutOfDefaults(t *testing.T) {
	assert.Equal(t, 5*time.Minute, timeoutOf(nil))
	assert.Equal(t, 5*time.Minute, timeoutOf(&model.OncallEscalationTierM{}))
	assert.Equal(t, 90*time.Second, timeoutOf(&model.OncallEscalationTierM{TimeoutSeconds: 90}))
}
