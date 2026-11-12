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
// The original repo for this file is https://github.com/onexstack/onex.

// Package ratelimit bounds how much one person may ask in a day.
//
// It is a quota, not a throttle: the per-conversation concurrency gate already
// stops a single conversation from running two turns at once, and this stops a
// single person from running a thousand. The two answer different questions and
// either alone is insufficient.
//
// A Redis failure allows the request. That is the same stance the inbound audit
// takes, and for the same reason: the quota protects capacity, and turning a
// cache outage into an outage of the product is a strictly worse trade than
// letting a few extra requests through.
package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter enforces a per-sender daily quota.
type Limiter struct {
	rdb    *redis.Client
	prefix string
	limit  int
	// now is injectable so the day boundary can be asserted.
	now func() time.Time
}

// New returns a Limiter, or nil when there is no quota (a non-positive limit)
// or nowhere to count (a nil client). A nil Limiter allows everything, which
// spares every call site a nil check.
func New(rdb *redis.Client, keyPrefix string, dailyLimit int) *Limiter {
	if rdb == nil || dailyLimit <= 0 {
		return nil
	}
	if keyPrefix == "" {
		keyPrefix = "onexai"
	}
	return &Limiter{rdb: rdb, prefix: keyPrefix, limit: dailyLimit, now: time.Now}
}

// Enabled reports whether a quota is in force. It is safe on a nil Limiter,
// which spares every call site a nil check.
func (l *Limiter) Enabled() bool { return l != nil && l.rdb != nil && l.limit > 0 }

// Limit returns the configured daily quota, or 0 when there is none.
func (l *Limiter) Limit() int {
	if l == nil {
		return 0
	}
	return l.limit
}

// Allow charges one request against the sender's daily quota.
//
// It reports whether the request may proceed and how many remain, with -1
// meaning "unlimited" (no limiter configured). A sender with no id is not
// counted: there is nothing to attribute the request to, and counting it under
// the empty string would make one quota shared by every anonymous caller.
func (l *Limiter) Allow(ctx context.Context, senderID string) (bool, int, error) {
	if l == nil {
		return true, -1, nil
	}
	if senderID == "" {
		return true, -1, nil
	}

	key := l.key(senderID)
	count, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		// See the package comment: a quota that fails closed is an outage.
		return true, -1, fmt.Errorf("ratelimit: counting %q: %w", senderID, err)
	}
	if count == 1 {
		// First request of the day for this sender: the counter expires when
		// the day does, so it is a daily quota rather than a lifetime one.
		if err := l.rdb.Expire(ctx, key, l.untilTomorrow()).Err(); err != nil {
			return true, -1, fmt.Errorf("ratelimit: setting the day's expiry: %w", err)
		}
	}
	if count > int64(l.limit) {
		// Give the increment back. Without this the counter climbs for every
		// refused request, and at a day boundary the expiry it carries would be
		// recomputed from a value that no longer means "requests today".
		if err := l.rdb.Decr(ctx, key).Err(); err != nil {
			return false, 0, fmt.Errorf("ratelimit: refunding a refused request: %w", err)
		}
		return false, 0, nil
	}
	return true, l.limit - int(count), nil
}

// key is the counter for one sender on one day.
//
// The date is part of the key as well as the TTL: the TTL alone would leave a
// counter readable for a moment after midnight, during which the previous day's
// usage would be charged to the new one.
func (l *Limiter) key(senderID string) string {
	return fmt.Sprintf("%s:quota:%s:%s", l.prefix, l.now().Format("20060102"), senderID)
}

// untilTomorrow is how long the counter should live.
func (l *Limiter) untilTomorrow() time.Duration {
	now := l.now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	return midnight.Sub(now)
}

// Describe renders the refusal for a caller, naming the quota that was reached.
func Describe(limit int) string {
	return fmt.Sprintf("今日请求次数已达上限（%s 次），请明天再试", strconv.Itoa(limit))
}
