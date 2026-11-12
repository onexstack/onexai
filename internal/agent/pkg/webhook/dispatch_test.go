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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/webhookclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// fakeSource serves hooks from memory, so a test never needs a database.
type fakeSource struct {
	hooks []*model.WebhookM
	err   error
	reads int32
}

func (f *fakeSource) List(context.Context, *where.Options) (int64, []*model.WebhookM, error) {
	atomic.AddInt32(&f.reads, 1)
	return int64(len(f.hooks)), f.hooks, f.err
}

func (f *fakeSource) Get(_ context.Context, opts *where.Options) (*model.WebhookM, error) {
	name, _ := opts.Filters["name"].(string)
	for _, h := range f.hooks {
		if h.Name == name {
			return h, nil
		}
	}
	return nil, agentstore.ErrNotFound
}

// hook builds a hook that subscribes to both events and cannot be parked by a
// single failure, so a test that is not about auto-disable is not affected by it.
func hook(name, url string) *model.WebhookM {
	return &model.WebhookM{
		Name:           name,
		Status:         model.StatusEnabled,
		URL:            url,
		Events:         model.StringList{model.EventOnStart, model.EventOnEnd},
		TimeoutSeconds: 2,
	}
}

// allowOnly answers every delivery with an allow verdict and records the bodies
// it was given.
func allowOnly(t *testing.T, bodies *[]webhookclient.Payload, headers *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p webhookclient.Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		if bodies != nil {
			*bodies = append(*bodies, p)
		}
		if headers != nil && headers.Get(webhookclient.HeaderEvent) == "" {
			*headers = r.Header.Clone()
		}
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func invocation(names ...string) Invocation {
	return Invocation{
		HookNames:  names,
		Agent:      "onex-u-user-abc",
		Agents:     []string{"onex-u-user-abc"},
		Surface:    "lark",
		Subject:    webhookclient.Subject{Kind: "user", ID: "user-abc", Name: "kong"},
		SessionKey: "conv-1",
		RunID:      "run-1",
	}
}

func TestBeforeAgentAllowsWhenEveryHookAllows(t *testing.T) {
	srv := allowOnly(t, nil, nil)
	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{hook("h", srv.URL)}}})

	verdict, err := d.BeforeAgent(context.Background(), invocation("h"))
	if err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("BeforeAgent() = %+v, want allowed", verdict)
	}
}

func TestBeforeAgentRefusesAndCarriesTheMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{
			Allowed: false, Code: "quotaExceeded", Message: "今日 AI 用量已达上限",
		})
	}))
	t.Cleanup(srv.Close)
	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{hook("h", srv.URL)}}})

	verdict, err := d.BeforeAgent(context.Background(), invocation("h"))
	if err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if verdict.Allowed {
		t.Fatal("BeforeAgent() allowed a run the hook refused")
	}
	if verdict.Message != "今日 AI 用量已达上限" || verdict.Code != "quotaExceeded" {
		t.Errorf("verdict = %+v, want the receiver's message and code passed through", verdict)
	}
}

// TestBeforeAgentRefusesOnTheFirstHookThatRefuses pins the short circuit: hooks
// after a refusal are not called, so a refusal cannot be undone by a later
// allow and a refusing hook is not asked twice.
func TestBeforeAgentRefusesOnTheFirstHookThatRefuses(t *testing.T) {
	var first, second int32
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&first, 1)
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: false, Message: "no"})
	}))
	t.Cleanup(refusing.Close)
	allowing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&second, 1)
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: true})
	}))
	t.Cleanup(allowing.Close)

	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{
		hook("a", refusing.URL), hook("b", allowing.URL),
	}}})
	verdict, _ := d.BeforeAgent(context.Background(), invocation("a", "b"))
	if verdict.Allowed {
		t.Fatal("want a refusal")
	}
	if got := atomic.LoadInt32(&second); got != 0 {
		t.Errorf("the hook after the refusal was called %d times, want 0", got)
	}
}

// TestBeforeAgentFailOpenOnAnUnreachableHook is the default the platform chose:
// a webhook that is down must not become an outage of the runs it observes.
func TestBeforeAgentFailOpenOnAnUnreachableHook(t *testing.T) {
	h := hook("h", "http://127.0.0.1:1/hook")
	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{h}}})

	verdict, err := d.BeforeAgent(context.Background(), invocation("h"))
	if err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("BeforeAgent() refused a run because the hook was unreachable: %+v", verdict)
	}
}

