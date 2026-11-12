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

package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenLimiter enforces a per-sender daily ceiling on tokens spent.
//
// # Why it is not just a Limiter with bigger numbers
//
// A request quota can be charged when the request arrives, because "one request"
// is known before the work is done. Tokens are not: the cost of a run is only
// known after the model has answered, and it varies by two orders of magnitude
// between "你好" (hello) and a long multi-round tool loop. So this is two operations
// rather than one — Allow before the run reads the day's total, Add after it
// records what the run spent — and a sender can therefore overshoot the ceiling
// by one run. That is the correct trade: refusing a run that would have been
// cheap, on the strength of an estimate, is worse than letting the day end
// slightly over budget, which the next run's check then catches.
//
// # Fail-open, like the request quota
//
// A ceiling that fails closed turns a Redis blip into "nobody may use the
// assistant". See the package comment.
type TokenLimiter struct {
	rdb    *redis.Client
	prefix string
	limit  int64
	// now is injectable so the day boundary can be asserted.
	now func() time.Time
}

// NewTokenLimiter returns a TokenLimiter, or nil when there is no ceiling (a
// non-positive limit) or nowhere to count (a nil client). A nil TokenLimiter
// allows everything, which spares every call site a nil check.
func NewTokenLimiter(rdb *redis.Client, keyPrefix string, dailyLimit int64) *TokenLimiter {
	if rdb == nil || dailyLimit <= 0 {
		return nil
	}
	if keyPrefix == "" {
		keyPrefix = "onexai"
	}
	return &TokenLimiter{rdb: rdb, prefix: keyPrefix, limit: dailyLimit, now: time.Now}
}

// Enabled reports whether a ceiling is in force. It is safe on a nil
// TokenLimiter.
func (l *TokenLimiter) Enabled() bool { return l != nil && l.rdb != nil && l.limit > 0 }

// Limit returns the configured daily ceiling, or 0 when there is none.
func (l *TokenLimiter) Limit() int64 {
	if l == nil {
		return 0
	}
	return l.limit
}

// Allow reports whether a sender may start a run: it is true while the day's
// spend is still below the ceiling, and it charges nothing.
//
// Charging nothing is what separates it from Limiter.Allow. That method
// increments and then gives the increment back when it refuses, because a
// refused request has to leave the count where it was; here there is no count to
// leave alone, since what a run costs is not known yet.
//
// A sender with no id is always allowed: there is nothing to attribute the spend
// to, and counting it under the empty string would pool every anonymous caller
// into one ceiling.
func (l *TokenLimiter) Allow(ctx context.Context, senderID string) (bool, int64, error) {
	if l == nil || senderID == "" {
		return true, -1, nil
	}

	used, err := l.rdb.Get(ctx, l.key(senderID)).Int64()
	if err == redis.Nil {
		// Nothing spent today: the key appears on the first charge, so its
		// absence is a zero rather than an error.
		return true, l.limit, nil
	}
	if err != nil {
		return true, -1, fmt.Errorf("ratelimit: reading tokens spent by %q: %w", senderID, err)
	}
	if used >= l.limit {
		return false, 0, nil
	}
	return true, l.limit - used, nil
}

// Add charges tokens a finished run spent.
//
// A non-positive count is not written: a run that made no model call owes
// nothing, and writing 0 would create the day's key (and its TTL) for a sender
// who has not spent anything yet.
func (l *TokenLimiter) Add(ctx context.Context, senderID string, tokens int64) error {
	if l == nil || senderID == "" || tokens <= 0 {
		return nil
	}

	key := l.key(senderID)
	// The expiry is set with the increment rather than on the first write only:
	// both calls in one pipeline is a single round trip, and re-asserting the
	// same midnight on every charge cannot drift the way a "only when the
	// counter was 0" condition can when two runs of the same sender finish
	// together.
	pipe := l.rdb.TxPipeline()
	pipe.IncrBy(ctx, key, tokens)
	pipe.Expire(ctx, key, l.untilTomorrow())
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("ratelimit: charging %d tokens to %q: %w", tokens, senderID, err)
	}
	return nil
}

// key is the per-sender, per-day token counter. The date is part of the key as
// well as the TTL, for the reason Limiter.key gives: the TTL alone would leave
// yesterday's total readable for a moment after midnight.
func (l *TokenLimiter) key(senderID string) string {
	return fmt.Sprintf("%s:tokens:%s:%s", l.prefix, l.now().Format("20060102"), senderID)
}

// untilTomorrow is how long the counter should live.
func (l *TokenLimiter) untilTomorrow() time.Duration {
	now := l.now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	return midnight.Sub(now)
}

// DescribeTokens renders the refusal for a caller, naming the ceiling reached.
//
// Millions are reported in units of ten thousand, which is how the console's own quota copy reads:
// a sentence naming "8000000" would be a number nobody converts in their head.
func DescribeTokens(limit int64) string {
	if limit >= 10000 {
		return fmt.Sprintf("今日 AI 用量已达上限（%d 万 token），请明天再试", limit/10000)
	}
	return fmt.Sprintf("今日 AI 用量已达上限（%d token），请明天再试", limit)
}
