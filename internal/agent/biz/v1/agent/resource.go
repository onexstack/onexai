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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2aclient"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errmsg"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// AgentResourceBiz is the declarative Agent CRUD business logic.
//
// It is separate from the run-time IAgentBiz in this package: that one answers
// what a run does, this one answers what the resource API stores.
type AgentResourceBiz interface {
	Create(ctx context.Context, req *v1.CreateAgentRequest) (*v1.CreateAgentResponse, error)
	Get(ctx context.Context, req *v1.GetAgentRequest) (*v1.GetAgentResponse, error)
	List(ctx context.Context, req *v1.ListAgentRequest) (*v1.ListAgentResponse, error)
	Update(ctx context.Context, req *v1.UpdateAgentRequest) (*v1.UpdateAgentResponse, error)
	Delete(ctx context.Context, req *v1.DeleteAgentRequest) (*v1.DeleteAgentResponse, error)
	// RegeneratePrompt removes the derived aggregated Prompt and lets the
	// controller rebuild it from the bound sources.
	RegeneratePrompt(ctx context.Context, req *v1.GetAgentRequest) (*v1.RegenerateAgentPromptResponse, error)
	// Owner returns the subject a debug run executes as. An ownerless agent is
	// an invalid target: the run would have no subject to authorize.
	Owner(ctx context.Context, name string) (string, error)
	// Exists reports whether an agent with name exists. Callers that only need
	// to validate a reference use it instead of loading the whole record.
	Exists(ctx context.Context, name string) (bool, error)
	// PreviewA2A fetches and normalizes a card without persisting anything.
	// It backs the registration wizard's "fetch and preview" action.
	PreviewA2A(ctx context.Context, req *v1.PreviewAgentA2ARequest) (*v1.PreviewAgentA2AResponse, error)

	InvokeA2A(ctx context.Context, req *v1.InvokeAgentA2ARequest) (*v1.InvokeAgentA2AResponse, error)
	StreamA2A(ctx context.Context, req *v1.InvokeAgentA2ARequest) (string, <-chan a2aclient.StreamEvent, error)
	GetA2ATask(ctx context.Context, name, taskID string) (*v1.InvokeAgentA2AResponse, error)
	CancelA2ATask(ctx context.Context, name, taskID string) error
	ListA2AInvocations(ctx context.Context, req *v1.ListAgentA2AInvocationRequest) (*v1.ListAgentA2AInvocationResponse, error)
	GetA2AInvocation(ctx context.Context, req *v1.GetAgentA2AInvocationRequest) (*v1.GetAgentA2AInvocationResponse, error)
}

type agentResourceBiz struct {
	store  agentstore.AIStore
	invoke *a2ainvoke.Service
	// webhookAuthz resolves an Agent's own authorization Webhooks. Nil leaves
	// the invoke surface on the platform's behavior.
	webhookAuthz *agentauthz.Resolver
	// toolCache drops discovered MCP tool sets after an agent write. Nil is a
	// deployment without an MCP runtime.
	toolCache mcpclient.Invalidator
}

var _ AgentResourceBiz = (*agentResourceBiz)(nil)

// NewResource creates an AgentResourceBiz. invalidator may be nil.
func NewResource(
	store agentstore.AIStore, webhookAuthz *agentauthz.Resolver, invalidator mcpclient.Invalidator,
) *agentResourceBiz {
	return &agentResourceBiz{store: store, webhookAuthz: webhookAuthz, toolCache: invalidator}
}

func (b *agentResourceBiz) invalidateTools() {
	if b.toolCache != nil {
		b.toolCache.Invalidate()
	}
}