func TestBeforeAgentFailClosedWhenTheHookSaysFail(t *testing.T) {
	h := hook("h", "http://127.0.0.1:1/hook")
	h.FailureMode = model.FailureModeFail
	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{h}}})

	verdict, _ := d.BeforeAgent(context.Background(), invocation("h"))
	if verdict.Allowed {
		t.Fatal("want a refusal under failureMode=fail")
	}
}

// TestHookIsParkedAfterRepeatedFailures pins the guard against one broken
// endpoint costing every run its timeout.
func TestHookIsParkedAfterRepeatedFailures(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	now := time.Now()
	d := NewDispatcher(&Options{
		Source: &fakeSource{hooks: []*model.WebhookM{hook("h", srv.URL)}},
		Now:    func() time.Time { return now },
	})

	for i := 0; i < disableAfterFailures; i++ {
		d.BeforeAgent(context.Background(), invocation("h"))
	}
	if got := atomic.LoadInt32(&calls); got != disableAfterFailures {
		t.Fatalf("endpoint called %d times, want %d", got, disableAfterFailures)
	}

	// Still inside the cooldown: the hook is skipped, not called again.
	d.BeforeAgent(context.Background(), invocation("h"))
	if got := atomic.LoadInt32(&calls); got != disableAfterFailures {
		t.Errorf("parked hook was called again (%d calls)", got)
	}

	// Past the cooldown: it is tried once more.
	now = now.Add(disableCooldown + time.Second)
	d.BeforeAgent(context.Background(), invocation("h"))
	if got := atomic.LoadInt32(&calls); got != disableAfterFailures+1 {
		t.Errorf("endpoint called %d times after the cooldown, want %d", got, disableAfterFailures+1)
	}
}

// TestSuccessClearsTheFailureRecord is what makes the counter consecutive: a
// hook that fails, recovers, then fails again is working intermittently.
func TestSuccessClearsTheFailureRecord(t *testing.T) {
	var fail int32 = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: true})
	}))
	t.Cleanup(srv.Close)

	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{hook("h", srv.URL)}}})
	for i := 0; i < disableAfterFailures-1; i++ {
		d.BeforeAgent(context.Background(), invocation("h"))
	}
	atomic.StoreInt32(&fail, 0)
	d.BeforeAgent(context.Background(), invocation("h"))

	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.failures["h"]; n != 0 {
		t.Errorf("failure count = %d after a success, want 0", n)
	}
}

// TestHookLayering pins the two-layer rule: an agent that names no hooks
// gets the deployment's defaults, and an agent that names "none" gets nothing.
func TestHookLayering(t *testing.T) {
	var bodies []webhookclient.Payload
	srv := allowOnly(t, &bodies, nil)
	hooks := []*model.WebhookM{hook("default", srv.URL), hook("explicit", srv.URL)}

	for _, tc := range []struct {
		name  string
		names []string
		want  int
	}{
		{"no names falls back to the defaults", nil, 1},
		{"a name of its own replaces the defaults", []string{"explicit"}, 1},
		{"none opts out entirely", []string{"none"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies = nil
			d := NewDispatcher(&Options{
				Source:   &fakeSource{hooks: hooks},
				Defaults: []string{"default"},
			})
			if _, err := d.BeforeAgent(context.Background(), invocation(tc.names...)); err != nil {
				t.Fatalf("BeforeAgent() error = %v", err)
			}
			if len(bodies) != tc.want {
				t.Fatalf("%d deliveries, want %d", len(bodies), tc.want)
			}
		})
	}
}

// TestAfterAgentDeliversUsageAndIgnoresTheVerdict covers the reporting half: the
// body carries what the run spent, and a refusal there changes nothing because
// the run has already answered.
func TestAfterAgentDeliversUsageAndIgnoresTheVerdict(t *testing.T) {
	var bodies []webhookclient.Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p webhookclient.Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		bodies = append(bodies, p)
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: false, Message: "too late"})
	}))
	t.Cleanup(srv.Close)

	d := NewDispatcher(&Options{Source: &fakeSource{hooks: []*model.WebhookM{hook("h", srv.URL)}}})
	d.AfterAgent(context.Background(), InvocationResult{
		Invocation: invocation("h"),
		Status:     "succeeded",
		Usage:      webhookclient.Usage{PromptTokens: 1234, CompletionTokens: 567, Calls: 3},
	})

	if len(bodies) != 1 {
		t.Fatalf("%d deliveries, want 1", len(bodies))
	}
	got := bodies[0]
	if got.ObjectKind != "agentInvocationResult" || got.Event != model.EventOnEnd {
		t.Errorf("objectKind/event = %q/%q, want agentInvocationResult/onEnd", got.ObjectKind, got.Event)
	}
	if got.Usage == nil || got.Usage.Calls != 3 || got.Usage.PromptTokens != 1234 {
		t.Errorf("usage = %+v, want the run's totals", got.Usage)
	}
}

