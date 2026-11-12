// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package di is the oai-agent-apiserver's composition root: one Provide*
// function per dependency, gathered into the Wire sets in sets.go and named by
// package agent's injector.
package di

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/pkg/authn"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/observe"
	"github.com/onexstack/onexai/internal/agent/pkg/ratelimit"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/pkg/idempotent"

	"github.com/onexstack/onexai/internal/pkg/readiness"
	"github.com/onexstack/onexai/internal/pkg/safego"

	"github.com/onexstack/onexai/internal/agent/store/runstore"
	customoptions "github.com/onexstack/onexai/pkg/options"

	"github.com/redis/go-redis/v9"
)

// redisPrefix is the key namespace every store in this service shares. One
// default and one lookup, so a deployment that renames it does not have to find
// six copies that agree by inspection.
func redisPrefix(cfg *Config) string {
	if cfg.RedisOptions.KeyPrefix != "" {
		return cfg.RedisOptions.KeyPrefix
	}
	return "onexai"
}

// redisTTL is the configured session TTL, or fallback when unset. Session and
// checkpoint keys expire on the same clock: a pause outliving its conversation
// would be answerable only by someone who remembers the question.
func redisTTL(cfg *Config, fallback time.Duration) time.Duration {
	if cfg.RedisOptions.SessionTTL > 0 {
		return cfg.RedisOptions.SessionTTL
	}
	return fallback
}

// resourceStoreWired reports whether the resource store can persist at all: a
// backend is selected and its connection exists. The resources themselves may
// be served by SQL (the tables that stayed behind) or by the control plane (the
// migrated kinds), so the question is "is there a store", not "is it SQL".
func resourceStoreWired(cfg *Config, db *gorm.DB) bool {
	return cfg.StoreOptions.UsesSQL() && db != nil
}

// ProvideRedisClient returns the process's one Redis client, or nil when no
// Redis is configured.
//
// # One client for the whole process
//
// Sessions, cross-pod stop signaling and the AK/SK nonce store all use this
// client. That is deliberate rather than convenient: a second pool would be a
// second set of timeouts and a second address to get wrong, for three
// mechanisms whose traffic is counted in bytes. The platform's services make
// the same choice for the same reason.
//
// # A configured Redis that does not answer is fatal
//
// The constructor pings, so this is a real connection rather than a lazy one.
// Falling back to the in-memory store here would be the worse failure: the
// operator asked for Redis, the process came up, and every conversation is now
// pinned to the pod that created it — which works in development and stops
// working under a load balancer, with nothing said about it. Refusing to start
// says so at the one moment somebody is watching.
//
// An *unconfigured* Redis is not this case: an empty address builds no client
// and the agent flow keeps its in-memory implementations, which is a deployment
// that never asked for anything else.
func ProvideRedisClient(cfg *Config) (*redis.Client, error) {
	ro := cfg.RedisOptions
	if !ro.Enabled() {
		slog.Info("Redis is not configured: agent sessions and run stops are process-local")
		return nil, nil
	}
	client, err := ro.NewClient()
	if err != nil {
		return nil, fmt.Errorf("redis: connect to %s: %w", ro.Addr, err)
	}
	// The address and the ACL user are logged; the password is not. A startup
	// log line is the first thing anyone reads when a deployment misbehaves, and
	// it is also the thing most likely to be pasted into a chat.
	slog.Info("Redis configured",
		"addr", ro.Addr, "username", ro.Username, "database", ro.Database, "key_prefix", ro.KeyPrefix)
	return client, nil
}

// ProvideReadiness answers /readyz. Only post-boot failure modes belong here:
// a dependency that cannot be reached at start-up already stopped the process.
//
// Redis is the one such dependency today. A configured Redis that stops
// answering breaks the nonce store and cross-pod run stops, so the instance
// should leave the load balancer rather than accept signed requests it cannot
// replay-check. An unconfigured Redis is not a failure: the process-local
// implementations are the deployment's choice, not a degradation.
func ProvideReadiness(rdb *redis.Client) *readiness.Registry {
	if rdb == nil {
		return readiness.New()
	}

	return readiness.New(readiness.Check{
		Name:  "redis",
		Probe: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
	})
}

// ProvideIdempotencyStore records the idempotency key of a mutating request so
// a retry cannot execute the same write twice.
//
// It shares the process's Redis client with everything else. Without Redis it
// falls back to a process-local store, which protects one replica only — the
// same limitation the nonce store documents, and the reason Redis is the
// supported configuration for a replicated deployment.
func ProvideIdempotencyStore(cfg *Config, rdb *redis.Client) idempotent.Store {
	return idempotent.New(rdb, redisPrefix(cfg))
}

// ProvideNonceStore builds the AK/SK replay-protection store.
//
// It prefers Redis so a request captured from one pod cannot be replayed against
// another; with the client nil it falls back to a process-local store, which
// only protects a single-process deployment. The returned store never disables
// the check — the fallback is always present.
func ProvideNonceStore(cfg *Config, rdb *redis.Client) authn.NonceStore {
	prefix := redisPrefix(cfg)
	return authn.NewNonceStore(rdb, prefix)
}

