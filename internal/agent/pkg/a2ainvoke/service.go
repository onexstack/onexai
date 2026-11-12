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

// Package a2ainvoke orchestrates one outbound A2A call: build the transport,
// wait for a terminal task (or stream it), and record what happened.
package a2ainvoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/store/where"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2aclient"
	"github.com/onexstack/onexai/internal/agent/pkg/datamask"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

const (
	defaultTimeout  = 120 * time.Second
	defaultPoll     = 500 * time.Millisecond
	recordTextLimit = 64 << 10
)

// CredentialReader reads the agent's credential Secret.
type CredentialReader interface {
	A2ACredentials(ctx context.Context, name string) (map[string][]byte, error)
}

// Service invokes external agents and records the calls.
type Service struct {
	Agents      agentstore.AgentStore
	Invocations agentstore.A2AInvocationStore
	Credentials CredentialReader
	Timeout     time.Duration
	PollEvery   time.Duration
}

// New builds a service.
func New(agents agentstore.AgentStore, invocations agentstore.A2AInvocationStore, credentials CredentialReader) *Service {
	return &Service{
		Agents:      agents,
		Invocations: invocations,
		Credentials: credentials,
		Timeout:     defaultTimeout,
		PollEvery:   defaultPoll,
	}
}

// CallRequest is one invocation, from the console, Chat, a tool or the API.
type CallRequest struct {
	Name      string
	Message   string
	ContextID string
	TaskID    string
	Skill     string
	Metadata  map[string]any
	Timeout   time.Duration

	Source      string
	CallerKind  string
	CallerID    string
	CallerName  string
	CallerAgent string
	ParentRunID string
	RootRunID   string
	RequestID   string
}

// CallResult is one completed invocation.
type CallResult struct {
	InvocationID string                  `json:"invocationId"`
	Result       *a2aclient.Result       `json:"result"`
	Events       []a2aclient.StreamEvent `json:"events,omitempty"`
	LatencyMs    int64                   `json:"latencyMs"`
	Retryable    bool                    `json:"retryable"`
}

// Invoke performs one call and waits for a terminal state.
func (s *Service) Invoke(ctx context.Context, req CallRequest) (*CallResult, error) {
	invoker, agent, err := s.invokerFor(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.callTimeout(req, agent))
	defer cancel()

	invocationID := uuid.NewString()
	start := time.Now()
	result, err := invoker.Send(callCtx, s.requestFor(agent, req))
	// A peer may answer "submitted" rather than "working" for an asynchronous
	// task (onexai's own 1.0 surface does). Poll any non-terminal state, but not
	// the two interrupt states: those wait for the caller, not for time.
	if err == nil && result != nil && result.TaskID != "" &&
		!a2aclient.Terminal(result.State) &&
		result.State != a2aclient.StateInputRequired && result.State != a2aclient.StateAuthRequired {
		result, err = s.poll(callCtx, invoker, result)
	}
	latency := time.Since(start).Milliseconds()
	s.record(ctx, req, agent, invoker, "SendMessage", invocationID, result, err, start, latency, nil)
	if err != nil {
		return &CallResult{InvocationID: invocationID, LatencyMs: latency, Retryable: retryable(err)}, err
	}
	return &CallResult{InvocationID: invocationID, Result: result, LatencyMs: latency}, nil
}

