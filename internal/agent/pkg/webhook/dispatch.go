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

package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/webhookclient"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// Auto-disable thresholds. A hook whose endpoint keeps failing stops being
// called, because the alternative is every run paying its timeout while an
// operator is not looking. The two numbers are deliberately different: the
// first is what a short outage looks like (a restart, a deploy), and the second
// is a hook that is simply broken.
const (
	// disableAfterFailures parks a hook for a cooldown.
	disableAfterFailures = 4
	// disableForGoodAfterFailures stops calling it until the process restarts.
	// Only a deliberate operator action fixes the configuration, so retrying
	// forever would only spend the timeout again.
	disableForGoodAfterFailures = 40
	// disableCooldown is how long a parked hook is skipped before being tried
	// again.
	disableCooldown = time.Minute
)

// Source resolves hook names to rows. It is the webhook table store's read
// surface and nothing else.
type Source interface {
	List(ctx context.Context, opts *where.Options) (int64, []*model.WebhookM, error)
	Get(ctx context.Context, opts *where.Options) (*model.WebhookM, error)
}

// SecretValue resolves a hook's secretRef to the token to send. A false second
// return means the secret is not there, which is a configuration fault: the
// delivery is refused rather than sent unauthenticated, because an endpoint
// that checks its token would reject it anyway and one that does not would be
// accepting an unauthenticated caller this deployment did not intend.
type SecretValue func(ctx context.Context, ref string) (string, bool)

// Decision is a hook's verdict plus which hook gave it, so the caller can log
// the source of a refusal without the receiver having to name itself.
type Decision struct {
	webhookclient.Verdict
	// Hook is the hook that answered. Empty when no hook was asked.
	Hook string
}

// Invocation is everything a hook is told about the run before or after it.
type Invocation struct {
	// HookNames are the hooks the run's agents declared, unioned. An empty list
	// means no agent declared any, and the deployment's default hooks apply; a
	// list containing "none" means the agents opted out of the default.
	HookNames []string

	// Agent is the primary agent and Agents is the full selection.
	Agent  string
	Agents []string

	// Surface is the calling surface (lark, onex, api, a2a). It is decided by
	// the server from the caller's credential, never taken from the request.
	Surface string

	// Subject is who the run is for.
	Subject webhookclient.Subject

	SessionKey string
	RunID      string
}

// InvocationResult is the Invocation plus what the run did.
type InvocationResult struct {
	Invocation
	// Status is the run's terminal state, spelled the way the audit row spells
	// it: "success" | "error" | "stopped". One vocabulary rather than two, so a
	// receiver comparing this against an audit row is not translating.
	Status string
	// Usage is what the run spent. It is read from the run's own tracker, never
	// from the caller: a receiver that could set this could set its own bill.
	Usage webhookclient.Usage
}

// Dispatcher calls the hooks a run declares. It is safe for concurrent use.
type Dispatcher struct {
	source   Source
	secrets  SecretValue
	client   *webhookclient.Client
	defaults []string
	instance string
	m        *metrics.Metrics
	// cfgClients caches one webhook client per ClientConfig JSON. The TLS
	// configuration and service resolution are properties of the endpoint, so
	// rebuilding them per delivery would pay the certificate pool cost on every
	// run.
	cfgClients sync.Map
	now        func() time.Time

	mu       sync.Mutex
	failures map[string]int
	parked   map[string]time.Time // temporarily disabled until this instant
	stopped  map[string]bool      // disabled until the process restarts
}

// Options configures a Dispatcher.
type Options struct {
	Source  Source
	Secrets SecretValue
	// Client delivers the events. Nil builds one.
	Client *webhookclient.Client
	// Instance names this deployment in the delivery headers.
	Instance string
	// Defaults are the hook names applied to a run whose agents declared none.
	// They exist so an agent nobody configured is still checked: without them, a
	// row that was created outside the provisioning path would run unmetered.
	Defaults []string
	// Metrics is optional.
	Metrics *metrics.Metrics
	// Now is injectable for tests.
	Now func() time.Time
}