// ProvideSessionStore builds the conversation-session store (Redis or in-memory).
func ProvideSessionStore(cfg *Config, rdb *redis.Client, runs runstore.Store) mem.SessionStore {
	// Durable history is the source when a database is configured. Redis stays
	// the fallback for a deployment without one, and remains the event/stop
	// transport either way.
	if runs != nil {
		return mem.NewPGStore(runs)
	}
	prefix, ttl := redisPrefix(cfg), redisTTL(cfg, 24*time.Hour)
	return mem.NewSessionStore(rdb, prefix, ttl)
}

// ProvideStopRegistry builds the run-stop registry (Redis Pub/Sub or local).
// For the Redis variant it launches the cross-pod subscription loop bound to
// the server lifetime context.
func ProvideStopRegistry(ctx context.Context, cfg *Config, rdb *redis.Client) runstop.Registry {
	prefix := redisPrefix(cfg)
	reg := runstop.New(rdb, prefix)
	if sub, ok := reg.(runstop.Subscriber); ok {
		go sub.Subscribe(ctx)
	}
	return reg
}

// ProvideCheckpointStore builds the store that keeps a run paused for approval
// alive between requests, and therefore between pods.
//
// Unlike the session and stop stores it has no local fallback worth the name:
// approval is answered by a human, minutes later, through whatever pod the load
// balancer picks, so a per-process store would make the feature work by
// coincidence. The in-memory implementation exists so a single-process
// deployment can be exercised at all; Redis is what makes it a deployment
// configuration, and the options validation says so.
func ProvideCheckpointStore(cfg *Config, rdb *redis.Client) checkpoint.Store {
	prefix, ttl := redisPrefix(cfg), redisTTL(cfg, 24*time.Hour)
	if cfg.AgentOptions.CheckpointTTL > 0 {
		ttl = cfg.AgentOptions.CheckpointTTL
	}
	return checkpoint.New(rdb, prefix, ttl)
}

// ProvideRateLimiter builds the daily quota, or nil when there is no quota or
// nowhere to count.
//
// A quota without Redis is not a degraded quota, it is no quota: there is
// nowhere to keep the count, and inventing a per-process one would give every
// replica its own copy of the same person's allowance.
func ProvideRateLimiter(cfg *Config, rdb *redis.Client) *ratelimit.Limiter {
	if cfg.AgentOptions.DailyQuota <= 0 {
		return nil
	}
	if rdb == nil {
		slog.Warn("agent.daily-quota is set but Redis is not configured; the quota is inert")
		return nil
	}
	prefix := redisPrefix(cfg)
	return ratelimit.New(rdb, prefix, cfg.AgentOptions.DailyQuota)
}

// ProvideTokenLimiter builds the daily token ceiling, or nil when there is none
// or nowhere to count.
//
// Separate from ProvideRateLimiter rather than folded into it because the two
// are different policies that a deployment can hold independently: a request
// count bounds how often someone asks, a token ceiling bounds what their asking
// costs, and a deployment may reasonably want one without the other. Both use
// the same Redis and the same key prefix, so their counters cannot collide —
// the metric name is in the key.
func ProvideTokenLimiter(cfg *Config, rdb *redis.Client) *ratelimit.TokenLimiter {
	if cfg.AgentOptions.DailyTokenQuota <= 0 {
		return nil
	}
	if rdb == nil {
		slog.Warn("agent.daily-token-quota is set but Redis is not configured; the ceiling is inert")
		return nil
	}
	prefix := redisPrefix(cfg)
	return ratelimit.NewTokenLimiter(rdb, prefix, cfg.AgentOptions.DailyTokenQuota)
}

// ProvideMetrics registers the service's Prometheus instruments.
//
// Registration is process-wide and idempotent-with-a-warning: a second call
// returns a nil sink rather than panicking, so a test that builds two servers
// does not fail on a collector collision.
// ProvideFeatureOptions exposes the alignment switches as a provider so the
// rest of the graph can inject them without every consumer reaching into
// *Config.
func ProvideFeatureOptions(cfg *Config) *customoptions.FeatureOptions {
	return cfg.FeatureOptions
}
func ProvideMetrics(feat *customoptions.FeatureOptions) *metrics.Metrics {
	m := metrics.New(prometheus.DefaultRegisterer, feat.AlignedMetricNames)
	metrics.M = m
	if m == nil {
		slog.Warn("agent metrics are unavailable; runs continue without them")
	}
	// A recovered panic is the one event that is always a bug and never a
	// condition, so it is wired as a counter rather than left to a log line
	// nobody greps for.
	safego.OnPanic(func(ctx context.Context, where string) { metrics.M.ObservePanic(where) })
	// Model-call timing comes from eino's own callback, which is process-wide
	// and must be installed before the first run.
	observe.Register(m)
	return m
}

// ProvideDB provides a database instance based on the configuration.
func ProvideDB(cfg *Config) (*gorm.DB, error) {
	return cfg.NewDB()
}
