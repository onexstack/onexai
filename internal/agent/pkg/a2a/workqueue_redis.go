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
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// WorkQueuePrefix is the namespace the work queue owns, under the deployment's
// Redis prefix.
const WorkQueuePrefix = ":agent:a2a:work"

const (
	// workGroup is the consumer group every replica joins. It is what turns the
	// stream into a work queue: each entry is delivered to exactly one consumer.
	workGroup = "onexai"
	// workMaxLen caps the queue. Entries are small (a request plus its encoded
	// call context) and are acknowledged as soon as they are handled.
	workMaxLen = 8192
	// leaseTTL is how long a task's lease survives without a heartbeat. It is
	// longer than the heartbeat interval by design: a busy replica must not lose
	// its task to a single missed beat.
	leaseTTL = 2 * time.Minute
	// defaultWorkers is how many tasks one replica executes at once when the
	// SDK asks for no particular number.
	defaultWorkers = 4
	// reclaimIdle is how long an unacknowledged entry must sit before another
	// replica takes it. It has to exceed leaseTTL by a wide margin: a task whose
	// lease is still being refreshed is being executed, not abandoned.
	reclaimIdle = 10 * time.Minute
	// reclaimEvery is how often a replica looks for abandoned entries.
	reclaimEvery = time.Minute
	// workBlock bounds one read so a shutdown is noticed promptly.
	workBlock = 500 * time.Millisecond
)

// acquireLeaseScript claims the right to run (or cancel) one task.
//
// KEYS[1] is the execution lease, KEYS[2] the cancellation lease. An execution
// is refused while either is held: a task being cancelled must not start
// running again. A cancellation is refused only while another cancellation is
// in flight, because that is the operation it would duplicate.
var acquireLeaseScript = redis.NewScript(`
if ARGV[1] == 'execute' then
	if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
	if redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
	redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
	return 1
end
if redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
return 1
`)

// releaseLeaseScript deletes the lease only if this holder still owns it, so a
// slow finisher cannot release a lease that has already been taken over.
var releaseLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

// refreshLeaseScript extends the lease under the same ownership check.
var refreshLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// RedisWorkQueue distributes task executions across replicas.
//
// The replica that receives a request publishes it here and subscribes to the
// task's events; whichever replica claims the work executes it and streams its
// events into the event queue. That is the half of resubscribe the event queue
// cannot provide on its own: without it, an execution is a property of the
// replica that accepted the request and a second replica has nothing to replay
// because nothing is running there.
//
// The lease is per task and lives in Redis rather than in a process map, so a
// cancel that arrives at a different replica than the one executing is still
// refused while that execution is in flight — which is what makes "cancel is
// idempotent, not a queue of cancels" hold across pods.
type RedisWorkQueue struct {
	client   *redis.Client
	stream   string
	leaseKey string
	consumer string
	ttl      time.Duration

	mu      sync.Mutex
	handler workqueue.HandlerFn
	started bool
	stop    context.CancelFunc
	baseCtx context.Context
}

var _ workqueue.Queue = (*RedisWorkQueue)(nil)

// NewRedisWorkQueue returns a work queue over client, namespaced by prefix and
// stopping when ctx ends.
//
// consumer identifies this replica in the consumer group. It has to be unique
// per replica: two processes sharing one name would split acknowledgements
// between them and leave entries pending under a name nobody claims.
func NewRedisWorkQueue(ctx context.Context, client *redis.Client, prefix, consumer string) *RedisWorkQueue {
	return &RedisWorkQueue{
		client:   client,
		stream:   prefix + WorkQueuePrefix,
		leaseKey: prefix + WorkQueuePrefix + ":lease",
		consumer: consumer,
		ttl:      leaseTTL,
		baseCtx:  ctx,
	}
}