// Stream starts one streaming call. The returned channel is closed after the
// terminal event and the invocation has been recorded.
func (s *Service) Stream(ctx context.Context, req CallRequest) (string, <-chan a2aclient.StreamEvent, error) {
	invoker, agent, err := s.invokerFor(ctx, req.Name)
	if err != nil {
		return "", nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.callTimeout(req, agent))
	events, err := invoker.Stream(callCtx, s.requestFor(agent, req))
	if err != nil {
		cancel()
		return "", nil, err
	}

	invocationID := uuid.NewString()
	start := time.Now()
	out := make(chan a2aclient.StreamEvent, 16)
	go func() {
		defer close(out)
		defer cancel()
		var collected []a2aclient.StreamEvent
		var final *a2aclient.Result
		var lastState string
		var streamErr error
		var text strings.Builder
		var artifacts []a2aclient.Artifact
		for event := range events {
			collected = append(collected, event)
			if event.State != "" {
				lastState = event.State
			}
			if event.Type == "error" && event.Message != "" {
				streamErr = errors.New(event.Message)
			}
			switch event.Type {
			case "delta":
				text.WriteString(event.Text)
			case "artifact":
				if event.Artifact != nil {
					artifacts = append(artifacts, *event.Artifact)
					for _, part := range event.Artifact.Parts {
						if part.Kind == "text" {
							text.WriteString(part.Text)
						}
					}
				}
			case "status":
				if event.Final {
					final = &a2aclient.Result{
						State: event.State, TaskID: event.TaskID,
						ContextID: event.ContextID, Message: event.Message,
						// A whole task snapshot carries its text and artifacts
						// on the terminal event itself. Record them as the
						// result, or the invocation row would show a successful
						// call with no output and no artifacts.
						Text:      event.Text,
						Artifacts: event.Artifacts,
					}
					// Forward what the stream accumulated so a caller that treats
					// this event as the terminal one has the whole answer, even
					// when the peer sent it as deltas or artifact updates.
					if event.Text == "" {
						event.Text = text.String()
					}
				}
			case "interrupt":
				// A pause carries the peer's question in Message, not Text. It
				// is the record's useful output: "what did the peer ask for"
				// is the answer a reader of the invocation list needs.
				final = &a2aclient.Result{
					State: event.State, TaskID: event.TaskID,
					ContextID: event.ContextID, Message: event.Message,
				}
			}
			select {
			case out <- event:
			case <-callCtx.Done():
			}
		}
		if final == nil {
			final = &a2aclient.Result{State: lastState}
		}
		// A stop (or timeout) before the peer reported any state would otherwise
		// leave the record with an empty state, which reads as "never ran".
		if final.State == "" {
			switch {
			case errors.Is(callCtx.Err(), context.Canceled):
				final.State = a2aclient.StateCanceled
			case errors.Is(callCtx.Err(), context.DeadlineExceeded):
				final.State = a2aclient.StateFailed
				if streamErr == nil {
					streamErr = callCtx.Err()
				}
			}
		}
		if final.Text == "" {
			final.Text = text.String()
		}
		// A pause has no answer text; its question is what the record should
		// show. Only for the two interrupt states — on an error the message is
		// a failure description, not output.
		if final.Text == "" && (final.State == a2aclient.StateInputRequired || final.State == a2aclient.StateAuthRequired) {
			final.Text = final.Message
		}
		if final.Artifacts == nil {
			final.Artifacts = artifacts
		}
		latency := time.Since(start).Milliseconds()
		s.record(ctx, req, agent, invoker, "SendStreamingMessage", invocationID, final, streamErr, start, latency, collected)
	}()
	return invocationID, out, nil
}

// callTimeout answers how long this call may take. The caller's explicit value
// wins; then the agent's own execution.a2a.timeoutSeconds, which is the setting
// an administrator configured for this peer; then the service default.
//
// Reading the agent's value here rather than at each entry point is what keeps
// the debug panel, the a2a_invoke tool and Chat from disagreeing about the same
// agent's deadline — the three of them call this service, and only one of them
// used to pass a timeout at all.
func (s *Service) callTimeout(req CallRequest, agent *model.AgentM) time.Duration {
	if req.Timeout > 0 {
		return req.Timeout
	}
	if agent != nil && agent.ExecutionJSON != "" {
		var execution model.AgentExecutionM
		if json.Unmarshal([]byte(agent.ExecutionJSON), &execution) == nil &&
			execution.A2A != nil && execution.A2A.TimeoutSeconds > 0 {
			return time.Duration(execution.A2A.TimeoutSeconds) * time.Second
		}
	}
	if s.Timeout > 0 {
		return s.Timeout
	}
	return defaultTimeout
}

