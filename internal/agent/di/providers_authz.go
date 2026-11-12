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
package di

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
	"k8s.io/apiserver/pkg/authentication/authenticator"

	k8sauthn "github.com/onexstack/onexai/internal/pkg/authn/k8s"

	authreviewbiz "github.com/onexstack/onexai/internal/agent/biz/v1/authreview"
	authzrulebiz "github.com/onexstack/onexai/internal/agent/biz/v1/authzrule"
	webhookbiz "github.com/onexstack/onexai/internal/agent/biz/v1/webhook"
	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	agentauthn "github.com/onexstack/onexai/internal/agent/pkg/authn"
	"github.com/onexstack/onexai/internal/agent/pkg/authzrule"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/controlplane"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/webhook"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/casdoor"
	"github.com/onexstack/onexai/internal/pkg/authz/cmdb"
	k8sauthz "github.com/onexstack/onexai/internal/pkg/authz/k8s"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
	authzwebhook "github.com/onexstack/onexai/internal/pkg/authz/webhook"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/declclient"
)

// ProvideAuthzRuleSource adapts the control plane to the OPA rule store's narrow
// read interface. A nil client (no control plane configured) yields a nil source,
// which the rule store treats as an empty rule set — the fail-closed state an
// operator has to seed anyway.
func ProvideAuthzRuleSource(client *declclient.Client) opa.RuleSource {
	if client == nil {
		return nil
	}
	return controlplane.NewSource(client)
}

// ProvideAuthzRuleStore builds the process-wide in-memory OPA rule store, or
// nil when OPA is disabled or there is no rule source.
//
// It is a provider of its own rather than a local inside
// ProvideAuthzCoordinator so that the exact same instance is shared with the
// rule write path (ProvideAuthzRules): two RuleStores would drift, and the
// writer's reload would then refresh a set nobody evaluates.
func ProvideAuthzRuleStore(cfg *Config, source opa.RuleSource) *opa.RuleStore {
	opts := cfg.AuthzOptions
	if !opts.Enabled || !opts.OPA.Enabled || source == nil {
		return nil
	}
	return opa.NewRuleStore(source)
}

// ProvideAuthzOPAAuthorizer builds the prepared Rego authorizer over the shared
// rule store, or nil when there is no rule store or the policy fails to
// prepare. It is separate from ProvideAuthzRuleStore because Wire only permits
// error as the last of a multi-return provider, and this keeps each provider a
// single value.
func ProvideAuthzOPAAuthorizer(ctx context.Context, ruleStore *opa.RuleStore) *opa.Authorizer {
	if ruleStore == nil {
		return nil
	}
	opaAuthorizer, err := opa.NewAuthorizer(ctx, ruleStore)
	if err != nil {
		slog.ErrorContext(ctx, "authz: could not build the OPA authorizer; continuing without it", "error", err)
		return nil
	}
	return opaAuthorizer
}

// authzReloadSettle is how long the reload loop waits for a burst of changes to
// go quiet, so an edit that touches several objects is one reload.
//
// authzReloadFloor is the shortest interval between two reloads. Every reload
// re-lists the whole rule set and compares it row by row, and a real change also
// re-prepares the OPA query and drops every cached decision — costs that scale
// with the table rather than with the edit. The source already filters out
// updates that cannot matter; this is the backstop that keeps any future reason
// to notify from becoming a hot loop, which is what it was: the agent service
// spent most of a core re-listing its own rules several times a second.
const (
	authzReloadSettle = 200 * time.Millisecond
	authzReloadFloor  = time.Second
)

// watchAuthzRuleChanges reloads the rule set whenever the control plane says it
// may have changed, collapsing bursts and keeping a floor between reloads.
func watchAuthzRuleChanges(ctx context.Context, changes <-chan struct{}, reload authzrule.Reloader) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
		}

		// One edit, one reload: keep waiting while changes are still arriving.
		settle := time.NewTimer(authzReloadSettle)
	settled:
		for {
			select {
			case <-ctx.Done():
				settle.Stop()
				return
			case _, ok := <-changes:
				if !ok {
					settle.Stop()
					return
				}
			case <-settle.C:
				break settled
			}
		}

		if err := reload(ctx); err != nil {
			slog.WarnContext(ctx, "authz: reloading the rule set failed; keeping the previous one", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(authzReloadFloor):
		}
	}
}