// NewDispatcher builds a Dispatcher. A nil Options is valid and dispatches nothing.
func NewDispatcher(opts *Options) *Dispatcher {
	if opts == nil {
		opts = &Options{}
	}
	client := opts.Client
	if client == nil {
		client = webhookclient.New(nil)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{
		source:   opts.Source,
		secrets:  opts.Secrets,
		client:   client,
		defaults: opts.Defaults,
		instance: opts.Instance,
		m:        opts.Metrics,
		now:      now,
		failures: map[string]int{},
		parked:   map[string]time.Time{},
		stopped:  map[string]bool{},
	}
}

// Enabled reports whether any hook could be called at all: with no source and
// no default there is nothing to resolve, and the caller can skip the work.
func (d *Dispatcher) Enabled() bool {
	if d == nil {
		return false
	}
	return d.source != nil || len(d.defaults) > 0
}

// BeforeAgent delivers onStart and reports the verdict.
//
// It returns an error only when the hook set itself could not be determined —
// a database that is not answering. A hook that refused, or that could not be
// reached under a failure mode of "fail", is an answer, not an error, and the
// caller distinguishes them by the verdict.
func (d *Dispatcher) BeforeAgent(ctx context.Context, in Invocation) (Decision, error) {
	if !d.Enabled() {
		return Decision{Verdict: allow()}, nil
	}
	hooks, err := d.hooksFor(ctx, in.HookNames, model.EventOnStart)
	if err != nil {
		return Decision{Verdict: allow()}, err
	}
	for _, h := range hooks {
		verdict, called := d.deliver(ctx, h, model.EventOnStart, d.payload(model.EventOnStart, in, nil))
		if !called {
			continue
		}
		if !verdict.Allowed {
			return Decision{Verdict: verdict, Hook: h.Name}, nil
		}
	}
	return Decision{Verdict: allow()}, nil
}

// AfterAgent delivers onEnd. It reports nothing: the run has already answered,
// so there is no decision left to make and nothing a receiver says could change
// it. Failures are logged and counted like any other delivery.
func (d *Dispatcher) AfterAgent(ctx context.Context, in InvocationResult) {
	if !d.Enabled() {
		return
	}
	hooks, err := d.hooksFor(ctx, in.HookNames, model.EventOnEnd)
	if err != nil {
		slog.WarnContext(ctx, "webhook: could not resolve the hooks to notify", "error", err)
		return
	}
	payload := d.payload(model.EventOnEnd, in.Invocation, &in.Usage)
	payload.ObjectKind = "agentInvocationResult"
	for _, h := range hooks {
		d.deliver(ctx, h, model.EventOnEnd, payload)
	}
}

// clientAndURL resolves the hook's endpoint and the client that reaches it.
//
// A hook written before ClientConfig existed has only URL; it keeps the shared
// client. A hook with a ClientConfig gets a client built from the resolved
// Service/CA/TLS inputs, cached by the serialized configuration.
func (d *Dispatcher) clientAndURL(h *model.WebhookM) (*webhookclient.Client, string, error) {
	if h.ClientConfigJSON == "" {
		return d.client, h.URL, nil
	}

	cfg := &agentv1alpha1.ClientConfig{}
	if err := json.Unmarshal([]byte(h.ClientConfigJSON), cfg); err != nil {
		return nil, "", err
	}
	resolved, err := clientconfig.Resolve(*cfg)
	if err != nil {
		return nil, "", err
	}
	if cached, ok := d.cfgClients.Load(h.ClientConfigJSON); ok {
		return cached.(*webhookclient.Client), resolved.URL, nil
	}
	httpClient, err := clientconfig.HTTPClient(*cfg)
	if err != nil {
		return nil, "", err
	}
	client := webhookclient.New(&webhookclient.Options{Transport: httpClient.Transport})
	d.cfgClients.Store(h.ClientConfigJSON, client)
	return client, resolved.URL, nil
}

// allow is the verdict that lets a run proceed.
func allow() webhookclient.Verdict { return webhookclient.Verdict{Allowed: true} }

// deliver makes one call and records its outcome. The bool reports whether the
// call was made at all: a hook that has been parked or stopped is skipped, and
// a skipped hook is not an answer anyone should act on.
func (d *Dispatcher) deliver(
	ctx context.Context, h *model.WebhookM, event string, payload webhookclient.Payload,
) (webhookclient.Verdict, bool) {
	if until, skip := d.shouldSkip(h.Name, event); skip {
		_ = until
		return allow(), false
	}

	body, err := renderBody(h, payload)
	if err != nil {
		d.recordFailure(h.Name)
		return d.onFailure(h), true
	}
	token := ""
	if h.SecretRef != "" {
		v, ok := d.secret(ctx, h.SecretRef)
		if !ok {
			d.recordFailure(h.Name)
			slog.ErrorContext(ctx, "webhook: the hook's secret is missing; refusing the delivery",
				"hook", h.Name, "secretRef", h.SecretRef)
			return d.onFailure(h), true
		}
		token = v
	}

	client, endpoint, err := d.clientAndURL(h)
	if err != nil {
		d.recordFailure(h.Name)
		slog.WarnContext(ctx, "webhook: endpoint could not be resolved",
			"hook", h.Name, "error", err)
		return d.onFailure(h), true
	}

	started := d.now()
	resp := client.Deliver(ctx, webhookclient.Request{
		HookName: h.Name,
		Event:    event,
		URL:      endpoint,
		Token:    token,
		Headers:  h.Headers,
		Timeout:  time.Duration(h.TimeoutSeconds) * time.Second,
		Instance: d.instance,
		Body:     body,
	})
	elapsed := d.now().Sub(started)

	if resp.Err != nil {
		d.recordFailure(h.Name)
		d.observe(event, h.Name, "failed", elapsed)
		slog.WarnContext(ctx, "webhook: delivery failed", "hook", h.Name, "event", event, "error", resp.Err)
		return d.onFailure(h), true
	}

	d.recordSuccess(h.Name)
	if resp.Verdict.Allowed {
		d.observe(event, h.Name, "allowed", elapsed)
		return resp.Verdict, true
	}
	d.observe(event, h.Name, "refused", elapsed)
	return resp.Verdict, true
}

// onFailure is what an unreachable endpoint means, per the hook's failure mode.
// The default is to let the run through: a webhook that is down must not become
// an outage of the thing it observes, which is the same judgement the platform
// makes about its own quota counters.
func (d *Dispatcher) onFailure(h *model.WebhookM) webhookclient.Verdict {
	if h.FailureMode == model.FailureModeFail {
		return webhookclient.Verdict{
			Allowed: false,
			Code:    "webhookUnavailable",
			Message: "无法确认本次请求是否被允许，请稍后再试",
		}
	}
	return allow()
}

// payload builds the default body for one event. The usage block is only set
// for onEnd: a run that has not happened has not spent anything, and a receiver
// reading zeros could not tell that from a run that cost nothing.
func (d *Dispatcher) payload(event string, in Invocation, usage *webhookclient.Usage) webhookclient.Payload {
	return webhookclient.Payload{
		ObjectKind: "agentInvocation",
		Event:      event,
		Agent:      in.Agent,
		Agents:     in.Agents,
		Surface:    in.Surface,
		Subject:    in.Subject,
		SessionKey: in.SessionKey,
		RunID:      in.RunID,
		Usage:      usage,
	}
}

// hooksFor resolves the names a run declared into the hooks to call for one
// event, in the order they should be called.
func (d *Dispatcher) hooksFor(ctx context.Context, names []string, event string) ([]*model.WebhookM, error) {
	names = d.applyDefault(names)
	if len(names) == 0 {
		return nil, nil
	}

	var all []*model.WebhookM
	if d.source != nil {
		var err error
		_, all, err = d.source.List(ctx, where.L(-1))
		if err != nil {
			return nil, err
		}
	}
	byName := make(map[string]*model.WebhookM, len(all))
	for _, h := range all {
		byName[h.Name] = h
	}

	out := make([]*model.WebhookM, 0, len(names))
	for _, n := range names {
		h, ok := byName[n]
		if !ok {
			// A name that resolves to nothing is worth saying once per run: it is
			// how an operator learns that the agent names a hook that was
			// deleted, which would otherwise look like a hook that never fires.
			slog.WarnContext(ctx, "webhook: an agent names a hook that does not exist", "hook", n, "event", event)
			continue
		}
		if !h.Enabled() || !h.Subscribes(event) {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

// applyDefault implements the two-layer rule: an agent that names no hooks gets
// the deployment's defaults, and an agent that names "none" gets nothing. The
// second exists so an agent can opt out; without it the only way to have no
// hooks would be to have no defaults either.
func (d *Dispatcher) applyDefault(names []string) []string {
	if len(names) == 0 {
		return d.defaults
	}
	if len(names) == 1 && names[0] == "none" {
		return nil
	}
	return names
}

func (d *Dispatcher) secret(ctx context.Context, ref string) (string, bool) {
	if d.secrets == nil {
		return "", false
	}
	return d.secrets(ctx, ref)
}

func (d *Dispatcher) observe(event, hook, outcome string, elapsed time.Duration) {
	if d.m != nil {
		d.m.ObserveWebhook(event, hook, outcome, elapsed)
	}
}

// shouldSkip reports whether a hook is currently parked or stopped, and marks
// its state so a caller can log why it was not called.
func (d *Dispatcher) shouldSkip(name, event string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped[name] {
		d.observe(event, name, "skipped", 0)
		return time.Time{}, true
	}
	if until, ok := d.parked[name]; ok {
		if d.now().Before(until) {
			d.observe(event, name, "skipped", 0)
			return until, true
		}
		// The cooldown is over: try it again and let the outcome decide.
		delete(d.parked, name)
	}
	return time.Time{}, false
}

// recordFailure counts a failed delivery and parks or stops the hook when it
// has failed enough times in a row. The counter is in memory, so a restart
// gives every hook another chance — which is the right behaviour for a hiccup
// and the wrong one for a hook that is simply misconfigured, and the second is
// why the "stopped" state exists and why it is loud.
func (d *Dispatcher) recordFailure(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures[name]++
	n := d.failures[name]

	switch {
	case n >= disableForGoodAfterFailures && !d.stopped[name]:
		d.stopped[name] = true
		slog.Error("webhook: disabling a hook until restart after repeated failures",
			"hook", name, "failures", n)
	case n >= disableAfterFailures:
		if _, already := d.parked[name]; !already {
			slog.Warn("webhook: parking a hook for a cooldown after repeated failures",
				"hook", name, "failures", n, "cooldown", disableCooldown)
		}
		d.parked[name] = d.now().Add(disableCooldown)
	}
}

// recordSuccess clears a hook's failure record, which is what makes the counter
// a count of *consecutive* failures: a hook that fails, succeeds, then fails
// again is a hook that is working intermittently, not one that is down.
func (d *Dispatcher) recordSuccess(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.failures, name)
	delete(d.parked, name)
}