// authorizeInvocation applies the Agent's own authentication and authorization
// to a direct invocation.
//
// The HTTP edge already authenticated the request; this repeats the per-Agent
// checks for the same reason admitRun does — the invoke surface does not go
// through run admission, and a caller reaching the business layer directly must
// not skip the Agent's own policy.
func (b *agentResourceBiz) authorizeInvocation(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	row, err := b.store.Agents().Get(ctx, model.EntryFilter(name))
	if err != nil && !errors.Is(err, agentstore.ErrNotFound) {
		return storeerr.Read(err)
	}
	if row == nil {
		return nil
	}
	if err := enforceAgentAuthn(ctx, []*model.AgentM{row}); err != nil {
		return err
	}
	if b.webhookAuthz == nil || len(row.AuthzWebhooks) == 0 {
		return nil
	}
	authorizer, found, err := b.webhookAuthz.AuthorizerFor(ctx, name)
	if err != nil {
		return errno.ErrInternal.WithMessage("the authorization webhook for agent %q could not be resolved: %s", name, err.Error())
	}
	if !found {
		return nil
	}
	subj, ok := authz.SubjectFromContext(ctx)
	if !ok {
		return errno.ErrUnauthenticated.WithMessage("the caller has no identity for agent %q", name)
	}
	_, ruleName := sharing.SplitRef(name)
	decision, reason, err := authorizer.Authorize(ctx, authz.AuthorizationRequest{
		Subject: subj, Verb: authz.VerbRead, AgentName: ruleName,
	})
	if err != nil {
		return errno.ErrInternal.WithMessage("the authorization webhook for agent %q failed", name)
	}
	if decision != authz.DecisionAllow {
		return &authz.ErrAgentAccessDenied{Subject: subj, AgentName: name, Reason: reason}
	}
	return nil
}

// a2aService builds the invocation service on first use. The eager version
// would call A2AInvocations through every test fake, including the ones that
// never touch A2A and embed a nil AIStore.
func (b *agentResourceBiz) a2aService() *a2ainvoke.Service {
	if b.invoke == nil {
		b.invoke = a2ainvoke.New(b.store.Agents(), b.store.A2AInvocations(), b.store.Agents())
	}
	return b.invoke
}

// Create implements AgentResourceBiz.
func (b *agentResourceBiz) Create(ctx context.Context, req *v1.CreateAgentRequest) (*v1.CreateAgentResponse, error) {
	row := conversion.AgentV1ToAgentModel(req.GetAgent())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Agents().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.CreateAgentResponse{Agent: conversion.AgentModelToAgentV1(row)}, nil
}

// Exists implements AgentResourceBiz.
func (b *agentResourceBiz) Exists(ctx context.Context, name string) (bool, error) {
	row, err := b.store.Agents().Get(ctx, where.F("name", name))
	if err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return row != nil, nil
}

// Get implements AgentResourceBiz.
func (b *agentResourceBiz) Get(ctx context.Context, req *v1.GetAgentRequest) (*v1.GetAgentResponse, error) {
	row, err := b.store.Agents().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "agent %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetAgentResponse{Agent: conversion.AgentModelToAgentV1(row)}, nil
}

