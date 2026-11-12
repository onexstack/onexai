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

package authz

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/ristretto/v2"
)

// DefaultCacheTTL is the default TTL for authorization cache entries.
const DefaultCacheTTL = 5 * time.Minute

// CachedAuthorizer wraps an inner Authorizer with an in-memory cache backed by
// ristretto, applying the same decorator pattern as unionAuthorizer — the cache
// is transparent to Coordinator, Registry, and all callers.
//
// Cache semantics:
//   - Execution errors are NOT cached (a transient backend failure should
//     not pin a stale negative result for the TTL).
//   - Each (Subject, Verb, Resource, Agent/Tool/MCP) tuple has its own cache
//     entry, keyed by the fields that participate in authorization decisions.
//   - Subject.Extra is included in the key — two requests from the same user
//     but different attributes produce different cache entries.
type CachedAuthorizer struct {
	inner Authorizer
	cache *ristretto.Cache[string, cachedEntry]
	ttl   time.Duration
}

// NewCachedAuthorizer creates an authorizer cache wrapper. ttl=0 defaults to
// DefaultCacheTTL (5 minutes).
func NewCachedAuthorizer(inner Authorizer, ttl time.Duration) (*CachedAuthorizer, error) {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	// ristretto config mirrors cmdb.Authorizer: one global cache per
	// CachedAuthorizer instance, each instance wrapping one backend type.
	cache, err := ristretto.NewCache(&ristretto.Config[string, cachedEntry]{
		NumCounters: 1e5, // track ~100k keys for eviction policy
		MaxCost:     1e7, // max ~10M cost units
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("authz: create cache: %w", err)
	}
	return &CachedAuthorizer{inner: inner, cache: cache, ttl: ttl}, nil
}

// Name delegates to the inner Authorizer prefixed with "cached:" so the
// backend identity is preserved in metrics and logs.
func (c *CachedAuthorizer) Name() string { return "cached:" + c.inner.Name() }

// DisplayName reports the wrapped backend's own name. It exists because the
// reason string travels to the person who asked — it is shown on an approval
// card and echoed by /v1/authz/decisions — and "cached:opa" there is an
// implementation detail, while in metrics and logs the marker is the point.
func (c *CachedAuthorizer) DisplayName() string { return c.inner.Name() }

// Authorize checks the cache; on a miss, delegates to the inner Authorizer and
// caches the result (unless there was an execution error).
func (c *CachedAuthorizer) Authorize(ctx context.Context, req AuthorizationRequest) (Decision, string, error) {
	key := cacheKey(req)
	if v, ok := c.cache.Get(key); ok {
		return v.decision, v.reason, nil
	}

	decision, reason, err := c.inner.Authorize(ctx, req)
	if err != nil {
		// Never cache errors — same contract as cmdb.Authorizer and
		// webhook.Authorizer: transient failures must not pin a stale result.
		return decision, reason, err
	}

	c.cache.SetWithTTL(key, cachedEntry{decision: decision, reason: reason}, 1, c.ttl)
	return decision, reason, nil
}

// cachedEntry is the immutable value stored in the ristretto cache.
type cachedEntry struct {
	decision Decision
	reason   string
}

// Wait blocks until all pending cache writes have been processed. Callers
// typically don't need this (ristretto writes complete within microseconds),
// but tests use it for deterministic assertions.
func (c *CachedAuthorizer) Wait() { c.cache.Wait() }

// Flush drops every cached decision.
//
// # Why a policy reload cannot do this by itself
//
// The cache key is the *request*; the value is the *answer*. Writing a rule
// changes the policy, so reloading the policy refreshes nothing that is already
// cached — the stale thing is the answer, not the rule set. Without a flush, a
// rule that turns a cached deny into an allow (or the reverse) does not take
// effect until the entry ages out, up to the TTL.
//
// The rule-write path therefore reloads *and* flushes. The periodic refresh
// deliberately does not: a timer firing every minute is not evidence that the
// policy changed, and clearing the cache on it would defeat the cache.
//
// Clearing everything rather than the affected keys is deliberate. Which cached
// requests a given rule could have decided is a question about the Rego policy —
// answering it here would be a second implementation of that policy, and the two
// would drift in the direction of granting or denying the wrong thing.
func (c *CachedAuthorizer) Flush() { c.cache.Clear() }

// cacheKey builds a deterministic, collision-free string key from every
// AuthorizationRequest field that participates in authorization decisions.
// Subject.Extra is included because policy attributes it carries (e.g. a
// group/namespace) drive the Rego inheritance rules — two requests with the
// same UserID but different attributes must have separate cache entries.
func cacheKey(req AuthorizationRequest) string {
	kind, name := req.EffectiveResource()

	// Pre-allocate for the typical key length (~80–120 bytes).
	var b strings.Builder
	b.Grow(128)

	b.WriteString(string(req.Subject.Kind))
	b.WriteByte('|')
	b.WriteString(req.Subject.PrimaryID())
	b.WriteByte('|')
	writeSortedExtra(&b, req.Subject.Extra)
	b.WriteByte('|')
	b.WriteString(string(req.Verb))
	b.WriteByte('|')
	b.WriteString(string(kind))
	b.WriteByte('|')
	b.WriteString(name)
	b.WriteByte('|')
	// Every selected Agent participates (see EffectiveAgentNames), sorted so two
	// runs that selected the same Agents share an entry regardless of order.
	agentNames := req.EffectiveAgentNames()
	slices.Sort(agentNames)
	b.WriteString(strings.Join(agentNames, ","))
	b.WriteByte('|')
	b.WriteString(req.ToolName)
	b.WriteByte('|')
	b.WriteString(req.MCPName)

	return b.String()
}

// writeSortedExtra serializes Extra into key=value pairs sorted by key, so the
// cache key is deterministic regardless of map iteration order. Example output
// for Extra{"group_namespace":"infra","oncall_group_codes":"sre-oncall"}:
// "group_namespace=infra;oncall_group_codes=sre-oncall;".
func writeSortedExtra(b *strings.Builder, extra map[string]string) {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(extra[k])
		b.WriteByte(';')
	}
}