// authzRuleReloader is the one definition of "reload the rules from the
// database": re-read the store, then re-prepare the OPA query from the new
// snapshot.
//
// invalidate, when non-nil, drops decisions computed under the old policy. It
// runs whenever the rule set actually changed — on the write path, and on the
// periodic refresher. Both, because the cache holds *answers*: leaving it alone
// after a DBA/seed edit lets a revoked grant survive for up to
// authz.DefaultCacheTTL, while clearing it on every timer tick that found the
// same rules would defeat the cache for nothing.
func authzRuleReloader(ruleStore *opa.RuleStore, authorizer *opa.Authorizer, invalidate func()) authzrule.Reloader {
	if ruleStore == nil || authorizer == nil {
		return nil
	}
	return func(ctx context.Context) error {
		changed, err := ruleStore.Sync(ctx)
		if err != nil {
			return err
		}
		// Re-preparing the query is only needed when the data changed; it is the
		// expensive half of a reload.
		if changed {
			if err := authorizer.Reload(ctx); err != nil {
				return err
			}
			if invalidate != nil {
				invalidate()
			}
		}
		return nil
	}
}

// authzInvalidator adapts the OPA result cache to the reloader's invalidate
// hook, or returns nil when there is no cache to clear.
func authzInvalidator(cache *authz.CachedAuthorizer) func() {
	if cache == nil {
		return nil
	}
	return cache.Flush
}

// ProvideAuthzOPACache wraps the OPA authorizer in the result cache.
//
// # Why this is a provider rather than a local in ProvideAuthzCoordinator
//
// The other backends' caches are built where they are used, and this one was
// too. It moved because the rule-write path has to be able to clear it: the
// cache holds answers, so reloading the policy does not invalidate them, and
// without the flush a rule change is invisible to any request shape that was
// decided under the old one until the entry ages out (up to
// authz.DefaultCacheTTL). Two instances would leave the write path clearing a
// cache nobody reads — the failure would look exactly like the bug.
//
// A nil return means "no cache": the coordinator then uses the authorizer
// unwrapped, which is the same fallback it applied when the cache failed to
// build.
func ProvideAuthzOPACache(opaAuthorizer *opa.Authorizer) *authz.CachedAuthorizer {
	if opaAuthorizer == nil {
		return nil
	}
	cache, err := authz.NewCachedAuthorizer(opaAuthorizer, authz.DefaultCacheTTL)
	if err != nil {
		slog.Warn("authz: could not build the OPA result cache; using the backend uncached", "error", err)
		return nil
	}
	return cache
}

// ProvideAuthzRules exposes the authorization-rule administration surface (the
// /v1/agent/authorizationrules endpoints) over the same store the OPA rule source reads.
//
// It returns nil when no resource store is configured at all; the handler then
// reports the API as unavailable rather than pretending to persist a rule.
//
// # Why the store comes from resources
//
// The rules used to be written to SQL while the rule source read them from the
// control plane, and a deployment that had finished that migration had no rule
// table to write — so the surface was disabled outright. That refusal was
// honest but not free: OneX provisions a workspace grant and a per-user grant
// through this API before every agent run, so a disabled API meant every run
// on such a deployment failed with 503 AgentNotReady. Taking the accessor from
// the resource store is what keeps the write and the read on one side.
func ProvideAuthzRules(
	cfg *Config, db *gorm.DB, resources store.AIStore, ruleStore *opa.RuleStore,
	opaAuthorizer *opa.Authorizer, opaCache *authz.CachedAuthorizer,
) *authzrule.Service {
	if !resourceStoreWired(cfg, db) || resources == nil {
		return nil
	}
	return authzrule.New(resources.AuthzRules(),
		authzRuleReloader(ruleStore, opaAuthorizer, authzInvalidator(opaCache)))
}

// ProvideWebhooks exposes the run-lifecycle hook administration surface (the
// /v1/agent/webhooks endpoints) over the control-plane store, so a hook written here
// is the same object the dispatcher reads.
//
// The reloader is nil because nothing caches hooks: the dispatcher reads them
// when a run starts, so a hook disabled a second ago is already gone from the
// next run's view. A cache would be the thing to reload, and there is none.
func ProvideWebhooks(resources store.AIStore) *webhook.Service {
	if resources == nil {
		return nil
	}
	return webhook.New(resources.Webhooks(), resources.Secrets(), nil)
}

