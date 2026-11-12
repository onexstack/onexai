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

package taskstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
)

type fakeRuns struct {
	runs map[string]*model.RunM
}

func (f *fakeRuns) CreateRun(_ context.Context, run *model.RunM) error {
	if f.runs == nil {
		f.runs = map[string]*model.RunM{}
	}
	if run.Version == 0 {
		run.Version = 1
	}
	copy := *run
	f.runs[run.ID] = &copy
	return nil
}
func (f *fakeRuns) GetRun(_ context.Context, id string) (*model.RunM, error) {
	run, ok := f.runs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copy := *run
	return &copy, nil
}
func (f *fakeRuns) UpdateRunTask(_ context.Context, id, taskJSON string, version int64) (int64, error) {
	run, ok := f.runs[id]
	if !ok {
		return 0, errors.New("not found")
	}
	if run.Version != version {
		return 0, runstore.ErrVersionConflict
	}
	run.TaskJSON = taskJSON
	run.Version = version + 1
	return run.Version, nil
}
func (f *fakeRuns) ListRun(context.Context, string, int) ([]*model.RunM, error) {
	out := make([]*model.RunM, 0, len(f.runs))
	for _, run := range f.runs {
		out = append(out, run)
	}
	return out, nil
}
func (f *fakeRuns) SettleRun(_ context.Context, run *model.RunM, version int64, _ []*model.RunEventM) error {
	stored, ok := f.runs[run.ID]
	if !ok {
		return errors.New("not found")
	}
	if stored.Version != version {
		return runstore.ErrVersionConflict
	}
	stored.State = run.State
	stored.TaskJSON = run.TaskJSON
	stored.Version = version + 1
	return nil
}

func TestTaskLifecycle(t *testing.T) {
	runs := &fakeRuns{}
	service := &Service{Runs: runs}
	ctx := context.Background()

	task, err := service.Create(ctx, "s1", `{"id":"t1","status":{"state":"TASK_STATE_SUBMITTED"}}`)
	require.NoError(t, err)
	assert.Equal(t, "t1", task.ID)
	assert.Equal(t, int64(1), task.Version)

	updated, err := service.Update(ctx, "t1", `{"id":"t1","status":{"state":"TASK_STATE_WORKING"}}`, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)

	_, err = service.Update(ctx, "t1", `{"id":"t1"}`, 1)
	require.ErrorIs(t, err, runstore.ErrVersionConflict)

	settled, err := service.Settle(ctx, "t1", `{"id":"t1","status":{"state":"TASK_STATE_COMPLETED"}}`, 2)
	require.NoError(t, err)
	assert.Equal(t, model.RunStateCompleted, settled.State)
	assert.Equal(t, int64(3), settled.Version)

	tasks, err := service.List(ctx, "s1")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSettleMapsInputRequired(t *testing.T) {
	runs := &fakeRuns{}
	service := &Service{Runs: runs}
	ctx := context.Background()
	_, err := service.Create(ctx, "s1", `{"id":"t1"}`)
	require.NoError(t, err)
	settled, err := service.Settle(ctx, "t1", `{"id":"t1","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}`, 1)
	require.NoError(t, err)
	assert.Equal(t, model.RunStateInterrupted, settled.State)
}

// TestCreateIsIdempotentOnTheTaskID pins the retry contract: a runtime that
// loses the response must get the task it already created, not a conflict, and
// must not overwrite what the first call stored.
func TestCreateIsIdempotentOnTheTaskID(t *testing.T) {
	runs := &fakeRuns{}
	service := &Service{Runs: runs}
	first := `{"id":"task-1","status":{"state":"TASK_STATE_WORKING"},"marker":"first"}`

	created, err := service.Create(context.Background(), "session-1", first)
	require.NoError(t, err)

	// The retry carries a different snapshot: if create were not idempotent it
	// would replace the stored one, which is how a retry rewinds a live task.
	retry := `{"id":"task-1","status":{"state":"TASK_STATE_WORKING"},"marker":"retry"}`
	again, err := service.Create(context.Background(), "session-1", retry)
	require.NoError(t, err)
	assert.Equal(t, created.ID, again.ID)
	assert.Contains(t, again.TaskJSON, "first", "a retry must not overwrite the stored task")
	assert.Len(t, runs.runs, 1, "the retry must not create a second row")
}

// TestCreateRefusesAnotherSessionsTaskID pins that the same id in another
// conversation is a mistake rather than a retry.
func TestCreateRefusesAnotherSessionsTaskID(t *testing.T) {
	service := &Service{Runs: &fakeRuns{}}
	taskJSON := `{"id":"task-1","status":{"state":"TASK_STATE_WORKING"}}`

	_, err := service.Create(context.Background(), "session-1", taskJSON)
	require.NoError(t, err)

	_, err = service.Create(context.Background(), "session-2", taskJSON)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another session")
}
