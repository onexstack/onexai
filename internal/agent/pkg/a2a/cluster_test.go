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
	"encoding/json"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/a2aproject/a2a-go/v2/a2asrv/limiter"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// newRedis starts a Redis stand-in and returns a client for it.
func newRedis(t *testing.T) *redis.Client {
	t.Helper()

	server, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newEventQueue returns an event queue namespaced for a test.
func newEventQueue(t *testing.T, client *redis.Client) *RedisEventQueue {
	t.Helper()
	return NewRedisEventQueue(client, "test")
}

// newWorkQueue returns a work queue whose workers stop with the test.
func newWorkQueue(t *testing.T, consumer string) *RedisWorkQueue {
	t.Helper()

	client := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	queue := NewRedisWorkQueue(ctx, client, "test", consumer)
	t.Cleanup(queue.Shutdown)
	return queue
}

// TestEventQueueReplaysForASecondSubscriber is the property resubscribe is
// built on: a client that attaches after events were written still sees them.
// Pub/sub would fail this test, which is why the queue is a stream.
func TestEventQueueReplaysForASecondSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	queue := newEventQueue(t, newRedis(t))
	writer, err := queue.CreateWriter(ctx, "task-1")
	require.NoError(t, err)

	for _, text := range []string{"first", "second"} {
		require.NoError(t, writer.Write(ctx, &eventqueue.Message{
			Event: a2a.NewArtifactEvent(a2a.TaskInfo{TaskID: "task-1", ContextID: "ctx-1"},
				a2a.NewTextPart(text)),
			TaskVersion: 1,
		}))
	}

	for _, name := range []string{"first subscriber", "resubscribed"} {
		reader, err := queue.CreateReader(ctx, "task-1")
		require.NoError(t, err)

		var texts []string
		for range 2 {
			msg, err := reader.Read(ctx)
			require.NoError(t, err, name)
			artifact, ok := msg.Event.(*a2a.TaskArtifactUpdateEvent)
			require.True(t, ok)
			texts = append(texts, artifact.Artifact.Parts[0].Text())
		}
		require.NoError(t, reader.Close())
		assert.Equal(t, []string{"first", "second"}, texts, name)
	}
}

// TestWorkQueueExecutesSubmittedWork covers the happy path: a submitted payload
// reaches the registered handler with its lease held.
func TestWorkQueueExecutesSubmittedWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	queue := newWorkQueue(t, "consumer-1")
	started := make(chan *workqueue.Payload, 1)
	submit := func(_ context.Context, payload *workqueue.Payload) (a2a.SendMessageResult, error) {
		started <- payload
		return nil, nil
	}

	queue.RegisterHandler(workqueue.HandlerConfig{Limiter: limiter.ConcurrencyConfig{MaxExecutions: 1}}, submit)

	_, err := queue.Write(ctx, &workqueue.Payload{Type: workqueue.PayloadTypeExecute, TaskID: "task-1"})
	require.NoError(t, err)

	select {
	case payload := <-started:
		assert.Equal(t, a2a.TaskID("task-1"), payload.TaskID)
	case <-ctx.Done():
		t.Fatal("the work item was never executed")
	}
}

