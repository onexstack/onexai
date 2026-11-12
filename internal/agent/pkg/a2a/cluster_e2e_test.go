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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// scriptedRunner replays a fixed answer, slowly enough that a test can observe
// the stream mid-flight. It implements Stopper, which is what makes cancel
// observable here.
type scriptedRunner struct {
	chunks int
	delay  time.Duration
	// hold makes the run end only when its context is cancelled, which is the
	// state a cancel is meant to end.
	hold bool

	mu      sync.Mutex
	stopped []string
}

func (r *scriptedRunner) Ask(ctx context.Context, _ *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	events := make(chan agentapi.Event)

	go func() {
		defer close(events)
		if r.hold {
			<-ctx.Done()
			events <- agentapi.Event{Type: agentapi.EventStopped}
			return
		}
		for i := range r.chunks {
			select {
			case <-ctx.Done():
				events <- agentapi.Event{Type: agentapi.EventStopped}
				return
			case <-time.After(r.delay):
			}
			events <- agentapi.Event{Type: agentapi.EventAnswer, Delta: fmt.Sprintf("part-%d ", i)}
		}
		events <- agentapi.Event{Type: agentapi.EventDone}
	}()
	return events, nil
}

func (r *scriptedRunner) Stop(_ context.Context, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, runID)
	return nil
}

func (r *scriptedRunner) stopRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stopped...)
}

// replica is one process of the cluster: its own transports, its own queue
// consumer, and the shared Redis underneath.
type replica struct {
	server *httptest.Server
	runner *scriptedRunner
}

// startReplica builds one replica over client. Each call stands for a separate
// process: the event queue, the task store and the work queue are separate
// objects, and they only share Redis.
func startReplica(t *testing.T, client *redis.Client, consumer string, runner *scriptedRunner) *replica {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	workQueue := NewRedisWorkQueue(ctx, client, "e2e", consumer)
	t.Cleanup(workQueue.Shutdown)

	handlers, err := NewHandlers(runner, Options{
		TaskStore:    NewRedisTaskStore(client, "e2e"),
		QueueManager: NewRedisEventQueue(client, "e2e"),
		WorkQueue:    workQueue,
		ContextCodec: CallContextCodec{},
	})
	require.NoError(t, err)

	// What the AK/SK middleware does in production: name the caller before the
	// SDK sees the request.
	caller := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := authz.ContextWithSubject(r.Context(), authz.SubjectFromService("oai-chat"))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	server := httptest.NewServer(caller(handlers.JSONRPC))
	t.Cleanup(server.Close)
	return &replica{server: server, runner: runner}
}

// rpc posts one JSON-RPC request and decodes each SSE event it produces.
func (r *replica) rpc(ctx context.Context, method string, params any, onEvent func(map[string]any) bool) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "test", "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server.URL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &payload); err != nil {
			continue
		}
		if !onEvent(payload) {
			return nil
		}
	}
	return scanner.Err()
}

// call posts one JSON-RPC request that answers in one body rather than a
// stream, which is every method except the two streaming ones.
func (r *replica) call(ctx context.Context, method string, params any) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "test", "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server.URL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// taskIDFrom reads the task id out of a first event.
func taskIDFrom(event map[string]any) string {
	result, _ := event["result"].(map[string]any)
	task, _ := result["task"].(map[string]any)
	id, _ := task["id"].(string)
	return id
}

// stateFrom reads a task state out of any event shape.
func stateFrom(event map[string]any) string {
	result, _ := event["result"].(map[string]any)
	if task, ok := result["task"].(map[string]any); ok {
		status, _ := task["status"].(map[string]any)
		state, _ := status["state"].(string)
		return state
	}
	if update, ok := result["statusUpdate"].(map[string]any); ok {
		status, _ := update["status"].(map[string]any)
		state, _ := status["state"].(string)
		return state
	}
	// A plain task, which is what GetTask and CancelTask answer with.
	if status, ok := result["status"].(map[string]any); ok {
		state, _ := status["state"].(string)
		return state
	}
	return ""
}