// TestDeliveryCarriesTheContractHeaders pins the headers a receiver routes on:
// without them the body is the only way to know which event arrived.
func TestDeliveryCarriesTheContractHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: true})
	}))
	t.Cleanup(srv.Close)

	h := hook("onex-quota", srv.URL)
	h.SecretRef = "sec-1"
	h.Headers = model.StringMap{"X-Tenant": "onex"}
	d := NewDispatcher(&Options{
		Source:   &fakeSource{hooks: []*model.WebhookM{h}},
		Instance: "onexai-dev",
		Secrets: func(context.Context, string) (string, bool) {
			return "s3cret", true
		},
	})
	if _, err := d.BeforeAgent(context.Background(), invocation("onex-quota")); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}

	for name, want := range map[string]string{
		webhookclient.HeaderEvent:    model.EventOnStart,
		webhookclient.HeaderToken:    "s3cret",
		webhookclient.HeaderHookUUID: "onex-quota",
		webhookclient.HeaderInstance: "onexai-dev",
		"X-Tenant":                   "onex",
	} {
		if got.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, got.Get(name), want)
		}
	}
	if got.Get(webhookclient.HeaderEventUUID) == "" {
		t.Error("no per-delivery identifier was sent")
	}
}

// TestMissingSecretRefusesTheDelivery: an endpoint that checks its token would
// reject an unauthenticated call anyway, and one that does not would be
// accepting a caller this deployment never intended.
func TestMissingSecretRefusesTheDelivery(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(webhookclient.Verdict{Allowed: true})
	}))
	t.Cleanup(srv.Close)

	h := hook("h", srv.URL)
	h.SecretRef = "missing"
	h.FailureMode = model.FailureModeFail
	d := NewDispatcher(&Options{
		Source:  &fakeSource{hooks: []*model.WebhookM{h}},
		Secrets: func(context.Context, string) (string, bool) { return "", false },
	})

	verdict, _ := d.BeforeAgent(context.Background(), invocation("h"))
	if verdict.Allowed {
		t.Error("want a refusal when the hook's secret is missing")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("the unauthenticated call was made anyway")
	}
}

// TestUnconfiguredDispatcherLetsEverythingThrough: with no source and no
// default there is nothing to resolve, which is the state of a deployment that
// has not adopted hooks.
func TestClientAndURLResolvesClientConfig(t *testing.T) {
	d := &Dispatcher{client: webhookclient.New(nil)}
	h := &model.WebhookM{
		ClientConfigJSON: `{"url":"http://127.0.0.1:18081/hook","timeoutSeconds":5,"headers":{"X-Tenant":"onexai"}}`,
	}

	client, endpoint, err := d.clientAndURL(h)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "http://127.0.0.1:18081/hook", endpoint)
}

func TestClientAndURLRejectsAnInvalidClientConfig(t *testing.T) {
	d := &Dispatcher{}
	_, _, err := d.clientAndURL(&model.WebhookM{ClientConfigJSON: `{"url":""}`})
	assert.Error(t, err)
}

func TestClientAndURLKeepsTheLegacyURL(t *testing.T) {
	shared := webhookclient.New(nil)
	d := &Dispatcher{client: shared}
	client, endpoint, err := d.clientAndURL(&model.WebhookM{URL: "https://example.com/hook"})
	require.NoError(t, err)
	assert.Same(t, shared, client)
	assert.Equal(t, "https://example.com/hook", endpoint)
}

func TestUnconfiguredDispatcherLetsEverythingThrough(t *testing.T) {
	var d *Dispatcher
	if verdict, err := d.BeforeAgent(context.Background(), invocation()); err != nil || !verdict.Allowed {
		t.Errorf("nil dispatcher: verdict=%+v err=%v, want allowed", verdict, err)
	}
	if d.Enabled() {
		t.Error("a nil dispatcher reports Enabled()")
	}
	d2 := NewDispatcher(nil)
	if d2.Enabled() {
		t.Error("a dispatcher with no source and no defaults reports Enabled()")
	}
}