// TestWorkQueueRefusesADoubleExecution pins the lease: while one replica runs a
// task, submitting it again is refused rather than run twice.
func TestWorkQueueRefusesADoubleExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue := newWorkQueue(t, "consumer-1")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	submit := func(context.Context, *workqueue.Payload) (a2a.SendMessageResult, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}
	queue.RegisterHandler(workqueue.HandlerConfig{Limiter: limiter.ConcurrencyConfig{MaxExecutions: 1}}, submit)

	_, err := queue.Write(ctx, &workqueue.Payload{Type: workqueue.PayloadTypeExecute, TaskID: "task-2"})
	require.NoError(t, err)

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the first execution never started")
	}

	_, err = queue.Write(ctx, &workqueue.Payload{Type: workqueue.PayloadTypeExecute, TaskID: "task-2"})
	assert.ErrorIs(t, err, workqueue.ErrLeaseAlreadyTaken)

	// A cancellation is allowed while an execution runs — that is what cancel
	// means — but a second cancellation is not.
	_, err = queue.Write(ctx, &workqueue.Payload{Type: workqueue.PayloadTypeCancel, TaskID: "task-2",
		CancelRequest: &a2a.CancelTaskRequest{ID: "task-2"}})
	assert.NoError(t, err)

	close(release)

	// Once the execution finishes and its lease is released, the task can be
	// submitted again.
	assert.Eventually(t, func() bool {
		_, writeErr := queue.Write(ctx, &workqueue.Payload{
			Type: workqueue.PayloadTypeExecute, TaskID: "task-2",
		})
		return writeErr == nil
	}, 5*time.Second, 50*time.Millisecond)
}

// TestCallContextCodecCarriesTheCaller pins what has to survive the hop to
// another replica: the authenticated caller, the authorization subject and the
// per-caller output ceiling. Losing any of them changes what the run is.
func TestCallContextCodecCarriesTheCaller(t *testing.T) {
	subject := authz.SubjectFromService("oai-chat-apiserver")
	callCtx, sdkCallCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(nil))
	sdkCallCtx.User = a2asrv.NewAuthenticatedUser("oai-chat-apiserver", nil)
	ctx := authz.ContextWithSubject(callCtx, subject)
	ctx = chatmodel.WithMaxOutputTokens(ctx, 2048)

	encoded, err := CallContextCodec{}.Encode(ctx)
	require.NoError(t, err)

	// The encoded context travels inside a JSON work item, so the test does too.
	raw, err := json.Marshal(encoded)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	restored, err := CallContextCodec{}.Decode(context.Background(), decoded)
	require.NoError(t, err)

	restoredCallCtx, ok := a2asrv.CallContextFrom(restored)
	require.True(t, ok, "the executing replica must see the same A2A user")
	require.NotNil(t, restoredCallCtx.User)
	assert.Equal(t, "oai-chat-apiserver", restoredCallCtx.User.Name)
	assert.True(t, restoredCallCtx.User.Authenticated)

	restoredSubject, ok := authz.SubjectFromContext(restored)
	require.True(t, ok)
	assert.Equal(t, authz.SubjectKindServiceAccount, restoredSubject.Kind)
	assert.Equal(t, "oai-chat-apiserver", restoredSubject.ServiceName)

	assert.Equal(t, 2048, chatmodel.MaxOutputTokens(restored))
}

// TestCallContextCodecIsEmptyWithoutAnythingToCarry keeps an unauthenticated
// deployment from writing a payload full of zeros.
func TestCallContextCodecIsEmptyWithoutAnythingToCarry(t *testing.T) {
	encoded, err := CallContextCodec{}.Encode(context.Background())
	require.NoError(t, err)
	assert.Nil(t, encoded)
}

// TestTaskStoreAndEventQueueShareAPrefix documents that the three Redis pieces
// stay disjoint namespaces, so a task id cannot collide with a queue key.
func TestTaskStoreAndEventQueueShareAPrefix(t *testing.T) {
	client := newRedis(t)

	store := NewRedisTaskStore(client, "test")
	events := NewRedisEventQueue(client, "test")
	work := NewRedisWorkQueue(context.Background(), client, "test", "consumer")

	assert.Equal(t, "test"+TaskStorePrefix+":task:t1", store.taskKey("t1"))
	assert.Equal(t, "test"+EventQueuePrefix+":t1", events.key("t1"))
	assert.NotContains(t, work.stream, EventQueuePrefix)
	assert.NotEqual(t, store.taskKey("t1"), events.key("t1"))
}

// TaskStore is referenced so the import stays honest if the store moves.
var _ taskstore.Store = (*RedisTaskStore)(nil)
