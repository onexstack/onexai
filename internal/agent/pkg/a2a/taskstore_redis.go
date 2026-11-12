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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/redis/go-redis/v9"
)

// TaskStorePrefix is the key namespace the task store owns, under the
// deployment's Redis prefix.
const TaskStorePrefix = ":agent:a2a"

// Compile-time proof that this type is what the SDK will call.
var _ taskstore.Store = (*RedisTaskStore)(nil)

// RedisTaskStore keeps A2A tasks in Redis, so a task survives a restart and is
// visible to every replica.
//
// The SDK ships only an in-memory store, which makes tasks/get, tasks/list and
// tasks/resubscribe properties of one process. This is the replacement for the
// first two: a task is a hash (<prefix>:agent:a2a:task:<id>) and each user owns
// a sorted set of their task ids scored by last update
// (<prefix>:agent:a2a:tasks:<user>), which is what List pages through.
//
// What it does not do is stream: resubscribe still reaches only the replica
// producing the run, because the event queue stays in memory. Cross-replica
// streaming needs a distributed work queue and lease, which is a different
// piece of work with a different failure mode.
type RedisTaskStore struct {
	client *redis.Client
	prefix string
	// authenticate names the task's owner, exactly as the SDK's own store does:
	// the A2A user the transport put in the call context. Ownership mismatch is
	// reported as "not found", per the spec, so a caller cannot probe for other
	// users' task ids.
	authenticate taskstore.Authenticator
}

// NewRedisTaskStore returns a store over client, namespaced by prefix.
func NewRedisTaskStore(client *redis.Client, prefix string) *RedisTaskStore {
	return &RedisTaskStore{
		client:       client,
		prefix:       prefix + TaskStorePrefix,
		authenticate: a2asrv.NewTaskStoreAuthenticator(),
	}
}

// taskKey is where one task lives.
func (s *RedisTaskStore) taskKey(id a2a.TaskID) string {
	return s.prefix + ":task:" + string(id)
}

// indexKey is the user's ordered set of task ids.
func (s *RedisTaskStore) indexKey(user string) string {
	return s.prefix + ":tasks:" + user
}

// createScript stores a task only when its id is unused.
//
// The check and the write are one script because two replicas answering two
// first messages for the same id must not both succeed: the loser has to learn
// that the task exists so it can append to it instead.
var createScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
	return 0
end
redis.call('HSET', KEYS[1], 'task', ARGV[2], 'version', 1, 'user', ARGV[1])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
return 1
`)

// updateScript advances a task under optimistic concurrency.
//
// Returns -1 for "absent or owned by someone else" (indistinguishable on
// purpose), -2 for a version mismatch, and the new version otherwise.
var updateScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
	return -1
end
if redis.call('HGET', KEYS[1], 'user') ~= ARGV[1] then
	return -1
end
local version = tonumber(redis.call('HGET', KEYS[1], 'version'))
local prev = tonumber(ARGV[2])
if prev ~= 0 and version ~= prev then
	return -2
end
local next_version = version + 1
redis.call('HSET', KEYS[1], 'task', ARGV[3], 'version', next_version)
redis.call('ZADD', KEYS[2], ARGV[4], ARGV[5])
return next_version
`)

// Create implements taskstore.Store.
func (s *RedisTaskStore) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	if task == nil || task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: a task needs an id: %w", a2a.ErrInvalidParams)
	}
	user, err := s.authenticate(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: task owner: %w", err)
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: encode task: %w", err)
	}

	created, err := createScript.Run(ctx, s.client,
		[]string{s.taskKey(task.ID), s.indexKey(user)},
		user, payload, time.Now().UnixNano(), string(task.ID),
	).Int()
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: create task: %w", err)
	}
	if created == 0 {
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	}
	return 1, nil
}

// Update implements taskstore.Store.
func (s *RedisTaskStore) Update(ctx context.Context, req *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	if req == nil || req.Task == nil || req.Task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: an update needs a task id: %w", a2a.ErrInvalidParams)
	}
	user, err := s.authenticate(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: task owner: %w", err)
	}
	payload, err := json.Marshal(req.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: encode task: %w", err)
	}

	version, err := updateScript.Run(ctx, s.client,
		[]string{s.taskKey(req.Task.ID), s.indexKey(user)},
		user, int64(req.PrevVersion), payload, time.Now().UnixNano(), string(req.Task.ID),
	).Int64()
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: update task: %w", err)
	}
	switch version {
	case -1:
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	case -2:
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}
	return taskstore.TaskVersion(version), nil
}

// Get implements taskstore.Store.
func (s *RedisTaskStore) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("a2a: task owner: %w", err)
	}
	stored, err := s.load(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if stored.User != user {
		// The spec says ownership mismatch reads as "not found", so the caller
		// learns nothing about another user's task. This line is what tells an
		// operator the difference between a missing task and a wrong caller —
		// which is the one thing a resubscribe bug looks like from the outside.
		slog.Debug("a2a: masking a task owned by someone else as not found",
			"task", taskID, "owner", stored.User, "caller", user)
		return nil, a2a.ErrTaskNotFound
	}
	return stored, nil
}