// ProvideWebhookDispatcher builds the run-lifecycle dispatcher, or nil when no
// resource store is configured: nothing to read means nothing to call.
//
// It reads hooks through the same store the administration API writes, and that
// store is the control plane, so a hook disabled a second ago has stopped being
// called by the next run without any cache to invalidate.
func ProvideWebhookDispatcher(
	cfg *Config, resources store.AIStore,
) *webhook.Dispatcher {
	if resources == nil {
		return nil
	}
	return webhook.NewDispatcher(&webhook.Options{
		Source:  resources.Webhooks(),
		Secrets: secretResolver(resources),
		// The deployment's own hook list, applied to a run whose agents
		// declared none. It is the answer to "what about an agent nobody
		// configured": without it such a run would be metered by nothing,
		// which is the failure this whole path exists to prevent.
		Defaults: cfg.AgentOptions.WebhookDefaults,
		Metrics:  metrics.M,
	})
}

// secretResolver reads a hook's token out of the secret store. It returns false
// for a reference the store does not hold, which the dispatcher turns into a
// refused delivery rather than an unauthenticated one.
func secretResolver(resources store.AIStore) webhook.SecretValue {
	secrets := resources.Secrets()
	return func(ctx context.Context, ref string) (string, bool) {
		s, err := secrets.Get(ctx, where.F("secret_id", ref))
		if err != nil || s == nil || s.SecretKey == "" {
			return "", false
		}
		return s.SecretKey, true
	}
}

// ProvideAgentCall builds the per-Agent authentication middleware dependency.
//
// It is constructed whenever a resource store exists, not only when a Webhook
// happens to exist today: an Agent gains authentication Webhooks by an ordinary
// write, and the middleware reads the rows per request so a change is visible
// after the control plane's watch delivers it.
func ProvideAgentCall(resources store.AIStore, cfg *Config) *agentauthn.AgentCall {
	if resources == nil {
		return nil
	}
	defaultAgent := ""
	if cfg != nil && cfg.AgentOptions != nil {
		defaultAgent = cfg.AgentOptions.A2ADefaultAgent
	}
	return agentauthn.NewAgentCall(agentauthn.AgentCallOptions{
		LoadAgent: func(ctx context.Context, ref string) (*model.AgentM, error) {
			return resources.Agents().Get(ctx, model.EntryFilter(ref))
		},
		ListWebhooks: func(ctx context.Context) ([]*model.WebhookM, error) {
			_, rows, err := resources.Webhooks().List(ctx, where.L(-1))
			return rows, err
		},
		ResolveSecret: func(ctx context.Context, ref string) (string, bool) {
			return secretResolver(resources)(ctx, ref)
		},
		DefaultAgent: defaultAgent,
	})
}

// ProvideAgentWebhookAuthz resolves an Agent's own authorization Webhooks.
//
// It is independent of authz.enabled: an Agent that names SubjectAccessReview
// hooks is governed by them even on a deployment that turned the platform's
// authorizers off.
func ProvideAgentWebhookAuthz(resources store.AIStore) *agentauthz.Resolver {
	if resources == nil {
		return nil
	}
	return agentauthz.New(agentauthz.Options{
		LoadAgent: func(ctx context.Context, ref string) (*model.AgentM, error) {
			return resources.Agents().Get(ctx, model.EntryFilter(ref))
		},
		ListWebhooks: func(ctx context.Context) ([]*model.WebhookM, error) {
			_, rows, err := resources.Webhooks().List(ctx, where.L(-1))
			return rows, err
		},
		ResolveSecret: func(ctx context.Context, ref string) (string, bool) {
			return secretResolver(resources)(ctx, ref)
		},
	})
}

// ProvideAuthzCoordinator builds the process-wide authorization entry point:
// the enabled backends, the per-agent backend registry, the OPA rule store and
// its periodic refresh.
//
// It returns nil when authorization is disabled. Every consumer treats a nil
// coordinator as "allow everything", which is what keeps a disabled deployment
// from having to special-case authz anywhere.
//
// When authorization is enabled but no agent/tool-level backend could be built,
// it returns an error instead of an unrestricted coordinator: an empty Union is
// fail-closed at the package level, so starting anyway would turn a missing
// rule source into a service that 403s every request with an opaque reason.
// ProvideTokenAuthenticator builds the Kubernetes OIDC authenticator described
// by auth.authentication-config. A zero path returns nil, which the middleware
// treats as "no OIDC path configured" rather than "reject everything".
func ProvideTokenAuthenticator(ctx context.Context, cfg *Config) (authenticator.Request, error) {
	if cfg == nil || cfg.AuthenticationConfig == "" {
		return nil, nil
	}
	authnCfg, err := k8sauthn.LoadAuthenticationConfiguration(cfg.AuthenticationConfig)
	if err != nil {
		return nil, err
	}
	return k8sauthn.NewTokenAuthenticator(ctx, authnCfg, "oai-agent-apiserver")
}

