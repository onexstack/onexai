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

package a2a

import (
	"context"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTaskStore starts a store whose owner is fixed, which is what the SDK's
// authenticator would report for a signed request.
func newTaskStore(t *testing.T, user string) *RedisTaskStore {
	t.Helper()

	server, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisTaskStore(client, "test")
	store.authenticate = func(context.Context) (string, error) { return user, nil }
	return store
}

// newTask builds a task in a given state.
func newTask(id, contextID string, state a2a.TaskState) *a2a.Task {
	at := time.Now()
	return &a2a.Task{
		ID:        a2a.TaskID(id),
		ContextID: contextID,
		Status:    a2a.TaskStatus{State: state, Timestamp: &at},
		History:   []*a2a.Message{a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))},
	}
}

// TestTaskStoreCreateAndUpdateAreVersioned covers the two properties the SDK
// relies on: an id is claimed once, and an update names the version it saw.
func TestTaskStoreCreateAndUpdateAreVersioned(t *testing.T) {
	ctx := context.Background()
	store := newTaskStore(t, "alice")
	task := newTask("t1", "c1", a2a.TaskStateWorking)

	version, err := store.Create(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, taskstore.TaskVersion(1), version)

	_, err = store.Create(ctx, newTask("t1", "c1", a2a.TaskStateWorking))
	assert.ErrorIs(t, err, taskstore.ErrTaskAlreadyExists)

	task.Status.State = a2a.TaskStateCompleted
	version, err = store.Update(ctx, &taskstore.UpdateRequest{Task: task, PrevVersion: version})
	require.NoError(t, err)
	assert.Equal(t, taskstore.TaskVersion(2), version)

	_, err = store.Update(ctx, &taskstore.UpdateRequest{Task: task, PrevVersion: 1})
	assert.ErrorIs(t, err, taskstore.ErrConcurrentModification)

	_, err = store.Update(ctx, &taskstore.UpdateRequest{Task: newTask("missing", "c1", a2a.TaskStateWorking)})
	assert.ErrorIs(t, err, a2a.ErrTaskNotFound)
}

// TestTaskStoreHidesAnotherUsersTask pins the spec's ownership rule: a task
// that belongs to someone else is "not found", not "forbidden", so an id cannot
// be probed for existence.
func TestTaskStoreHidesAnotherUsersTask(t *testing.T) {
	ctx := context.Background()
	alice := newTaskStore(t, "alice")
	task := newTask("t1", "c1", a2a.TaskStateWorking)
	_, err := alice.Create(ctx, task)
	require.NoError(t, err)

	_, err = storeWithUser(alice, "bob").Get(ctx, "t1")
	assert.ErrorIs(t, err, a2a.ErrTaskNotFound)

	_, err = storeWithUser(alice, "bob").Update(ctx, &taskstore.UpdateRequest{Task: task})
	assert.ErrorIs(t, err, a2a.ErrTaskNotFound)
}

// storeWithUser reads the same Redis as store, as a different caller.
func storeWithUser(store *RedisTaskStore, user string) *RedisTaskStore {
	other := &RedisTaskStore{
		client:       store.client,
		prefix:       store.prefix,
		authenticate: func(context.Context) (string, error) { return user, nil },
	}
	return other
}

// TestTaskStoreListFiltersAndPaginates covers the query surface: a filter that
// is not part of the index, and a second page reached through the token.
func TestTaskStoreListFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	store := newTaskStore(t, "alice")

	for i, task := range []*a2a.Task{
		newTask("t1", "c1", a2a.TaskStateCompleted),
		newTask("t2", "c2", a2a.TaskStateCompleted),
		newTask("t3", "c1", a2a.TaskStateWorking),
	} {
		_, err := store.Create(ctx, task)
		require.NoError(t, err)
		// Distinct scores, so recency ordering is deterministic.
		time.Sleep(time.Duration(i+1) * time.Millisecond)
	}

	all, err := store.List(ctx, &a2a.ListTasksRequest{})
	require.NoError(t, err)
	assert.Equal(t, 3, all.TotalSize)

	inC1, err := store.List(ctx, &a2a.ListTasksRequest{ContextID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, 2, inC1.TotalSize)

	completed, err := store.List(ctx, &a2a.ListTasksRequest{Status: a2a.TaskStateCompleted})
	require.NoError(t, err)
	assert.Equal(t, 2, completed.TotalSize)

	firstPage, err := store.List(ctx, &a2a.ListTasksRequest{PageSize: 2})
	require.NoError(t, err)
	require.Len(t, firstPage.Tasks, 2)
	assert.Equal(t, 3, firstPage.TotalSize)
	assert.Equal(t, "t3", string(firstPage.Tasks[0].ID), "newest first")
	require.NotEmpty(t, firstPage.NextPageToken)

	secondPage, err := store.List(ctx, &a2a.ListTasksRequest{PageSize: 2, PageToken: firstPage.NextPageToken})
	require.NoError(t, err)
	require.Len(t, secondPage.Tasks, 1)
	assert.Equal(t, "t1", string(secondPage.Tasks[0].ID))
	assert.Empty(t, secondPage.NextPageToken)
}

// TestTaskStoreListTrimsHistoryAndArtifacts mirrors the SDK's own response
// shape, so a caller cannot tell which store answered.
func TestTaskStoreListTrimsHistoryAndArtifacts(t *testing.T) {
	ctx := context.Background()
	store := newTaskStore(t, "alice")

	task := newTask("t1", "c1", a2a.TaskStateCompleted)
	task.Artifacts = []*a2a.Artifact{{ID: "a1", Parts: []*a2a.Part{a2a.NewTextPart("answer")}}}
	_, err := store.Create(ctx, task)
	require.NoError(t, err)

	listed, err := store.List(ctx, &a2a.ListTasksRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Tasks, 1)
	assert.Nil(t, listed.Tasks[0].Artifacts, "artifacts are opt-in")

	withArtifacts, err := store.List(ctx, &a2a.ListTasksRequest{IncludeArtifacts: true})
	require.NoError(t, err)
	require.Len(t, withArtifacts.Tasks, 1)
	assert.Len(t, withArtifacts.Tasks[0].Artifacts, 1)

	noHistory := 0
	withoutHistory, err := store.List(ctx, &a2a.ListTasksRequest{HistoryLength: &noHistory})
	require.NoError(t, err)
	require.Len(t, withoutHistory.Tasks, 1)
	assert.Empty(t, withoutHistory.Tasks[0].History)
}

// TestTaskStoreListNeedsAnOwner pins the SDK's rule: an unauthenticated list is
// empty of meaning, so it is refused rather than answered with everyone's tasks.
func TestTaskStoreListNeedsAnOwner(t *testing.T) {
	store := newTaskStore(t, "")

	_, err := store.List(context.Background(), &a2a.ListTasksRequest{})
	assert.ErrorIs(t, err, a2a.ErrUnauthenticated)
}