// Write implements workqueue.Writer.
func (q *RedisWorkQueue) Write(ctx context.Context, payload *workqueue.Payload) (a2a.TaskID, error) {
	if payload == nil || payload.TaskID == "" {
		return "", fmt.Errorf("a2a: a work item needs a payload and a task id: %w", workqueue.ErrMalformedPayload)
	}

	execKey := q.leaseKeyFor(payload.TaskID, workqueue.PayloadTypeExecute)
	cancelKey := q.leaseKeyFor(payload.TaskID, workqueue.PayloadTypeCancel)
	token := uuid.NewString()

	acquired, err := acquireLeaseScript.Run(ctx, q.client,
		[]string{execKey, cancelKey},
		string(payload.Type), token, q.ttl.Milliseconds(),
	).Int()
	if err != nil {
		return "", fmt.Errorf("a2a: acquire lease: %w", err)
	}
	if acquired == 0 {
		return "", workqueue.ErrLeaseAlreadyTaken
	}

	lease := workLease{Key: execKey, Token: token}
	if payload.Type != workqueue.PayloadTypeExecute {
		lease.Key = cancelKey
	}
	raw, err := json.Marshal(workEnvelope{Payload: payload, Lease: lease})
	if err != nil {
		q.release(context.WithoutCancel(ctx), lease)
		return "", fmt.Errorf("a2a: encode work item: %w", err)
	}

	if err := q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		MaxLen: workMaxLen,
		Approx: true,
		Values: map[string]any{"work": string(raw)},
	}).Err(); err != nil {
		q.release(context.WithoutCancel(ctx), lease)
		return "", fmt.Errorf("a2a: enqueue work: %w", err)
	}
	return payload.TaskID, nil
}

// RegisterHandler implements workqueue.Queue.
//
// It starts one worker per allowed concurrent execution, plus one that reclaims
// entries abandoned by a replica that died. Workers run until the context given
// to the constructor ends or Shutdown is called.
func (q *RedisWorkQueue) RegisterHandler(cfg workqueue.HandlerConfig, fn workqueue.HandlerFn) {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return
	}
	q.handler = fn
	q.started = true
	ctx, cancel := context.WithCancel(q.baseCtx)
	q.stop = cancel
	q.mu.Unlock()

	if err := q.ensureGroup(ctx); err != nil {
		slog.Error("a2a: the work queue has no consumer group; executions will not be distributed",
			"error", err)
		cancel()
		return
	}

	workers := cfg.Limiter.MaxExecutions
	if workers <= 0 {
		workers = defaultWorkers
	}
	for i := range workers {
		go q.consume(ctx, fmt.Sprintf("%s-%d", q.consumer, i))
	}
	go q.reclaim(ctx)
}

// Shutdown stops the workers. It exists for tests and for a graceful stop; the
// process ending is enough in production, because the consumer group keeps the
// unfinished entries for the next replica.
func (q *RedisWorkQueue) Shutdown() {
	q.mu.Lock()
	stop := q.stop
	q.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// ensureGroup creates the consumer group if this is the first replica.
func (q *RedisWorkQueue) ensureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, q.stream, workGroup, "0").Err()
	if err != nil && !isBusyGroup(err) {
		return fmt.Errorf("a2a: create consumer group: %w", err)
	}
	return nil
}

// isBusyGroup reports whether the group already exists, which is the normal
// case for every replica after the first.
func isBusyGroup(err error) bool {
	return err != nil && (errors.Is(err, redis.Nil) || strings.Contains(err.Error(), "BUSYGROUP"))
}

// consume reads work as one member of the group until ctx ends.
func (q *RedisWorkQueue) consume(ctx context.Context, consumer string) {
	for ctx.Err() == nil {
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    workGroup,
			Consumer: consumer,
			Streams:  []string{q.stream, ">"},
			Count:    1,
			Block:    workBlock,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("a2a: work queue read failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, stream := range streams {
			for _, entry := range stream.Messages {
				q.handle(ctx, entry)
				if err := q.client.XAck(ctx, q.stream, workGroup, entry.ID).Err(); err != nil &&
					ctx.Err() == nil {
					slog.Warn("a2a: work queue acknowledge failed", "error", err)
				}
			}
		}
	}
}

// reclaim takes over entries a dead replica left unacknowledged.
//
// An entry whose lease is still alive belongs to a replica that is executing it,
// so it is acknowledged rather than run twice: the owner finishes and its own
// acknowledgement is then a no-op.
func (q *RedisWorkQueue) reclaim(ctx context.Context) {
	ticker := time.NewTicker(reclaimEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		entries, _, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   q.stream,
			Group:    workGroup,
			Consumer: q.consumer + "-reclaimer",
			MinIdle:  reclaimIdle,
			Start:    "0-0",
			Count:    10,
		}).Result()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("a2a: work queue reclaim failed", "error", err)
			}
			continue
		}

		for _, entry := range entries {
			if q.leaseAlive(ctx, entry) {
				slog.Info("a2a: reclaimed an entry whose owner is still running", "entry", entry.ID)
			} else {
				slog.Warn("a2a: taking over work abandoned by a replica", "entry", entry.ID)
				q.handle(ctx, entry)
			}
			if err := q.client.XAck(ctx, q.stream, workGroup, entry.ID).Err(); err != nil && ctx.Err() == nil {
				slog.Warn("a2a: work queue acknowledge after reclaim failed", "error", err)
			}
		}
	}
}