// ProvideAuthReview builds the Kubernetes TokenReview/SubjectAccessReview
// business logic.
//
// It is always constructed: the review endpoints are how an external control
// plane asks this service the same questions the run path already answers, and
// a nil coordinator simply means authorization is disabled (the Coordinator's
// nil-receiver handling reports that as Allow, matching every other caller).
func ProvideAuthReview(
	cfg *Config, resources store.AIStore, coordinator *authz.Coordinator,
	tokenAuthenticator authenticator.Request,
) authreviewbiz.Biz {
	var authorizationConfig string
	if cfg.AuthzOptions != nil {
		authorizationConfig = cfg.AuthzOptions.AuthorizationConfig
	}
	return authreviewbiz.New(authreviewbiz.Config{
		JWTSecret:              cfg.JWTSecret,
		WebhookTokenConfigured: cfg.WebhookToken != "",
		TokenAuthenticator:     tokenAuthenticator,
		AuthenticationConfig:   cfg.AuthenticationConfig,
		AuthorizationConfig:    authorizationConfig,
	}, resources, coordinator)
}

func ProvideAuthzCoordinator(
	ctx context.Context, cfg *Config, resources store.AIStore, source opa.RuleSource,
	ruleStore *opa.RuleStore, opaAuthorizer *opa.Authorizer, opaCache *authz.CachedAuthorizer,
) (*authz.Coordinator, error) {
	opts := cfg.AuthzOptions
	if !opts.Enabled {
		return nil, nil
	}

	// Metrics are registered once, at the same moment the coordinator that uses
	// them is built.
	authz.M = authz.NewMetrics(prometheus.DefaultRegisterer)

	// Each backend is wrapped in the same TTL cache, so a policy evaluation that
	// costs a network round-trip (CMDB, webhook, Casdoor) is paid once per
	// distinct request shape rather than once per tool call.
	cached := func(inner authz.Authorizer) authz.Authorizer {
		if inner == nil {
			return nil
		}
		c, err := authz.NewCachedAuthorizer(inner, authz.DefaultCacheTTL)
		if err != nil {
			slog.WarnContext(ctx, "authz: could not build the result cache; using the backend uncached",
				"backend", inner.Name(), "error", err)
			return inner
		}
		return c
	}

	registry := authz.NewRegistry(nil)
	var agentToolAuthz, resourceAuthz []authz.Authorizer

	if opts.OPA.Enabled && source != nil && opaAuthorizer != nil {
		// The same instance the rule-write path clears (see
		// ProvideAuthzOPACache), so a change made by a DBA or the seed SQL drops
		// the cached answers within one refresh interval, not one TTL.
		reload := authzRuleReloader(ruleStore, opaAuthorizer, authzInvalidator(opaCache))

		// The first load is synchronous so the first request is evaluated
		// against the real rule set; afterwards the control plane's watch drives
		// every reload. A failed reload keeps the previous snapshot, and the
		// fail-closed default turns "no rules" into "ask a human".
		if err := reload(ctx); err != nil {
			slog.WarnContext(ctx, "authz: initial rule load failed; will retry on the next change", "error", err)
		}
		if watcher, ok := source.(interface{ Changes() <-chan struct{} }); ok {
			go watchAuthzRuleChanges(ctx, watcher.Changes(), reload)
		}

		// The cache comes from ProvideAuthzOPACache rather than from the local
		// helper, so the rule-write path clears the same instance it reads. A
		// nil cache means it could not be built, and the authorizer is then
		// registered unwrapped — the same fallback the local helper made.
		var opaBackend authz.Authorizer = opaAuthorizer
		if opaCache != nil {
			opaBackend = opaCache
		}
		agentToolAuthz = append(agentToolAuthz, opaBackend)
		resourceAuthz = append(resourceAuthz, opaBackend)
		registry.Register("opa", opaBackend)
	} else if opts.OPA.Enabled && source == nil {
		slog.WarnContext(ctx, "authz: opa is enabled but there is no rule source "+
			"(controlplane.kubeconfig is empty or the control plane is unreachable); "+
			"every request will fall through to the other backends")
	}

	if opts.CMDB.Enabled {
		// The CMDB client is a placeholder that reports "not implemented", which
		// the union reads as NoOpinion. It is registered only for completeness —
		// the default config keeps it off.
		cmdbAuthorizer := cmdb.NewAuthorizer(cmdb.NotImplementedChecker{})
		cachedCMDB := cached(cmdbAuthorizer)
		resourceAuthz = append(resourceAuthz, cachedCMDB)
		registry.Register("cmdb", cachedCMDB)
	}

	authorizationConfigSet := strings.TrimSpace(opts.AuthorizationConfig) != ""
	if authorizationConfigSet {
		// The Kubernetes AuthorizationConfiguration is the primary webhook
		// surface. Its authorizers are combined with kube-apiserver's ordered
		// semantics inside the adapter; the platform union around it keeps its
		// existing deny-wins algebra with OPA/CMDB/Casdoor.
		authorizationConfig, err := k8sauthz.LoadAuthorizationConfiguration(opts.AuthorizationConfig)
		if err != nil {
			return nil, err
		}
		chain, err := k8sauthz.NewAuthorizationChain(authorizationConfig)
		if err != nil {
			return nil, err
		}
		if chain != nil {
			adapter := cached(k8sauthz.Adapter{Authorizer: chain})
			agentToolAuthz = append(agentToolAuthz, adapter)
			resourceAuthz = append(resourceAuthz, adapter)
			registry.Register("authorization-config", adapter)
		}
	}

	for _, ep := range opts.Webhook.Endpoints {
		if authorizationConfigSet || !opts.Webhook.Enabled {
			break
		}
		w := authzwebhook.NewAuthorizer(ep.Name, ep.URL, ep.Timeout, authzwebhook.FailureMode(ep.FailureMode))
		if ep.SecretRef != "" {
			// A configured signature that cannot be produced must not become an
			// unsigned request: the receiver would have to trust the URL alone,
			// which is the state the option exists to end.
			resolve := secretResolver(resources)
			if resolve == nil {
				return nil, fmt.Errorf(
					"authz.webhook.endpoints[%s].secret-ref %q is configured but this deployment has no secret store",
					ep.Name, ep.SecretRef)
			}
			secret, ok := resolve(ctx, ep.SecretRef)
			if !ok {
				return nil, fmt.Errorf("authz.webhook.endpoints[%s].secret-ref %q could not be resolved", ep.Name, ep.SecretRef)
			}
			w = w.WithSecret(secret)
		}
		cachedWebhook := cached(w)
		agentToolAuthz = append(agentToolAuthz, cachedWebhook)
		resourceAuthz = append(resourceAuthz, cachedWebhook)
		registry.Register("webhook:"+ep.Name, cachedWebhook)
	}

	if opts.Casdoor.Enabled {
		c := casdoor.NewAuthorizer(casdoor.Config{
			Endpoint:         opts.Casdoor.Endpoint,
			ClientID:         opts.Casdoor.ClientID,
			ClientSecret:     opts.Casdoor.ClientSecret,
			Certificate:      opts.Casdoor.Certificate,
			OrganizationName: opts.Casdoor.OrganizationName,
			ApplicationName:  opts.Casdoor.ApplicationName,
			Organization:     opts.Casdoor.Organization,
			PermissionID:     opts.Casdoor.PermissionID,
			ModelID:          opts.Casdoor.ModelID,
			ResourceID:       opts.Casdoor.ResourceID,
			EnforcerID:       opts.Casdoor.EnforcerID,
			Owner:            opts.Casdoor.Owner,
			Timeout:          opts.Casdoor.Timeout,
		})
		cachedCasdoor := cached(c)
		agentToolAuthz = append(agentToolAuthz, cachedCasdoor)
		resourceAuthz = append(resourceAuthz, cachedCasdoor)
		registry.Register("casdoor", cachedCasdoor)
	}

	// Fail closed at startup rather than at the first request. An empty
	// agent/tool-level Union denies everything, and the caller would see a 403
	// that names no cause; the actual cause (no rule source, OPA off, every
	// backend disabled) is only knowable here.
	if len(agentToolAuthz) == 0 {
		return nil, fmt.Errorf(
			"authz: enabled but no agent/tool-level authorizer was built "+
				"(opa.enabled=%t, rule source present=%t, cmdb.enabled=%t, webhook.enabled=%t, authorization-config set=%t, casdoor.enabled=%t); "+
				"every request would be denied",
			opts.OPA.Enabled, source != nil, opts.CMDB.Enabled, opts.Webhook.Enabled, authorizationConfigSet, opts.Casdoor.Enabled)
	}
	if len(resourceAuthz) == 0 {
		// A legitimate posture (strictest), so this is a warning: only tools
		// that extract an addressable resource are affected, and they deny.
		slog.WarnContext(ctx, "authz: no resource-level authorizer was built; "+
			"every tool call whose arguments extract a resource will be denied")
	}

	agentToolUnion := authz.NewUnionAuthorizer(agentToolAuthz...)
	registry.SetDefault(agentToolUnion)

	coordinator := authz.NewCoordinator(
		agentToolUnion,
		authz.NewUnionAuthorizer(resourceAuthz...),
		opts.Enabled,
	).WithAgentBackendSelection(registry, agentBackendLookup(ctx, resources)).
		WithNamespaceAuthz(namespaceLookup(ctx, resources))

	slog.InfoContext(ctx, "authorization configured",
		"enabled", coordinator.Enabled(),
		"opa", opts.OPA.Enabled,
		"cmdb", opts.CMDB.Enabled,
		"webhook", opts.Webhook.Enabled,
		"authorization_config", authorizationConfigSet,
		"casdoor", opts.Casdoor.Enabled,
		"rule_source", source != nil,
	)
	return coordinator, nil
}