// GetTask fetches one remote task snapshot.
func (s *Service) GetTask(ctx context.Context, name, taskID string) (*a2aclient.Result, error) {
	if taskID == "" {
		return nil, errors.New("taskId is required")
	}
	invoker, _, err := s.invokerFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return invoker.GetTask(ctx, taskID)
}

// Cancel cancels one remote task.
func (s *Service) Cancel(ctx context.Context, name, taskID string) error {
	if taskID == "" {
		return errors.New("taskId is required")
	}
	invoker, _, err := s.invokerFor(ctx, name)
	if err != nil {
		return err
	}
	return invoker.Cancel(ctx, taskID)
}

// List returns the invocation records for one agent.
func (s *Service) List(ctx context.Context, name string, offset, limit int) (int64, []*model.A2AInvocationM, error) {
	if s.Invocations == nil {
		return 0, nil, errors.New("invocation storage is not configured")
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	options := where.NewWhere().F("agent_name", name).O(offset).L(limit)
	return s.Invocations.List(ctx, options)
}

// Get returns one invocation record.
func (s *Service) Get(ctx context.Context, name, invocationID string) (*model.A2AInvocationM, error) {
	if s.Invocations == nil {
		return nil, errors.New("invocation storage is not configured")
	}
	return s.Invocations.Get(ctx, where.NewWhere().F("agent_name", name).F("invocation_id", invocationID))
}

func (s *Service) poll(ctx context.Context, invoker *a2aclient.Invoker, result *a2aclient.Result) (*a2aclient.Result, error) {
	ticker := time.NewTicker(s.pollEvery())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-ticker.C:
			next, err := invoker.GetTask(ctx, result.TaskID)
			if err != nil {
				return result, err
			}
			if next == nil {
				continue
			}
			if next.Text == "" {
				next.Text = result.Text
			}
			if next.Artifacts == nil {
				next.Artifacts = result.Artifacts
			}
			result = next
			if a2aclient.Terminal(result.State) || result.State == a2aclient.StateInputRequired ||
				result.State == a2aclient.StateAuthRequired {
				return result, nil
			}
		}
	}
}

// requestFor merges the agent's default metadata with the call's own. onexai's
// A2A surface uses the `agents` key to pick the agent set, so a registered
// self-endpoint is unusable without it.
func (s *Service) requestFor(agent *model.AgentM, req CallRequest) a2aclient.Request {
	metadata := map[string]any{}
	var execution model.AgentExecutionM
	if agent.ExecutionJSON != "" && json.Unmarshal([]byte(agent.ExecutionJSON), &execution) == nil &&
		execution.A2A != nil && execution.A2A.MetadataJSON != "" {
		_ = json.Unmarshal([]byte(execution.A2A.MetadataJSON), &metadata)
	}
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return a2aclient.Request{
		Message: req.Message, ContextID: req.ContextID, TaskID: req.TaskID,
		Skill: req.Skill, Metadata: metadata,
	}
}

func (s *Service) pollEvery() time.Duration {
	if s.PollEvery > 0 {
		return s.PollEvery
	}
	return defaultPoll
}

