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
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/redis/go-redis/v9"
)

// EventQueuePrefix is the namespace the event queue owns, under the
// deployment's Redis prefix.
const EventQueuePrefix = ":agent:a2a:events"

const (
	// eventBlock is how long one read blocks before the caller is given the
	// chance to notice a cancelled context. Short enough that a shutdown does
	// not wait, long enough that an idle stream is not a polling loop.
	eventBlock = 500 * time.Millisecond
	// eventMaxLen caps one task's stream. An answer is one entry per chunk, so
	// this bounds memory per running task rather than per deployment.
	eventMaxLen = 4096
	// eventTTL bounds how long a finished task's events are kept. The task
	// itself is in the task store; the events exist so a client that lost its
	// connection can catch up, and a day is longer than any retry policy.
	eventTTL = 24 * time.Hour
)

// RedisEventQueue carries a task's events between replicas.
//
// It is what makes resubscribe work across pods: the pod executing a task
// appends events to a Redis stream, and any pod a reconnecting client lands on
// replays that stream. The SDK ships only an in-memory queue, where a
// subscription can reach nothing but the process producing the run.
//
// A Redis stream rather than pub/sub on purpose: pub/sub drops what was
// published before a subscriber attached, which is exactly the case a
// resubscribe exists for. The replay is safe because the SDK's subscription
// yields the stored task first and skips events at or below that version, so a
// reader that starts at the beginning cannot duplicate an answer.
type RedisEventQueue struct {
	client *redis.Client
	prefix string
}

var _ eventqueue.Manager = (*RedisEventQueue)(nil)

// NewRedisEventQueue returns an event queue over client, namespaced by prefix.
func NewRedisEventQueue(client *redis.Client, prefix string) *RedisEventQueue {
	return &RedisEventQueue{client: client, prefix: prefix + EventQueuePrefix}
}

// key is one task's stream.
func (q *RedisEventQueue) key(taskID a2a.TaskID) string {
	return q.prefix + ":" + string(taskID)
}

// CreateReader implements eventqueue.Manager.
//
// The reader starts at the beginning of the stream, which is what lets a
// resubscribe see the events it missed; see the type comment for why that is
// safe.
func (q *RedisEventQueue) CreateReader(_ context.Context, taskID a2a.TaskID) (eventqueue.Reader, error) {
	slog.Debug("a2a: subscribing to a task's events", "task", taskID, "key", q.key(taskID))
	return &redisEventReader{client: q.client, key: q.key(taskID), lastID: "0"}, nil
}

// CreateWriter implements eventqueue.Manager.
func (q *RedisEventQueue) CreateWriter(_ context.Context, taskID a2a.TaskID) (eventqueue.Writer, error) {
	return &redisEventWriter{client: q.client, key: q.key(taskID)}, nil
}

// Destroy implements eventqueue.Manager.
func (q *RedisEventQueue) Destroy(ctx context.Context, taskID a2a.TaskID) error {
	if err := q.client.Del(ctx, q.key(taskID)).Err(); err != nil {
		return fmt.Errorf("a2a: destroy event queue: %w", err)
	}
	return nil
}

// redisEventWriter appends one task's events.
type redisEventWriter struct {
	client *redis.Client
	key    string
}

var _ eventqueue.Writer = (*redisEventWriter)(nil)

// Write implements eventqueue.Writer.
func (w *redisEventWriter) Write(ctx context.Context, msg *eventqueue.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("a2a: encode event: %w", err)
	}

	pipe := w.client.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: w.key,
		MaxLen: eventMaxLen,
		Approx: true,
		Values: map[string]any{"message": string(payload)},
	})
	// The TTL slides with every event, so a running task's stream never expires
	// under it and a finished task's stream is bounded from its last write.
	pipe.Expire(ctx, w.key, eventTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("a2a: write event: %w", err)
	}
	return nil
}

// Close implements eventqueue.Writer.
func (w *redisEventWriter) Close() error { return nil }

// redisEventReader replays and tails one task's events.
type redisEventReader struct {
	client *redis.Client
	key    string
	// lastID is the stream cursor. It starts at "0" — Redis rejects an empty id —
	// which reads the stream from the beginning: that is what lets a client that
	// lost its connection see the events it missed. Replaying is safe because the
	// SDK yields the stored task first and skips events up to its version.
	lastID string
	closed atomic.Bool
}

var _ eventqueue.Reader = (*redisEventReader)(nil)

// Read implements eventqueue.Reader.
func (r *redisEventReader) Read(ctx context.Context) (*eventqueue.Message, error) {
	for {
		if r.closed.Load() {
			return nil, eventqueue.ErrQueueClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		streams, err := r.client.XRead(ctx, &redis.XReadArgs{
			Streams: []string{r.key, r.lastID},
			Block:   eventBlock,
			Count:   1,
		}).Result()
		if errors.Is(err, redis.Nil) {
			// Nothing new within the block window; loop so a cancellation is
			// noticed promptly.
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("a2a: read event: %w", err)
		}

		for _, stream := range streams {
			for _, entry := range stream.Messages {
				r.lastID = entry.ID
				payload, ok := entry.Values["message"].(string)
				if !ok {
					// A stream holding something else is not ours to interpret;
					// skipping keeps a foreign writer from killing a subscriber.
					continue
				}
				var msg eventqueue.Message
				if err := json.Unmarshal([]byte(payload), &msg); err != nil {
					return nil, fmt.Errorf("a2a: decode event: %w", err)
				}
				return &msg, nil
			}
		}
	}
}

// Close implements eventqueue.Reader.
func (r *redisEventReader) Close() error {
	r.closed.Store(true)
	return nil
}