// agentBackendLookup resolves an agent name to its parsed authz_backends list,
// for the coordinator's per-agent backend selection. A missing agent, an empty
// column or malformed JSON all mean "no override", i.e. the process-wide union.
func agentBackendLookup(ctx context.Context, resources store.AIStore) func(string) ([]string, bool) {
	return func(agentName string) ([]string, bool) {
		if agentName == "" {
			return nil, false
		}
		// agentName may be "namespace/name": the store lookup uses the full
		// reference so a same-named agent in another namespace is not picked.
		m, err := resources.Agents().Get(ctx, model.EntryFilter(agentName))
		if err != nil || m == nil || m.AuthzBackends == "" {
			return nil, false
		}
		var names []string
		if err := json.Unmarshal([]byte(m.AuthzBackends), &names); err != nil {
			slog.WarnContext(ctx, "authz: agent authz_backends is not a JSON array; ignoring",
				"agent", agentName, "error", err)
			return nil, false
		}
		return names, len(names) > 0
	}
}

// namespaceLookup resolves a (kind, name) AI resource to the namespace it
// belongs to, for the coordinator's namespace-level checks. Every resource
// model carries a namespace column; an empty one normalizes to
// model.DefaultNamespace, so a resource that predates the column still yields a
// check under the default namespace rather than silently skipping the gate.
//
// An unknown resource returns (_, false), which skips the namespace check for
// it — the same "no opinion" outcome as a missing agent.
func namespaceLookup(ctx context.Context, resources store.AIStore) func(authz.ResourceKind, string) (string, bool) {
	return func(kind authz.ResourceKind, name string) (string, bool) {
		if name == "" {
			return "", false
		}
		switch kind {
		case authz.ResourceKindAgent:
			if m, err := resources.Agents().Get(ctx, model.EntryFilter(name)); err == nil && m != nil {
				return model.NormalizeNamespace(m.Namespace), true
			}
		case authz.ResourceKindSkill:
			if m, err := resources.Skills().Get(ctx, model.EntryFilter(name)); err == nil && m != nil {
				return model.NormalizeNamespace(m.Namespace), true
			}
		case authz.ResourceKindMCP:
			if m, err := resources.MCPs().Get(ctx, model.EntryFilter(name)); err == nil && m != nil {
				return model.NormalizeNamespace(m.Namespace), true
			}
		case authz.ResourceKindKnowledgeBase:
			if m, err := resources.KnowledgeBases().Get(ctx, model.EntryFilter(name)); err == nil && m != nil {
				return model.NormalizeNamespace(m.Namespace), true
			}
		}
		return "", false
	}
}

