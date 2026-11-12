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

// Package webhookclient delivers one run-lifecycle event to one endpoint and
// reports what came back.
//
// # What it is, and what it deliberately is not
//
// It is the transport: a POST, the delivery headers, a timeout, and the
// decision about what a non-2xx status means. It holds no policy — which hooks
// a run calls, what a refusal does to the run, and whether an endpoint that
// keeps failing should stop being called all belong to the dispatcher that uses
// it (internal/agent/pkg/webhook).
//
// # Why one client rather than one per endpoint
//
// Connections are pooled per host by http.Transport, so a client per endpoint
// would be several pools for one host. The timeout is per call instead, from the
// hook's own configuration, which is also what keeps a slow endpoint from
// holding a run up for longer than its row says it may.
package webhookclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/webhook"
)

// newUUID is the default per-delivery identifier.
func newUUID() string { return uuid.NewString() }

// Delivery headers. They are the contract's other half: the body carries the
// event, and these say which hook is delivering it and which delivery it is,
// without the receiver having to parse anything.
const (
	// HeaderEvent names the lifecycle moment: the same value the body carries in
	// its event field. It is repeated as a header so a receiver can route
	// without parsing a body it may not understand.
	HeaderEvent = "X-Onexai-Event"
	// HeaderToken carries the shared secret named by the hook's secretRef. It is
	// a plain shared secret compared constant-time, not a signature: the sender
	// and the receiver are two of this platform's own services and the contract
	// is what they agree on, so there is nothing to recover from the body.
	HeaderToken = "X-Onexai-Token"
	// HeaderEventUUID is unique per delivery, so a receiver can drop a repeat
	// rather than act on it twice.
	HeaderEventUUID = "X-Onexai-Event-UUID"
	// HeaderHookUUID names the hook row that produced this delivery, so a
	// receiver serving several can tell them apart.
	HeaderHookUUID = "X-Onexai-Hook-UUID"
	// HeaderInstance names the deployment the delivery came from.
	HeaderInstance = "X-Onexai-Instance"
)

// Subject is who the run is for. ID is the asker's subject — the platform
// account for a speaker OneX has bound, otherwise the chat identity — which is
// the same string the platform's ledger keys usage by.
type Subject struct {
	Kind  string `json:"kind"` // "user" | "serviceaccount"
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	IsSRE bool   `json:"isSRE,omitempty"`
}

// Usage is what a finished run spent. The five field names are the platform's
// own usage contract, verbatim, so a receiver reading both sees the same words.
type Usage struct {
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	Calls            int64 `json:"calls"`
	CacheHitTokens   int64 `json:"cacheHitTokens"`
	CacheMissTokens  int64 `json:"cacheMissTokens"`
}

// Payload is the default request body. It carries the event that is being
// delivered, so one body shape serves both — the objectKind says which.
type Payload struct {
	ObjectKind string   `json:"objectKind"` // "agentInvocation" | "agentInvocationResult"
	Event      string   `json:"event"`      // "onStart" | "onEnd"
	Agent      string   `json:"agent,omitempty"`
	Agents     []string `json:"agents,omitempty"`
	Surface    string   `json:"surface,omitempty"`
	Subject    Subject  `json:"subject"`
	SessionKey string   `json:"sessionKey,omitempty"`
	RunID      string   `json:"runID,omitempty"`
	Usage      *Usage   `json:"usage,omitempty"` // onEnd only
}

// Verdict is the answer a delivery came back with. It is the extension on top
// of the webhook contract this service borrows: the response body of a plain
// webhook is ignored, and this one is read for the decision.
type Verdict struct {
	// Allowed is whether the run may proceed. It is meaningful for onStart; an
	// onEnd delivery's answer changes nothing and is not consulted.
	Allowed bool `json:"allowed"`
	// Message is shown to the person whose run was refused. It is the reason in
	// their language, which is why it is a field of its own rather than the
	// HTTP status text.
	Message string `json:"message,omitempty"`
	// Code names the refusal in the platform's error vocabulary, so a receiver
	// can say "out of allowance" rather than leaving every refusal looking like
	// a permissions problem.
	Code string `json:"code,omitempty"`
}

// Request is one delivery.
type Request struct {
	// HookName identifies the row this came from; it goes in a header and in
	// the log.
	HookName string
	// Event is the event being delivered (Payload.Event, repeated here because
	// the dispatcher routes on it).
	Event string
	// URL is the endpoint. The hook's validation has already refused anything
	// without a scheme.
	URL string
	// Token is the shared secret, or empty for an endpoint that takes none.
	Token string
	// Headers are the hook's own custom headers. The hook's validation has
	// already refused the reserved namespace.
	Headers map[string]string
	// Timeout bounds the call.
	Timeout time.Duration
	// Instance names this deployment.
	Instance string
	// Body is the payload, already rendered — the default shape, or the hook's
	// template if it has one.
	Body []byte
}

// Response is what a delivery produced. Exactly one of Err and Reached is set:
// Reached means the endpoint answered 2xx (whether it allowed the run is in
// Verdict), and Err means it did not answer at all, timed out, or answered
// something else.
type Response struct {
	Reached bool
	Verdict Verdict
	Status  int
	Err     error
}

// Client delivers events. It is safe for concurrent use.
type Client struct {
	client  *webhook.Client
	newUUID func() string
}

// Options configures a Client.
type Options struct {
	// Transport, when nil, is a default one with keep-alives enabled: the
	// connection pool is the reason this client is shared rather than built per
	// call.
	Transport http.RoundTripper
	// NewUUID generates the per-delivery identifier. Nil means a random one.
	NewUUID func() string
}

// New returns a Client. A nil Options is valid.
func New(opts *Options) *Client {
	if opts == nil {
		opts = &Options{}
	}
	tr := opts.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	uuid := opts.NewUUID
	if uuid == nil {
		uuid = newUUID
	}
	return &Client{
		// No Timeout here: the deadline comes from the hook's own timeout, per
		// call. See webhook.New.
		client:  webhook.New(tr),
		newUUID: uuid,
	}
}

// Deliver posts one delivery and reports what happened. It never returns an
// error: a delivery that failed is a Response with Err set, because "the
// endpoint did not answer" is one of the outcomes the caller has a policy for
// (see the hook's failure mode), not an exceptional path.
func (c *Client) Deliver(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	headers := map[string]string{
		"Content-Type":  "application/json",
		HeaderEvent:     req.Event,
		HeaderEventUUID: c.newUUID(),
		HeaderHookUUID:  req.HookName,
	}
	if req.Instance != "" {
		headers[HeaderInstance] = req.Instance
	}
	if req.Token != "" {
		headers[HeaderToken] = req.Token
	}
	// The hook's own headers last, so they cannot replace a delivery header even
	// if one reached the database before the validation that refuses them.
	for k, v := range req.Headers {
		if _, ok := headers[k]; !ok {
			headers[k] = v
		}
	}

	status, body, err := c.client.Post(ctx, req.URL, req.Body, headers)
	if err != nil {
		return Response{Err: err}
	}
	if !webhook.StatusOK(status) {
		return Response{
			Status: status,
			Err:    fmt.Errorf("calling %s: status %d: %s", req.URL, status, bytes.TrimSpace(body)),
		}
	}

	// The answer is read, but a body that will not parse is not a failure: a
	// plain webhook receiver answers 2xx with nothing, and treating that as a
	// broken delivery would make the simplest possible receiver unusable.
	var out Verdict
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return Response{Reached: true, Verdict: out, Status: status}
}