// List implements taskstore.Store.
//
// The user's index is read whole rather than in a window when a filter is
// present, because the filters (context, state, status time) are not part of the
// index and a window would answer with fewer results than exist. TotalSize is
// therefore exact for the filtered set.
func (s *RedisTaskStore) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	const defaultPageSize = 50

	user, err := s.authenticate(ctx)
	if user == "" || err != nil {
		return nil, a2a.ErrUnauthenticated
	}

	pageSize := defaultPageSize
	if req != nil && req.PageSize != 0 {
		pageSize = req.PageSize
	}
	if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("a2a: page size must be between 1 and 100 inclusive, got %d: %w",
			pageSize, a2a.ErrInvalidRequest)
	}

	entries, err := s.client.ZRevRangeWithScores(ctx, s.indexKey(user), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("a2a: list tasks: %w", err)
	}

	var cursorTime time.Time
	var cursorID a2a.TaskID
	if req != nil && req.PageToken != "" {
		cursorTime, cursorID, err = decodePageToken(req.PageToken)
		if err != nil {
			return nil, err
		}
	}

	skipped := 0
	for i, entry := range entries {
		id, _ := entry.Member.(string)
		if !afterCursor(entry.Score, id, cursorTime, cursorID) {
			skipped = i + 1
		}
	}
	entries = entries[skipped:]

	var page []*a2a.Task
	total := 0
	for _, entry := range entries {
		id, _ := entry.Member.(string)
		stored, err := s.load(ctx, a2a.TaskID(id))
		if err != nil {
			if errors.Is(err, a2a.ErrTaskNotFound) {
				continue
			}
			return nil, err
		}
		if !matches(stored.Task, req) {
			continue
		}
		total++
		if len(page) < pageSize {
			page = append(page, trimForList(stored.Task, req))
		}
	}

	next := ""
	if total > len(page) {
		last := page[len(page)-1]
		next = encodePageToken(timeFromEntries(entries, string(last.ID)), last.ID)
	}

	return &a2a.ListTasksResponse{
		Tasks:         page,
		TotalSize:     total,
		PageSize:      pageSize,
		NextPageToken: next,
	}, nil
}

// load reads one stored task.
func (s *RedisTaskStore) load(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	fields, err := s.client.HGetAll(ctx, s.taskKey(taskID)).Result()
	if err != nil {
		return nil, fmt.Errorf("a2a: read task: %w", err)
	}
	if len(fields) == 0 {
		return nil, a2a.ErrTaskNotFound
	}

	task := &a2a.Task{}
	if payload := fields["task"]; payload != "" {
		if err := json.Unmarshal([]byte(payload), task); err != nil {
			return nil, fmt.Errorf("a2a: decode task %s: %w", taskID, err)
		}
	}
	version, _ := strconv.ParseInt(fields["version"], 10, 64)
	return &taskstore.StoredTask{
		Task:    task,
		Version: taskstore.TaskVersion(version),
		User:    fields["user"],
	}, nil
}

// matches reports whether a stored task satisfies the list filter.
func matches(task *a2a.Task, req *a2a.ListTasksRequest) bool {
	if task == nil {
		return false
	}
	if req == nil {
		return true
	}
	if req.ContextID != "" && task.ContextID != req.ContextID {
		return false
	}
	if req.Status != a2a.TaskStateUnspecified && task.Status.State != req.Status {
		return false
	}
	if req.StatusTimestampAfter != nil && task.Status.Timestamp != nil &&
		task.Status.Timestamp.Before(*req.StatusTimestampAfter) {
		return false
	}
	return true
}

// trimForList applies the request's history and artifact trimming, mirroring
// the SDK's own store so a caller cannot tell which one answered.
func trimForList(task *a2a.Task, req *a2a.ListTasksRequest) *a2a.Task {
	const defaultMaxHistoryLength = 100

	historyLength := defaultMaxHistoryLength
	if req != nil && req.HistoryLength != nil {
		historyLength = *req.HistoryLength
	}
	switch {
	case historyLength <= 0:
		task.History = []*a2a.Message{}
	case len(task.History) > historyLength:
		task.History = task.History[len(task.History)-historyLength:]
	}
	if req == nil || !req.IncludeArtifacts {
		task.Artifacts = nil
	}
	return task
}

// afterCursor reports whether (score, id) comes after the cursor in the index's
// order: newest first, ties broken by descending id.
func afterCursor(score float64, id string, cursorTime time.Time, cursorID a2a.TaskID) bool {
	if cursorID == "" {
		return true
	}
	at := time.Unix(0, int64(score))
	if !at.Equal(cursorTime) {
		return at.Before(cursorTime)
	}
	return id < string(cursorID)
}

// timeFromEntries is the score of the entry naming id, which is how the next
// page token gets its cursor.
func timeFromEntries(entries []redis.Z, id string) time.Time {
	for _, entry := range entries {
		if member, _ := entry.Member.(string); member == id {
			return time.Unix(0, int64(entry.Score))
		}
	}
	return time.Now()
}

// encodePageToken mirrors the SDK's token format, so a token minted by one store
// is understood by the other.
func encodePageToken(at time.Time, taskID a2a.TaskID) string {
	return base64.URLEncoding.EncodeToString(fmt.Appendf(nil, "%s_%s", at.Format(time.RFC3339Nano), taskID))
}

// decodePageToken is the inverse of encodePageToken.
func decodePageToken(token string) (time.Time, a2a.TaskID, error) {
	decoded, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	at, id, found := strings.Cut(string(decoded), "_")
	if !found {
		return time.Time{}, "", a2a.ErrParseError
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	return parsed, a2a.TaskID(id), nil
}