// resourceMappingsFor returns a lookup from the MCP server names a run requests
// to their raw resource_mappings JSON, resolved against the resource store.
//
// # Keys are the request's spelling
//
// The run's MCP list carries whichever spelling its caller used: the agent path
// passes canonical "namespace/name" entries, while a composition may still pass
// a bare name. The returned map is looked up by the MCPName that ToolOrigins
// reports, and that is the request's own spelling too (see
// mcpclient.Manager.ToolOrigins), so the key must be the request string — not
// the stored row name and not a re-derived canonical form.
//
// # Resolution rules
//
//   - A qualified request ("ns/name") resolves only against the row with that
//     exact namespace and name. It never falls back to a same-named row in
//     another namespace: applying another tenant's resource mappings would be
//     the cross-namespace mix-up the canonical form exists to prevent.
//   - A bare request resolves by name only when that name is unique across
//     namespaces. An ambiguous name is omitted rather than guessed, matching
//     mcpclient.Manager's own bare-name rule.
//
// A row with no resource_mappings is omitted either way.
func resourceMappingsFor(ctx context.Context, resources store.AIStore) func([]string) map[string]string {
	return func(mcps []string) map[string]string {
		if len(mcps) == 0 {
			return nil
		}
		_, rows, err := resources.MCPs().List(ctx, where.L(-1))
		if err != nil {
			return nil
		}

		byKey := make(map[string]*model.MCPM, len(rows))
		byName := make(map[string]*model.MCPM, len(rows))
		ambiguous := make(map[string]bool, len(rows))
		for _, m := range rows {
			if m == nil {
				continue
			}
			byKey[model.CanonicalRef(m.Namespace, m.Name)] = m
			if _, ok := byName[m.Name]; ok {
				ambiguous[m.Name] = true
				continue
			}
			byName[m.Name] = m
		}

		out := make(map[string]string, len(mcps))
		for _, name := range mcps {
			if name == "" {
				continue
			}
			ref := model.SplitRef(name)
			var m *model.MCPM
			if ref.Namespace != "" {
				m = byKey[model.CanonicalRef(ref.Namespace, ref.Name)]
			} else if !ambiguous[ref.Name] {
				m = byName[ref.Name]
			}
			if m != nil && m.ResourceMappings != "" {
				out[name] = m.ResourceMappings
			}
		}
		return out
	}
}