// List implements AgentResourceBiz.
func (b *agentResourceBiz) List(ctx context.Context, req *v1.ListAgentRequest) (*v1.ListAgentResponse, error) {
	total, list, err := b.store.Agents().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "agent") {
		before := len(list)
		list = authzview.Filter(ctx, "agent", list, func(row *model.AgentM) (string, string, string) {
			return row.Namespace, row.Name, row.Owner
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	agents := make([]*v1.Agent, 0, len(list))
	for _, row := range list {
		agents = append(agents, conversion.AgentModelToAgentV1(row))
	}
	return &v1.ListAgentResponse{Agents: agents, Count: total}, nil
}

// Update implements AgentResourceBiz.
func (b *agentResourceBiz) Update(ctx context.Context, req *v1.UpdateAgentRequest) (*v1.UpdateAgentResponse, error) {
	row := conversion.AgentV1ToAgentModel(req.GetAgent())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Agents().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	// spec.customCommands / spec.mcps may have changed.
	b.invalidateTools()
	return &v1.UpdateAgentResponse{Agent: conversion.AgentModelToAgentV1(row)}, nil
}

// Delete implements AgentResourceBiz.
func (b *agentResourceBiz) Delete(ctx context.Context, req *v1.DeleteAgentRequest) (*v1.DeleteAgentResponse, error) {
	if err := b.store.Agents().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.DeleteAgentResponse{}, nil
}

// RegeneratePrompt implements AgentResourceBiz.
//
// Deleting is the same reset whether the aggregate was generated or manually
// overridden.
// Owner implements AgentResourceBiz.
func (b *agentResourceBiz) Owner(ctx context.Context, name string) (string, error) {
	// name may be "namespace/name"; a bare name still resolves when unique.
	row, err := b.store.Agents().Get(ctx, model.EntryFilter(name))
	if err := storeerr.GetError(row, err, "agent %q not found", name); err != nil {
		return "", err
	}
	if row.Owner == "" {
		// An ownerless agent gives the run no subject: authorization has nothing
		// to evaluate, and tools that scope their data by owner refuse the call.
		// That would surface as a failure halfway through a stream, where the
		// status can no longer carry an explanation, so it is refused here.
		return "", errno.ErrInvalidArgument.WithMessage(
			"agent %q has no owner, so a debug run cannot be attributed", name)
	}
	return row.Owner, nil
}

func (b *agentResourceBiz) RegeneratePrompt(
	ctx context.Context, req *v1.GetAgentRequest,
) (*v1.RegenerateAgentPromptResponse, error) {
	name := req.GetName()
	agent, err := b.store.Agents().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(agent, err, "agent %q not found", name); err != nil {
		return nil, err
	}

	err = b.store.Prompts().Delete(ctx, where.F("name", agentv1alpha1.AggregatedPromptName(name)))
	if err != nil {
		if !apierrors.IsNotFound(err) && !errors.Is(err, agentstore.ErrNotFound) {
			return nil, err
		}
		// The derived object was already gone. Nudge the controller so it
		// reconciles even when no Prompt event follows.
		if row, getErr := b.store.Agents().Get(ctx, where.F("name", name)); getErr == nil {
			if updateErr := b.store.Agents().Update(ctx, row); updateErr != nil {
				return nil, updateErr
			}
		}
	}
	return &v1.RegenerateAgentPromptResponse{Regenerated: true}, nil
}

// PreviewA2A implements AgentResourceBiz.
//
// The request is the whole card source: the form sends what the user is
// looking at, including a transient credential. Nothing is persisted and no
// stored row is merged in, so clearing a URL in the form cannot make the test
// silently pass against the previous value.
func (b *agentResourceBiz) PreviewA2A(ctx context.Context, req *v1.PreviewAgentA2ARequest) (*v1.PreviewAgentA2AResponse, error) {
	execution := req.GetExecution()
	a := execution.GetA2A()
	if a == nil {
		return &v1.PreviewAgentA2AResponse{Ok: false, Message: "execution.a2a is required"}, nil
	}
	headers, err := previewHeaders(ctx, a.GetAuth())
	if err != nil {
		return &v1.PreviewAgentA2AResponse{Ok: false, Message: errmsg.Text(err)}, nil
	}
	var card *a2aclient.Card
	start := time.Now()
	if (a.GetSource() == "json" || a.GetSource() == "manual") && a.GetCard() != "" {
		card, err = a2aclient.ParseCard([]byte(a.GetCard()))
	} else {
		candidates := a2aclient.CandidateCardURLs(a.GetEndpoint(), a.GetCardUrl())
		if len(candidates) == 0 {
			return &v1.PreviewAgentA2AResponse{Ok: false, Message: "cardUrl or endpoint is required"}, nil
		}
		fetcher := netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: true,
			MaxBytes:          256 << 10,
			Timeout:           10 * time.Second,
			MaxRedirects:      3,
			Retries:           -1,
		})
		httpClient := a2aclient.TimeoutClient(fetcher.Client(), 10*time.Second)
		httpClient = a2aclient.ClientWithHeaders(httpClient, headers)
		card, err = a2aclient.FetchCard(ctx, httpClient, candidates, nil)
	}
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &v1.PreviewAgentA2AResponse{Ok: false, Message: errmsg.Text(err), LatencyMs: latency}, nil
	}
	return &v1.PreviewAgentA2AResponse{
		Ok:               true,
		LatencyMs:        latency,
		EffectiveCardUrl: card.SourceURL,
		Summary:          cardToA2AStatus(card),
		RawCard:          string(card.Raw),
	}, nil
}

