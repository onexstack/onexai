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

package a2a

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Path and PathREST are the only place this surface's transport mount is written
// down. The routes the handler package registers, the REST prefix it strips and
// the interface URLs the published card advertises are all derived from them, so
// moving the transports cannot leave the card pointing at the old address.
//
// PathCard is deliberately not derived from Path. A2A discovers the card at the
// origin-root well-known location the protocol defines (RFC 8615), not under the
// transport mount. It aliases the SDK's constant, so the route and the published
// location cannot drift; the card's location is a protocol constant, not a
// deployment choice.
const (
	// Path is where the JSON-RPC transport answers.
	Path = "/v1/chat/a2a"
	// PathREST is the prefix the HTTP+JSON transport is mounted under, and
	// which has to be stripped before the SDK's handler sees the request: its
	// routes are absolute and start at /message:send, /tasks, ...
	PathREST = Path + "/rest"
	// PathCard is the origin-root well-known location of the agent card. It is
	// not Path-prefixed: see the comment above.
	PathCard = a2asrv.WellKnownAgentCardPath
	// Source names this surface on audit records and run metadata.
	Source = "a2a"
)

// Options is what a deployment decides about the A2A surface.
type Options struct {
	// DefaultAgent names the agent set a request gets when it does not name one
	// itself. Empty leaves the server's own default in place.
	DefaultAgent string
	// Description is advertised by the agent card. Empty falls back to a
	// generic sentence, which is what a deployment that did not bother says.
	Description string
	// PublicURL is the absolute base URL the card advertises for the
	// transports, e.g. https://onexai.example.com/v1/chat/a2a. It does not
	// change where the card itself is served: that is PathCard, fixed at the
	// protocol's origin-root well-known location.
	//
	// Empty means the deployment did not decide, and the card then carries the
	// path-relative default. That is usable from a client that resolved the
	// card from the same origin and unusable from a strict one, so startup says
	// so rather than guessing a host — see TrustedProxy.
	PublicURL string
	// TrustedProxy lets the card's URLs be derived from the request
	// (X-Forwarded-Proto/Host, then Host) when PublicURL is empty.
	//
	// Off by default, because the card route is unauthenticated and nothing in
	// this service validates those headers: anyone who can reach the surface
	// could otherwise make the card point a peer at an address of their
	// choosing. It is meaningful only when every request really does arrive
	// through a proxy this deployment controls.
	TrustedProxy bool
	// Version is the agent's own version, advertised by the card.
	Version string
	// Guard screens a turn before it reaches the model. Nil does not screen,
	// which is the state of a deployment that configured no inbound audit.
	Guard Guard
	// Lookup refuses a request naming an agent this deployment does not have.
	// Nil skips the check, which is all a deployment with no resource store can
	// do; the run then answers from an agent with no instruction and no tools,
	// which is why the check is worth wiring whenever a store exists.
	Lookup AgentLookup
	// TaskStore keeps tasks where every replica can read them. Nil uses the
	// SDK's in-memory store, which makes a task a property of one process: a
	// restart loses it and another replica cannot answer for it.
	TaskStore taskstore.Store
	// QueueManager carries a task's events between replicas, which is what lets
	// a client that lost its stream reattach through a different replica.
	QueueManager eventqueue.Manager
	// WorkQueue distributes executions between replicas. Nil executes a task on
	// the replica that received the request.
	WorkQueue workqueue.Queue
	// ContextCodec carries the caller and the edge's run settings to whichever
	// replica executes the task. It is required once WorkQueue is set: without
	// it the executing replica starts from a bare context and the run loses its
	// caller.
	ContextCodec a2asrv.ContextCodec
}

// Handlers are the three transports of one surface. They share one request
// handler, so a task a JSON-RPC call started can be read back over REST.
type Handlers struct {
	// JSONRPC serves POST /v1/chat/a2a.
	JSONRPC http.Handler
	// REST serves everything under /v1/chat/a2a/rest.
	REST http.Handler
	// Card serves the agent card at the origin-root well-known path.
	Card http.Handler
}