// invokerFor loads the agent, its card status and its credential, then builds
// the wire client. It is the single place that turns a stored agent into an
// outbound call.
func (s *Service) invokerFor(ctx context.Context, name string) (*a2aclient.Invoker, *model.AgentM, error) {
	if s.Agents == nil {
		return nil, nil, errors.New("agent storage is not configured")
	}
	agent, err := s.Agents.Get(ctx, where.F("name", name))
	if err != nil {
		return nil, nil, fmt.Errorf("agent %q: %w", name, err)
	}
	var execution model.AgentExecutionM
	if agent.ExecutionJSON == "" || json.Unmarshal([]byte(agent.ExecutionJSON), &execution) != nil ||
		execution.A2A == nil || execution.Mode != "a2a" {
		return nil, nil, fmt.Errorf("agent %q is not an A2A execution", name)
	}
	var status model.AgentA2AStatusM
	if agent.A2AStatusJSON == "" || json.Unmarshal([]byte(agent.A2AStatusJSON), &status) != nil {
		return nil, nil, fmt.Errorf("agent %q has no synced AgentCard", name)
	}
	endpoint := status.ResolvedEndpoint
	// A card may publish a path-relative interface (onexai does when no public
	// URL is configured). The stored endpoint override is the absolute base.
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = execution.A2A.Endpoint
	}
	var clientConfigHTTP *http.Client
	if len(execution.A2A.ClientConfig) > 0 {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal(execution.A2A.ClientConfig, cfg); err != nil {
			return nil, nil, fmt.Errorf("agent %q clientConfig: %w", name, err)
		}
		resolved, err := clientconfig.Resolve(*cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("agent %q endpoint: %w", name, err)
		}
		endpoint = resolved.URL
		clientConfigHTTP, err = clientconfig.HTTPClient(*cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("agent %q http client: %w", name, err)
		}
	}
	if endpoint == "" {
		return nil, nil, fmt.Errorf("agent %q has no A2A endpoint", name)
	}

	secret := map[string][]byte(nil)
	if s.Credentials != nil {
		secret, err = s.Credentials.A2ACredentials(ctx, name)
		if err != nil {
			return nil, nil, err
		}
	}
	headers, authErr := headersForAuth(ctx, execution.A2A.Auth, secret)
	if authErr != nil {
		return nil, nil, authErr
	}

	fetcher := netguard.NewFetcher(netguard.Options{
		AllowPrivateHosts: true,
		Timeout:           s.Timeout,
		MaxRedirects:      3,
		Retries:           -1,
	})
	client := fetcher.Client()
	if clientConfigHTTP != nil {
		// The ClientConfig carries the resolved Service/TLS inputs. It replaces
		// the default transport for this call; the A2A fetcher already allows
		// in-cluster addresses, so the destination policy is unchanged.
		client = clientConfigHTTP
	}
	if len(headers) > 0 {
		client = a2aclient.ClientWithHeaders(client, headers)
	}
	if execution.A2A.Auth != nil && execution.A2A.Auth.Type == "aksk" {
		accessKey := string(secret["accessKey"])
		secretKey := string(secret["secretKey"])
		if accessKey == "" || secretKey == "" {
			return nil, nil, errors.New("AKSK credentials are missing")
		}
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		client.Transport = a2aclient.NewAKSKTransport(transport, accessKey, secretKey)
	}
	return a2aclient.NewInvoker(endpoint, status.ProtocolVersion, status.Transport, client, nil), agent, nil
}