// InvokeA2A implements AgentResourceBiz.
func (b *agentResourceBiz) InvokeA2A(ctx context.Context, req *v1.InvokeAgentA2ARequest) (*v1.InvokeAgentA2AResponse, error) {
	if err := b.authorizeInvocation(ctx, req.GetName()); err != nil {
		return nil, err
	}
	call := callRequestFromV1(ctx, req)
	result, err := b.a2aService().Invoke(ctx, call)
	if err != nil {
		return nil, a2aErrno(err)
	}
	return callResultToV1(result), nil
}

// StreamA2A implements AgentResourceBiz.
func (b *agentResourceBiz) StreamA2A(ctx context.Context, req *v1.InvokeAgentA2ARequest) (string, <-chan a2aclient.StreamEvent, error) {
	if err := b.authorizeInvocation(ctx, req.GetName()); err != nil {
		return "", nil, err
	}
	taskID, events, err := b.a2aService().Stream(ctx, callRequestFromV1(ctx, req))
	if err != nil {
		return "", nil, a2aErrno(err)
	}
	return taskID, events, nil
}

// GetA2ATask implements AgentResourceBiz.
func (b *agentResourceBiz) GetA2ATask(ctx context.Context, name, taskID string) (*v1.InvokeAgentA2AResponse, error) {
	result, err := b.a2aService().GetTask(ctx, name, taskID)
	if err != nil {
		return nil, a2aErrno(err)
	}
	return callResultToV1(&a2ainvoke.CallResult{Result: result}), nil
}

// CancelA2ATask implements AgentResourceBiz.
func (b *agentResourceBiz) CancelA2ATask(ctx context.Context, name, taskID string) error {
	return a2aErrno(b.a2aService().Cancel(ctx, name, taskID))
}

// ListA2AInvocations implements AgentResourceBiz.
func (b *agentResourceBiz) ListA2AInvocations(ctx context.Context, req *v1.ListAgentA2AInvocationRequest) (*v1.ListAgentA2AInvocationResponse, error) {
	total, rows, err := b.a2aService().List(ctx, req.GetName(), int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := &v1.ListAgentA2AInvocationResponse{Count: total}
	for _, row := range rows {
		out.Invocations = append(out.Invocations, invocationToV1(row))
	}
	return out, nil
}

// GetA2AInvocation implements AgentResourceBiz.
func (b *agentResourceBiz) GetA2AInvocation(ctx context.Context, req *v1.GetAgentA2AInvocationRequest) (*v1.GetAgentA2AInvocationResponse, error) {
	row, err := b.a2aService().Get(ctx, req.GetName(), req.GetInvocationId())
	if err := storeerr.GetError(row, err, "A2A invocation %q not found", req.GetInvocationId()); err != nil {
		return nil, err
	}
	return &v1.GetAgentA2AInvocationResponse{Invocation: invocationToV1(row)}, nil
}

// a2aErrno maps an A2A invocation failure to the wire error: an absent
// invocation is 404, everything else is the upstream's failure with a fixed
// message (the client's error text names the endpoint and sometimes the key).
func a2aErrno(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentstore.ErrNotFound) || apierrors.IsNotFound(err) {
		return errno.ErrNotFound
	}
	return errno.ErrInternal.WithMessage("the A2A invocation failed")
}