// NewHandlers builds the surface over the given runner.
//
// It is a constructor rather than a provider body so that the card, the
// transports and the identity adapter are tested together: what a client sees
// and what a call is authorized as are two halves of one contract.
//
// A nil runner builds a surface that answers every task with "the agent engine
// is not configured". That is deliberate: the card and the protocol error shape
// are still worth serving in a deployment that runs no agent, and a missing
// route would be indistinguishable from a mistyped URL.
func NewHandlers(runner Runner, opts Options) (*Handlers, error) {
	if opts.PublicURL != "" {
		if err := validatePublicURL(opts.PublicURL); err != nil {
			return nil, err
		}
	}

	// The SDK builds its own CallContext per request and thinks the caller
	// unauthenticated until an interceptor says otherwise. This is the only hook
	// that runs after that CallContext exists, so it is where the AK/SK identity
	// becomes the A2A user — which is what the task store's ownership check reads.
	requestOptions := []a2asrv.RequestHandlerOption{
		a2asrv.WithCallInterceptors(identityInterceptor{}),
	}
	// Cluster mode is on exactly when all three pieces are wired: a task store,
	// somewhere to put events, and a way to hand work to another replica. It is
	// what makes tasks/resubscribe work across pods — the run executes on one
	// replica and any other can replay its events. Falling back to the task
	// store alone keeps tasks/get working, which is the polling substitute.
	switch {
	case opts.TaskStore != nil && opts.QueueManager != nil && opts.WorkQueue != nil:
		requestOptions = append(requestOptions, a2asrv.WithClusterMode(a2asrv.ClusterConfig{
			QueueManager: opts.QueueManager,
			WorkQueue:    opts.WorkQueue,
			TaskStore:    opts.TaskStore,
			ContextCodec: opts.ContextCodec,
		}))
	case opts.TaskStore != nil:
		requestOptions = append(requestOptions, a2asrv.WithTaskStore(opts.TaskStore))
	}

	requestHandler := a2asrv.NewHandler(NewExecutor(runner, opts), requestOptions...)

	return &Handlers{
		JSONRPC: a2asrv.NewJSONRPCHandler(requestHandler),
		REST:    a2asrv.NewRESTHandler(requestHandler),
		Card:    cardHandler(buildCard(opts), opts),
	}, nil
}

// validatePublicURL refuses anything a client could not use as a base URL.
func validatePublicURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("a2a: a2a-public-url %q is not a URL: %w", raw, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("a2a: a2a-public-url %q must be absolute (http:// or https://)", raw)
	}
	return nil
}

// userKindAttribute is where the identity interceptor records what the caller
// is: the A2A user carries a name and a flag, and the authorization kind has to
// travel beside them so a run on another replica does not turn a service into a
// person.
const userKindAttribute = "kind"

// identityInterceptor carries the authenticated caller into the A2A call.
//
// The subject is the one the AK/SK middleware attached before the SDK ran; a
// deployment with authentication bypassed has none, and the executor then falls
// back to the surface's own name. Nothing here rejects a request: the boundary
// check has already happened, and this is attribution, not authorization.
type identityInterceptor struct{}

// Before implements a2asrv.CallInterceptor.
func (identityInterceptor) Before(
	ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request,
) (context.Context, any, error) {
	if subject, ok := authz.SubjectFromContext(ctx); ok {
		if id := subject.PrimaryID(); id != "" {
			callCtx.User = a2asrv.NewAuthenticatedUser(id, map[string]any{
				userKindAttribute: string(subject.Kind),
			})
		}
	}
	return ctx, nil, nil
}

// After implements a2asrv.CallInterceptor.
func (identityInterceptor) After(context.Context, *a2asrv.CallContext, *a2asrv.Response) error {
	return nil
}