// TestResubscribeThroughAnotherReplica is the cross-replica promise in one test:
// a client that loses its stream reattaches through a different process and
// sees the events it missed, ending in the run's real state.
func TestResubscribeThroughAnotherReplica(t *testing.T) {
	client := newRedis(t)
	// Slow enough that the client can disconnect while the answer is still
	// being produced.
	runner := &scriptedRunner{chunks: 20, delay: 30 * time.Millisecond}
	first := startReplica(t, client, "consumer-a", runner)
	second := startReplica(t, client, "consumer-b", runner)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var taskID string

	// The first connection is cut after a few chunks, the way a dropped
	// connection behaves.
	cutCtx, cut := context.WithCancel(ctx)
	seen := 0
	_ = first.rpc(cutCtx, "SendStreamingMessage", map[string]any{
		"message": map[string]any{
			"messageId": "m-1",
			"role":      "ROLE_USER",
			"contextId": "ctx-1",
			"parts":     []any{map[string]any{"text": "写一段长答案"}},
		},
	}, func(event map[string]any) bool {
		if id := taskIDFrom(event); id != "" {
			taskID = id
		}
		seen++
		if seen >= 3 {
			cut()
			return false
		}
		return true
	})
	require.NotEmpty(t, taskID, "the first connection must have named the task")

	// Reattach through the other replica.
	var (
		states    []string
		artifacts int
	)
	require.NoError(t, second.rpc(ctx, "SubscribeToTask", map[string]any{"id": taskID},
		func(event map[string]any) bool {
			if state := stateFrom(event); state != "" {
				states = append(states, state)
			}
			result, _ := event["result"].(map[string]any)
			if _, ok := result["artifactUpdate"]; ok {
				artifacts++
			}
			return true
		}))

	require.NotEmpty(t, states, "the resubscription produced no state")
	assert.Equal(t, string(a2a.TaskStateCompleted), states[len(states)-1],
		"the resubscription must end in the run's real state")
	assert.Positive(t, artifacts, "the resubscription must replay the answer it missed")
}

// TestCancelThroughAnotherReplica pins the other half: a cancel that arrives at
// a replica which is not executing the task still ends the run.
func TestCancelThroughAnotherReplica(t *testing.T) {
	client := newRedis(t)
	runner := &scriptedRunner{hold: true}
	first := startReplica(t, client, "consumer-a", runner)
	second := startReplica(t, client, "consumer-b", runner)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()

	var (
		taskID  string
		mu      sync.Mutex
		lastRun string
	)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_ = first.rpc(streamCtx, "SendStreamingMessage", map[string]any{
			"message": map[string]any{
				"messageId": "m-2",
				"role":      "ROLE_USER",
				"contextId": "ctx-2",
				"parts":     []any{map[string]any{"text": "一直写下去"}},
			},
		}, func(event map[string]any) bool {
			if id := taskIDFrom(event); id != "" {
				mu.Lock()
				taskID = id
				mu.Unlock()
			}
			if state := stateFrom(event); state != "" {
				mu.Lock()
				lastRun = state
				mu.Unlock()
			}
			return true
		})
	}()

	// Wait until the run exists, then cancel it from the other replica.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return taskID != ""
	}, 10*time.Second, 20*time.Millisecond)

	_, err := second.call(ctx, "CancelTask", map[string]any{"id": taskID})
	require.NoError(t, err)

	select {
	case <-streamDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the streaming call never ended after the cancel")
	}
	// The run itself has to have been stopped: the cancel reached whichever
	// replica was executing, through the stop registry rather than through this
	// process's memory.
	assert.NotEmpty(t, runner.stopRequests(), "the run behind the task must have been stopped")

	// And the task has to end in a terminal state, read back through the replica
	// that did not execute it.
	task, err := second.call(ctx, "GetTask", map[string]any{"id": taskID})
	require.NoError(t, err)
	final := stateFrom(task)
	assert.Equal(t, string(a2a.TaskStateCanceled), final)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("stream final state: %s", lastRun)
}
