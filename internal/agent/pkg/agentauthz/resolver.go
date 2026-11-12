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

// Package agentauthz resolves an Agent's authorization Webhooks into the
// platform's Authorizer interface.
//
// It is the authorization half of the per-Agent override: when an Agent names
// authz_webhooks, those SubjectAccessReview endpoints are the only authority for
// that Agent, and the platform's OPA/CMDB/Casdoor/webhook union is not
// consulted. When the list is empty the resolver says "no opinion" and the
// existing platform path runs unchanged.
package agentauthz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
	authzwebhook "github.com/onexstack/onexai/internal/pkg/authz/webhook"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
)

const (
	// cacheTTL bounds how long a built authorizer is reused. It is the window
	// between an operator editing a Webhook and the next run seeing it; the
	// control plane's watch is faster, but rebuilding a TLS client per decision
	// is not free.
	cacheTTL = 30 * time.Second
	// defaultTimeout matches the Webhook resource's default.
	defaultTimeout = 30 * time.Second
)

// SecretValueFunc resolves a Webhook's secretRef.
type SecretValueFunc func(ctx context.Context, ref string) (string, bool)

// Options are the resolver's dependencies.
type Options struct {
	// LoadAgent resolves an agent reference to its row.
	LoadAgent func(ctx context.Context, ref string) (*model.AgentM, error)
	// ListWebhooks returns every Webhook row.
	ListWebhooks func(ctx context.Context) ([]*model.WebhookM, error)
	// ResolveSecret resolves a Webhook's secretRef.
	ResolveSecret SecretValueFunc
}

// Resolver builds and caches per-Agent authorizers.
type Resolver struct {
	opts  Options
	cache sync.Map // agent ref -> cachedAuthorizer
}

type cachedAuthorizer struct {
	authorizer authz.Authorizer
	found      bool
	expires    time.Time
}

// New builds a resolver. It returns nil when the lookups are not wired, which
// every caller treats as "no Agent-level authorization".
func New(opts Options) *Resolver {
	if opts.LoadAgent == nil || opts.ListWebhooks == nil {
		return nil
	}
	return &Resolver{opts: opts}
}

// AuthorizerFor returns the Union authorizer an Agent's authorization Webhooks
// define.
//
// found=false means the Agent named no authorization Webhooks, and the caller
// must run the platform path. Any error means the Agent named Webhooks that
// cannot be resolved (missing, disabled, wrong type, unreadable secret); the
// caller must fail closed rather than fall back to the platform, because falling
// back would let a deleted Webhook silently widen access.
func (r *Resolver) AuthorizerFor(ctx context.Context, agentRef string) (authz.Authorizer, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	if cached, ok := r.cache.Load(agentRef); ok {
		entry := cached.(cachedAuthorizer)
		if time.Now().Before(entry.expires) {
			return entry.authorizer, entry.found, nil
		}
		r.cache.Delete(agentRef)
	}

	agent, err := r.opts.LoadAgent(ctx, agentRef)
	if err != nil {
		return nil, false, err
	}
	if agent == nil || len(agent.AuthzWebhooks) == 0 {
		r.store(agentRef, nil, false)
		return nil, false, nil
	}

	rows, err := r.opts.ListWebhooks(ctx)
	if err != nil {
		return nil, true, err
	}
	byName := make(map[string]*model.WebhookM, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	authorizers := make([]authz.Authorizer, 0, len(agent.AuthzWebhooks))
	for _, name := range agent.AuthzWebhooks {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		hook, ok := byName[name]
		if !ok {
			return nil, true, fmt.Errorf("agent %q names authorization webhook %q, which does not exist", agent.Name, name)
		}
		if !hook.Enabled() || hook.EffectiveType() != model.WebhookTypeAuthorization {
			return nil, true, fmt.Errorf("agent %q names authorization webhook %q, which is not an enabled authorization hook", agent.Name, name)
		}
		authorizer, err := buildAuthorizer(ctx, hook, r.opts.ResolveSecret)
		if err != nil {
			return nil, true, err
		}
		authorizers = append(authorizers, authorizer)
	}
	if len(authorizers) == 0 {
		return nil, true, fmt.Errorf("agent %q names authorization webhooks but none is usable", agent.Name)
	}

	union := authz.NewUnionAuthorizer(authorizers...)
	r.store(agentRef, union, true)
	return union, true, nil
}

func (r *Resolver) store(agentRef string, authorizer authz.Authorizer, found bool) {
	r.cache.Store(agentRef, cachedAuthorizer{
		authorizer: authorizer,
		found:      found,
		expires:    time.Now().Add(cacheTTL),
	})
}

// buildAuthorizer turns one Webhook row into the platform Authorizer.
func buildAuthorizer(ctx context.Context, hook *model.WebhookM, secrets SecretValueFunc) (authz.Authorizer, error) {
	endpoint := hook.URL
	var httpClient *http.Client
	if hook.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(hook.ClientConfigJSON), cfg); err != nil {
			return nil, fmt.Errorf("authorization webhook %q has an unreadable clientConfig: %w", hook.Name, err)
		}
		if hook.TimeoutSeconds > 0 {
			cfg.TimeoutSeconds = int32(hook.TimeoutSeconds)
		}
		resolved, err := clientconfig.Resolve(*cfg)
		if err != nil {
			return nil, fmt.Errorf("authorization webhook %q: %w", hook.Name, err)
		}
		client, err := clientconfig.HTTPClient(*cfg)
		if err != nil {
			return nil, fmt.Errorf("authorization webhook %q: %w", hook.Name, err)
		}
		endpoint = resolved.URL
		httpClient = client
	}
	if endpoint == "" {
		return nil, fmt.Errorf("authorization webhook %q has no endpoint", hook.Name)
	}

	timeout := time.Duration(hook.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	mode := authzwebhook.FailureModeNoOpinion
	if hook.FailureMode == model.FailureModeFail {
		mode = authzwebhook.FailureModeDeny
	}

	authorizer := authzwebhook.NewAuthorizer(hook.Name, endpoint, timeout, mode)
	if httpClient != nil {
		authorizer.WithHTTPClient(httpClient)
	}
	if len(hook.Headers) > 0 {
		authorizer.WithHeaders(hook.Headers)
	}
	if hook.SecretRef != "" {
		if secrets == nil {
			return nil, fmt.Errorf("authorization webhook %q names a secret but the deployment has no secret store", hook.Name)
		}
		secret, ok := secrets(ctx, hook.SecretRef)
		if !ok {
			return nil, fmt.Errorf("authorization webhook %q names a secret that cannot be resolved", hook.Name)
		}
		authorizer.WithBearer(secret)
	}
	return authorizer, nil
}