// leaseAlive reports whether an entry's lease is still held.
func (q *RedisWorkQueue) leaseAlive(ctx context.Context, entry redis.XMessage) bool {
	envelope, err := decodeWorkEnvelope(entry)
	if err != nil {
		return false
	}
	n, err := q.client.Exists(ctx, envelope.Lease.Key).Result()
	return err == nil && n == 1
}

// handle runs one entry's work, holding its lease for as long as it runs.
func (q *RedisWorkQueue) handle(ctx context.Context, entry redis.XMessage) {
	envelope, err := decodeWorkEnvelope(entry)
	if err != nil {
		slog.Error("a2a: undecodable work item; dropping it", "entry", entry.ID, "error", err)
		return
	}

	q.mu.Lock()
	handler := q.handler
	q.mu.Unlock()
	if handler == nil {
		return
	}

	lease := &redisLease{client: q.client, lease: envelope.Lease, ttl: q.ttl, taskID: envelope.Payload.TaskID}
	beatCtx, stopBeat := context.WithCancel(ctx)
	go lease.beat(beatCtx)
	defer func() {
		stopBeat()
		lease.Release(context.WithoutCancel(ctx))
	}()

	if _, err := handler(workqueue.AttachHeartbeater(ctx, lease), envelope.Payload); err != nil {
		slog.Warn("a2a: work item failed", "task", envelope.Payload.TaskID,
			"type", envelope.Payload.Type, "error", err)
	}
}

// leaseKeyFor is one task's lease for one kind of work.
func (q *RedisWorkQueue) leaseKeyFor(taskID a2a.TaskID, kind workqueue.PayloadType) string {
	scope := "cancel"
	if kind == workqueue.PayloadTypeExecute {
		scope = "exec"
	}
	return q.leaseKey + ":" + scope + ":" + string(taskID)
}

// release drops a lease this queue took out but never handed to a worker.
func (q *RedisWorkQueue) release(ctx context.Context, lease workLease) {
	releaseLeaseScript.Run(ctx, q.client, []string{lease.Key}, lease.Token)
}

// workEnvelope is one queued item: the SDK's payload plus the lease that says
// who may run it.
type workEnvelope struct {
	Payload *workqueue.Payload `json:"payload"`
	Lease   workLease          `json:"lease"`
}

// workLease names a held lease.
type workLease struct {
	Key   string `json:"key"`
	Token string `json:"token"`
}

// decodeWorkEnvelope reads one queue entry.
func decodeWorkEnvelope(entry redis.XMessage) (*workEnvelope, error) {
	raw, ok := entry.Values["work"].(string)
	if !ok {
		return nil, fmt.Errorf("entry %s carries no work item", entry.ID)
	}
	var envelope workEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("entry %s: %w", entry.ID, err)
	}
	if envelope.Payload == nil || envelope.Lease.Key == "" {
		return nil, fmt.Errorf("entry %s: incomplete work item", entry.ID)
	}
	return &envelope, nil
}

// redisLease is one task's exclusive right to execute, refreshed while it runs.
type redisLease struct {
	client *redis.Client
	lease  workLease
	taskID a2a.TaskID
	ttl    time.Duration
}

var _ workqueue.Heartbeater = (*redisLease)(nil)

// TaskID implements workqueue.Lease.
func (l *redisLease) TaskID() a2a.TaskID { return l.taskID }

// Release implements workqueue.Lease.
func (l *redisLease) Release(ctx context.Context) {
	releaseLeaseScript.Run(ctx, l.client, []string{l.lease.Key}, l.lease.Token)
}

// HeartbeatInterval implements workqueue.Heartbeater.
func (l *redisLease) HeartbeatInterval() time.Duration { return l.ttl / 3 }

// Heartbeat implements workqueue.Heartbeater.
func (l *redisLease) Heartbeat(ctx context.Context) error {
	return refreshLeaseScript.Run(ctx, l.client, []string{l.lease.Key},
		l.lease.Token, l.ttl.Milliseconds()).Err()
}

// beat refreshes the lease until stopped, so a long run is not mistaken for an
// abandoned one.
func (l *redisLease) beat(ctx context.Context) {
	ticker := time.NewTicker(l.HeartbeatInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.Heartbeat(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("a2a: lease heartbeat failed", "key", l.lease.Key, "error", err)
			}
		}
	}
}