func callRequestFromV1(ctx context.Context, req *v1.InvokeAgentA2ARequest) a2ainvoke.CallRequest {
	call := a2ainvoke.CallRequest{
		Name:      req.GetName(),
		Message:   req.GetMessage(),
		ContextID: req.GetContextId(),
		TaskID:    req.GetTaskId(),
		Skill:     req.GetSkill(),
		RequestID: req.GetRequestId(),
		Timeout:   time.Duration(req.GetTimeoutSeconds()) * time.Second,
		Source:    "console",
	}
	if raw := req.GetMetadataJson(); raw != "" {
		_ = json.Unmarshal([]byte(raw), &call.Metadata)
	}
	if subject, ok := authz.SubjectFromContext(ctx); ok {
		call.CallerKind = string(subject.Kind)
		call.CallerID = subject.PrimaryID()
		call.CallerName = subject.UserName
	}
	return call
}

func callResultToV1(result *a2ainvoke.CallResult) *v1.InvokeAgentA2AResponse {
	if result == nil {
		return &v1.InvokeAgentA2AResponse{}
	}
	out := &v1.InvokeAgentA2AResponse{
		InvocationId: result.InvocationID,
		LatencyMs:    result.LatencyMs,
		Retryable:    result.Retryable,
	}
	if result.Result == nil {
		return out
	}
	r := result.Result
	out.TaskId = r.TaskID
	out.ContextId = r.ContextID
	out.State = r.State
	out.Text = r.Text
	out.RawTask = r.RawTask
	for _, artifact := range r.Artifacts {
		out.Artifacts = append(out.Artifacts, artifactToV1(artifact))
	}
	if r.State == a2aclient.StateInputRequired || r.State == a2aclient.StateAuthRequired {
		out.Interrupt = &v1.A2AInterrupt{
			Kind:    r.State,
			Message: r.Message,
		}
	}
	if r.State == a2aclient.StateFailed || r.State == a2aclient.StateRejected {
		out.Error = r.Message
	}
	return out
}

func artifactToV1(artifact a2aclient.Artifact) *v1.A2AArtifact {
	out := &v1.A2AArtifact{
		ArtifactId:  artifact.ID,
		Name:        artifact.Name,
		Description: artifact.Description,
	}
	for _, part := range artifact.Parts {
		out.Parts = append(out.Parts, &v1.A2APart{
			Kind:     part.Kind,
			Text:     part.Text,
			Data:     part.Data,
			FileUrl:  part.FileURL,
			FileName: part.FileName,
			MimeType: part.MIMEType,
		})
	}
	return out
}

func invocationToV1(row *model.A2AInvocationM) *v1.A2AInvocation {
	if row == nil {
		return nil
	}
	out := &v1.A2AInvocation{
		InvocationId:  row.InvocationID,
		Agent:         row.AgentName,
		Namespace:     row.Namespace,
		AgentKind:     row.AgentKind,
		Source:        row.Source,
		CallerKind:    row.CallerKind,
		CallerId:      row.CallerID,
		CallerName:    row.CallerName,
		CallerAgent:   row.CallerAgent,
		ParentRunId:   row.ParentRunID,
		TaskId:        row.TaskID,
		ContextId:     row.ContextID,
		State:         row.State,
		Method:        row.Method,
		Transport:     row.Transport,
		AuthType:      row.AuthType,
		Input:         row.Input,
		Output:        row.Output,
		Error:         row.ErrorMessage,
		LatencyMs:     row.LatencyMs,
		CardVersion:   row.CardVersion,
		CardDigest:    row.CardDigest,
		ArtifactCount: row.ArtifactCount,
		Artifacts:     row.ArtifactsJSON,
		Events:        row.EventsJSON,
		Truncated:     row.Truncated,
		RequestId:     row.RequestID,
		CreatedAt:     row.CreatedAt.UnixMilli(),
	}
	if row.CompletedAt != nil {
		out.CompletedAt = row.CompletedAt.UnixMilli()
	}
	return out
}