func (s *Service) record(
	ctx context.Context, req CallRequest, agent *model.AgentM, invoker *a2aclient.Invoker,
	method, invocationID string, result *a2aclient.Result, callErr error, start time.Time, latency int64,
	events []a2aclient.StreamEvent,
) {
	if s.Invocations == nil {
		return
	}
	row := &model.A2AInvocationM{
		InvocationID: invocationID,
		RequestID:    req.RequestID,
		AgentName:    agent.Name,
		Namespace:    agent.Namespace,
		AgentKind:    agent.Kind,
		Source:       cmpOr(req.Source, "api"),
		CallerKind:   req.CallerKind,
		CallerID:     req.CallerID,
		CallerName:   req.CallerName,
		CallerAgent:  req.CallerAgent,
		ParentRunID:  req.ParentRunID,
		RootRunID:    req.RootRunID,
		Method:       method,
		Input:        datamask.Mask(truncate(req.Message, recordTextLimit)),
		LatencyMs:    latency,
		CreatedAt:    start,
	}
	var execution model.AgentExecutionM
	_ = json.Unmarshal([]byte(agent.ExecutionJSON), &execution)
	if execution.A2A != nil && execution.A2A.Auth != nil {
		row.AuthType = execution.A2A.Auth.Type
	}
	var card model.AgentA2AStatusM
	_ = json.Unmarshal([]byte(agent.A2AStatusJSON), &card)
	row.Transport = card.Transport
	row.CardVersion = card.CardVersion
	row.CardDigest = card.CardDigest
	if result != nil {
		row.TaskID = result.TaskID
		row.ContextID = result.ContextID
		row.State = result.State
		row.Output = datamask.Mask(truncate(result.Text, recordTextLimit))
		row.ArtifactCount = int32(len(result.Artifacts))
		if encoded, err := json.Marshal(result.Artifacts); err == nil {
			row.ArtifactsJSON = truncate(string(encoded), recordTextLimit)
		}
		row.CardVersion = ""
	}
	if len(events) > 0 {
		if encoded, err := json.Marshal(events); err == nil {
			row.EventsJSON = truncate(string(encoded), recordTextLimit)
		}
	}
	if callErr != nil {
		row.ErrorMessage = datamask.Mask(truncate(callErr.Error(), recordTextLimit))
		if row.State == "" {
			row.State = "failed"
		}
	}
	completed := time.Now()
	row.CompletedAt = &completed
	// The record is a side effect of the call; a full table must not turn a
	// successful answer into a failure. It also must survive the request
	// context being cancelled after the answer was produced.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.Invocations.Create(writeCtx, row)
}

func headersForAuth(ctx context.Context, auth *model.A2AAuthM, secret map[string][]byte) (map[string]string, error) {
	if auth == nil || auth.Type == "" || auth.Type == "none" {
		return nil, nil
	}
	headers := map[string]string{}
	switch auth.Type {
	case "apiKey":
		value := string(secret["apiKey"])
		if value == "" {
			return nil, errors.New("apiKey credential is missing")
		}
		name := auth.APIKeyHeader
		if name == "" {
			name = "X-API-KEY"
		}
		headers[name] = value
	case "bearer":
		value := string(secret["token"])
		if value == "" {
			return nil, errors.New("bearer token is missing")
		}
		headers["Authorization"] = "Bearer " + value
	case "headers":
		raw := secret["headers"]
		if len(raw) == 0 {
			return nil, errors.New("custom headers credential is missing")
		}
		if err := json.Unmarshal(raw, &headers); err != nil {
			return nil, fmt.Errorf("custom headers credential: %w", err)
		}
	case "oauth2":
		clientID := string(secret["clientID"])
		if clientID == "" {
			clientID = auth.ClientID
		}
		clientSecret := string(secret["clientSecret"])
		if auth.TokenURL == "" || clientID == "" || clientSecret == "" {
			return nil, errors.New("OAuth2 needs tokenUrl, clientID and clientSecret")
		}
		cfg := clientcredentials.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     auth.TokenURL,
			Scopes:       auth.Scopes,
		}
		if auth.Audience != "" {
			cfg.EndpointParams = url.Values{"audience": {auth.Audience}}
		}
		switch auth.ClientAuth {
		case "post":
			cfg.AuthStyle = oauth2.AuthStyleInParams
		case "basic":
			cfg.AuthStyle = oauth2.AuthStyleInHeader
		}
		fetcher := netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: true, Timeout: defaultTimeout, MaxRedirects: 3, Retries: -1,
		})
		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, fetcher.Client())
		token, err := cfg.Token(tokenCtx)
		if err != nil {
			return nil, fmt.Errorf("OAuth2 token request failed: %w", err)
		}
		headers["Authorization"] = "Bearer " + token.AccessToken
	case "runtime":
		value := string(secret["token"])
		if value == "" {
			return nil, errors.New("runtime credential is missing")
		}
		headers["Authorization"] = "Bearer " + value
	case "aksk":
		// The signature is per request, so it is installed as a transport below
		// rather than as a static header here.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown auth type %q", auth.Type)
	}
	return headers, nil
}

func retryable(err error) bool {
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func cmpOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