// ProvideDecisionCallers is the optional allowlist for /v1/authz/decisions.
// Nil (empty configuration) keeps the endpoint open to every signed caller.
func ProvideDecisionCallers(cfg *Config) map[string]struct{} {
	if cfg.AuthzOptions == nil || len(cfg.AuthzOptions.DecisionCallers) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(cfg.AuthzOptions.DecisionCallers))
	for _, name := range cfg.AuthzOptions.DecisionCallers {
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// ProvideResourceGuard translates the option block into the handler's guard.
// A deployment with no authz options gets the zero guard, which is off.
func ProvideResourceGuard(cfg *Config) handler.ResourceGuard {
	if cfg == nil || cfg.AuthzOptions == nil {
		return handler.ResourceGuard{}
	}
	guard := cfg.AuthzOptions.ResourceGuard
	mode := authzview.Mode(guard.WriteMode)
	if mode == "" {
		mode = authzview.ModeOff
	}
	kinds := make(map[string]struct{}, len(guard.ReadFilterKinds))
	for _, kind := range guard.ReadFilterKinds {
		if kind != "" {
			kinds[kind] = struct{}{}
		}
	}
	return handler.ResourceGuard{
		WriteMode:         mode,
		ReadFilterEnabled: guard.ReadFilterEnabled,
		ReadFilterKinds:   kinds,
	}
}

// ProvideAuthzRulesAdmin adapts the rule service to the handler's port. A nil
// service becomes a nil interface: a typed nil stored in the interface would
// compare != nil, and the handler would advertise an API it cannot serve.
func ProvideAuthzRulesAdmin(rules *authzrule.Service) authzrulebiz.AuthzRuleStore {
	if rules == nil {
		return nil
	}
	return rules
}

// ProvideWebhooksAdmin adapts the hook service to the handler's port, with the
// same nil rule as the rule service.
func ProvideWebhooksAdmin(webhooks *webhook.Service) webhookbiz.WebhookStore {
	if webhooks == nil {
		return nil
	}
	return webhooks
}