func previewHeaders(ctx context.Context, auth *v1.A2AAuth) (map[string]string, error) {
	if auth == nil {
		return nil, nil
	}
	switch auth.GetType() {
	case "", "none":
		return nil, nil
	case "apiKey":
		if auth.GetCredential() == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("apiKey credential is required")
		}
		name := auth.GetApiKeyHeader()
		if name == "" {
			name = "X-API-KEY"
		}
		return map[string]string{name: auth.GetCredential()}, nil
	case "bearer":
		if auth.GetCredential() == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("bearer token is required")
		}
		return map[string]string{"Authorization": "Bearer " + auth.GetCredential()}, nil
	case "headers":
		if auth.GetCredential() == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("custom headers are required")
		}
		headers := map[string]string{}
		if err := json.Unmarshal([]byte(auth.GetCredential()), &headers); err != nil {
			return nil, errno.ErrInvalidArgument.WithMessage("custom headers must be a JSON object")
		}
		return headers, nil
	case "oauth2":
		if auth.GetTokenUrl() == "" || auth.GetClientId() == "" || auth.GetClientSecret() == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("OAuth2 needs tokenUrl, clientId and clientSecret")
		}
		cfg := clientcredentials.Config{
			ClientID:     auth.GetClientId(),
			ClientSecret: auth.GetClientSecret(),
			TokenURL:     auth.GetTokenUrl(),
			Scopes:       auth.GetScopes(),
		}
		if auth.GetAudience() != "" {
			cfg.EndpointParams = url.Values{"audience": {auth.GetAudience()}}
		}
		switch auth.GetClientAuth() {
		case "post":
			cfg.AuthStyle = oauth2.AuthStyleInParams
		case "basic":
			cfg.AuthStyle = oauth2.AuthStyleInHeader
		}
		fetcher := netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: true, Timeout: 10 * time.Second, MaxRedirects: 3, Retries: -1,
		})
		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, fetcher.Client())
		token, err := cfg.Token(tokenCtx)
		if err != nil {
			return nil, fmt.Errorf("OAuth2 token request failed: %w", err)
		}
		return map[string]string{"Authorization": "Bearer " + token.AccessToken}, nil
	default:
		return nil, fmt.Errorf("auth type %q is not supported by preview yet", auth.GetType())
	}
}

func cardToA2AStatus(card *a2aclient.Card) *v1.AgentA2AStatus {
	status := &v1.AgentA2AStatus{
		Phase:            "Ready",
		ProtocolVersion:  card.ProtocolVersion,
		Transport:        card.Transport(),
		ResolvedEndpoint: card.Endpoint(),
		CardVersion:      card.Version,
		CardDigest:       card.Digest,
		CardFetchedAt:    time.Now().UnixMilli(),
		Provider:         card.Provider,
		AuthReady:        true,
		InputModes:       append([]string(nil), card.InputModes...),
		OutputModes:      append([]string(nil), card.OutputModes...),
		Capabilities: &v1.A2ACapabilities{
			Streaming:              card.Capabilities.Streaming,
			PushNotifications:      card.Capabilities.PushNotifications,
			ExtendedAgentCard:      card.Capabilities.ExtendedAgentCard,
			StateTransitionHistory: card.Capabilities.StateTransitionHistory,
		},
	}
	for _, skill := range card.Skills {
		status.Skills = append(status.Skills, &v1.A2ASkill{
			Id:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Tags:        append([]string(nil), skill.Tags...),
			Examples:    append([]string(nil), skill.Examples...),
			InputModes:  append([]string(nil), skill.InputModes...),
			OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	for _, ext := range card.Extensions {
		status.Extensions = append(status.Extensions, &v1.A2AExtension{
			Uri: ext.URI, Description: ext.Description, Required: ext.Required,
		})
	}
	if len(card.SecuritySchemes) > 0 {
		status.SecuritySchemes = string(card.SecuritySchemes)
	}
	if len(card.Raw) > 0 {
		status.Card = string(card.Raw)
	}
	return status
}
