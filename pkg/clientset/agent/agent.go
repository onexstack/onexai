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

// Package agent provides a typed client for the oai-agent-apiserver service.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"resty.dev/v3"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ErrBusy reports that oai-agent-apiserver refused a request because the conversation
// it names already has a run in flight (HTTP 409 Conflict).
//
// It is a sentinel rather than a message match because the distinction matters
// to the caller: this is the one failure that means "try again shortly" rather
// than "something is broken", and the server's explanation for it is a
// user-facing sentence in Chinese that no code should be keying on.
var ErrBusy = errors.New("agent: the conversation is already busy")

// ErrNotPending reports that an approval named a pause the run is no longer
// waiting on: already answered, expired, or an earlier pause of the same run
// (HTTP 410 Gone).
//
// It is a sentinel for the same reason ErrBusy is, and separate from it because
// the two say different things to a person: this one means the request is stale,
// the other means try again shortly. Both are the server's explanation in
// Chinese, which no code should be keying on.
var ErrNotPending = errors.New("agent: the approval is no longer pending")

// Interface defines the operations for the oai-agent-apiserver client.
type Interface interface {
	// ChatCompletions performs a synchronous OpenAI-compatible completion.
	ChatCompletions(ctx context.Context, req *v1.ChatCompletionRequest) (*v1.ChatCompletionResponse, error)
	// ChatCompletionsStream performs a streaming completion, invoking onChunk for
	// each SSE delta chunk. It returns nil once the stream terminates normally.
	ChatCompletionsStream(ctx context.Context, req *v1.ChatCompletionRequest, onChunk func(*v1.ChatCompletionStreamResponse) error) error
	// AgentAskStream performs a backend-neutral agent run, invoking onEvent for
	// each streamed event (answer, reasoning, tool steps, interrupts). It returns
	// nil once the run terminates.
	AgentAskStream(ctx context.Context, req *v1.AgentAskRequest, onEvent func(*v1.AgentStreamEvent) error) error
	// AgentApproveStream answers a run paused on an approval interrupt and
	// streams the run's continuation, invoking onEvent for each streamed event.
	// It returns nil once the resumed run terminates, which may be at another
	// pause rather than at an answer.
	AgentApproveStream(ctx context.Context, req *v1.AgentApproveRequest, onEvent func(*v1.AgentStreamEvent) error) error
	// Stop cancels an in-flight agent run.
	Stop(ctx context.Context, runID string) error
	// ListAgent returns all agents.
	ListAgent(ctx context.Context) ([]*v1.Agent, error)
	// GetAgent returns a single agent by name.
	GetAgent(ctx context.Context, name string) (*v1.Agent, error)
	// ListSkill returns all skills.
	ListSkill(ctx context.Context) ([]*v1.Skill, error)
	// ContextSkills returns the skills an agent has exposed as commands.
	ContextSkills(ctx context.Context, agentName string) ([]*v1.ContextSkill, error)
	// ContextCustomCommands returns the user-defined slash commands an agent
	// exposes, with the parameter declarations the chat service needs to build
	// a cobra command.
	ContextCustomCommands(ctx context.Context, agentName string) ([]*v1.ContextCustomCommand, error)
	// ListCustomCommand returns every custom command definition. The MCP
	// bridge re-reads it periodically (mcp.refresh-interval) to decide which
	// tools exist; the per-caller filter still narrows them by binding.
	ListCustomCommand(ctx context.Context) ([]*v1.CustomCommand, error)
	// InvokeCustomCommand runs one user-defined command.
	InvokeCustomCommand(
		ctx context.Context, name string, req *v1.InvokeCustomCommandRequest,
	) (*v1.InvokeCustomCommandResponse, error)
	// ApproveCustomCommand answers a parked tool invocation.
	ApproveCustomCommand(
		ctx context.Context, name string, req *v1.ApproveCustomCommandRequest,
	) (*v1.ApproveCustomCommandResponse, error)
	// ListMCP returns all MCP servers.
	ListMCP(ctx context.Context) ([]*v1.MCP, error)
	// ListKnowledgeBase returns all knowledge bases.
	ListKnowledgeBase(ctx context.Context) ([]*v1.KnowledgeBase, error)
	// ClearSession resets the conversation memory for a session.
	ClearSession(ctx context.Context, sessionID string) error
	// CheckAgentAccess asks whether a subject may use an agent.
	CheckAgentAccess(ctx context.Context, subjectID, agentName string) (bool, string, error)
	// UploadAsset stores image bytes on the backend and returns the reference
	// that names them.
	//
	// The bytes go to the backend rather than staying here because the backend
	// owns the store: it is the process configured with a media driver, and the
	// one that resolves a reference when a model needs the image. This service
	// holds no bytes beyond the request that received them.
	UploadAsset(ctx context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error)
	// FetchAssetContent returns an asset's bytes and media type.
	//
	// It is the counterpart of UploadAsset: the backend owns the store, so a
	// caller that needs the image itself asks for it rather than learning which
	// driver is configured.
	FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error)
	// UserGroupCodes returns the enabled user groups an identity belongs to.
	//
	// Chat uses it to turn a person into the group subjects its role rules are
	// written against: the membership lives in the agent schema, and this is the
	// signed read that lets the chat service see it without opening the agent's
	// database.
	UserGroupCodes(ctx context.Context, identity string) ([]string, error)
}

// AgentAccessRequest is the body of POST /v1/authz/decisions.
//
// It names the subject and the agent rather than a full resource descriptor
// because those are the only two things the caller has an opinion about; the
// verb is always "read" for this use, and a caller that needed another verb
// would be asking a different question.
type AgentAccessRequest struct {
	// SubjectID is the identity the agent service authorizes as.
	SubjectID string `json:"subjectID"`
	// AgentName is the agent being asked about.
	AgentName string `json:"agentName"`
}

// AgentAccessResponse is the answer. Reason is written for a person: it is shown
// in the refusal the speaker reads.
type AgentAccessResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Client implements Interface over HTTP using resty.
type Client struct {
	client *resty.Client
	// signer, when non-nil, signs every request with the AK/SK pair
	// oai-agent-apiserver requires on /v1.
	signer *signer
}

var _ Interface = (*Client)(nil)

// NewForConfig creates a Client configured with the provided options. The
// options' Endpoint must point to the oai-agent-apiserver base URL.
//
// The client is unsigned: it exists for callers that talk to a server with
// verification bypassed (local development) and for tests. A production
// deployment wants NewWithSigning.
func NewForConfig(opts *genericoptions.RestyOptions) *Client {
	return &Client{client: opts.WithTrace().NewClient()}
}

// NewWithSigning creates a Client that signs every request with the given AK/SK
// pair, which is what oai-agent-apiserver's inbound authentication requires. An empty
// key or secret falls back to an unsigned client, so a half-configured
// deployment fails loudly against the server rather than silently sending
// garbage credentials.
func NewWithSigning(opts *genericoptions.RestyOptions, apiKey, secretKey string) *Client {
	return &Client{client: opts.WithTrace().NewClient(), signer: newSigner(apiKey, secretKey)}
}

// ChatCompletions performs a synchronous completion.
func (c *Client) ChatCompletions(ctx context.Context, req *v1.ChatCompletionRequest) (*v1.ChatCompletionResponse, error) {
	req.Stream = false
	const path = "/v1/chat/completions"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, false)
	if err != nil {
		return nil, err
	}
	var resp v1.ChatCompletionResponse
	r, err := rq.SetResult(&resp).Post(path)
	if err != nil {
		return nil, fmt.Errorf("agent chat completions: %w", err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent returned %d: %s", r.StatusCode(), r.String())
	}
	return &resp, nil
}

// ChatCompletionsStream performs a streaming completion.
func (c *Client) ChatCompletionsStream(
	ctx context.Context,
	req *v1.ChatCompletionRequest,
	onChunk func(*v1.ChatCompletionStreamResponse) error,
) error {
	req.Stream = true
	const path = "/v1/chat/completions"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, true)
	if err != nil {
		return err
	}
	resp, err := rq.Post(path)
	if err != nil {
		return fmt.Errorf("agent chat completions stream: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("agent returned %d", resp.StatusCode())
	}
	defer resp.RawResponse.Body.Close()

	return scanSSE(resp.RawResponse.Body, onChunk)
}

// AgentAskStream performs a backend-neutral agent run over SSE.
func (c *Client) AgentAskStream(
	ctx context.Context,
	req *v1.AgentAskRequest,
	onEvent func(*v1.AgentStreamEvent) error,
) error {
	const path = "/v1/agent/ask"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, true)
	if err != nil {
		return err
	}
	resp, err := rq.Post(path)
	if err != nil {
		return fmt.Errorf("agent agent ask stream: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		// Read and close the body once, for every refusal. A streamed response
		// has nobody downstream to consume it, so a branch that returns without
		// reading leaks the connection — and the bytes it skips are the server's
		// explanation, which is the only thing that tells a 403 from a 500.
		httpErr := newHTTPError(resp)
		if resp.StatusCode() == http.StatusConflict {
			// The run this request wanted to start cannot begin yet: the server is
			// still on the previous turn for this conversation. Checked ahead of the
			// generic branch because it is the one failure the caller acts on
			// differently rather than merely reports.
			return fmt.Errorf("%w: %w", ErrBusy, httpErr)
		}
		return httpErr
	}
	defer resp.RawResponse.Body.Close()

	return scanAgentSSE(resp.RawResponse.Body, onEvent)
}

// AgentApproveStream answers a paused agent run and streams its continuation,
// which is the same shape as AgentAskStream because answering resumes the run:
// the tools it paused on execute now, and what the model says and does next
// reaches the caller the same way the first half did.
func (c *Client) AgentApproveStream(
	ctx context.Context,
	req *v1.AgentApproveRequest,
	onEvent func(*v1.AgentStreamEvent) error,
) error {
	const path = "/v1/agent/approve"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, true)
	if err != nil {
		return err
	}
	resp, err := rq.Post(path)
	if err != nil {
		return fmt.Errorf("agent agent approve stream: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		// Read and close once, for the same reasons AgentAskStream does: the two
		// named refusals below are not the only ones that carry a body, and an
		// unread streamed body leaks.
		httpErr := newHTTPError(resp)
		switch resp.StatusCode() {
		case http.StatusGone:
			// The pause is no longer waiting: already answered, expired, or an
			// earlier pause of the same run.
			return fmt.Errorf("%w: %w", ErrNotPending, httpErr)
		case http.StatusConflict:
			// The conversation is still on the run this answer belongs to.
			return fmt.Errorf("%w: %w", ErrBusy, httpErr)
		}
		return httpErr
	}
	defer resp.RawResponse.Body.Close()

	return scanAgentSSE(resp.RawResponse.Body, onEvent)
}

// Stop cancels an in-flight agent run.
func (c *Client) Stop(ctx context.Context, runID string) error {
	const path = "/v1/agent/stop"
	rq, err := c.prepare(ctx, http.MethodPost, path, &v1.AgentStopRequest{RunID: runID}, false)
	if err != nil {
		return err
	}
	r, err := rq.Post(path)
	if err != nil {
		return fmt.Errorf("agent stop: %w", err)
	}
	if r.IsStatusFailure() {
		return fmt.Errorf("agent returned %d: %s", r.StatusCode(), r.String())
	}
	return nil
}

// ListAgent returns all agents.
func (c *Client) ListAgent(ctx context.Context) ([]*v1.Agent, error) {
	var resp v1.ListAgentResponse
	if err := c.get(ctx, "/v1/agent/agents", &resp); err != nil {
		return nil, err
	}
	return resp.GetAgents(), nil
}

// GetAgent returns a single agent by name.
func (c *Client) GetAgent(ctx context.Context, name string) (*v1.Agent, error) {
	var resp v1.GetAgentResponse
	if err := c.get(ctx, "/v1/agent/agents/"+name, &resp); err != nil {
		return nil, err
	}
	return resp.GetAgent(), nil
}

// ListSkill returns all skills.
func (c *Client) ListSkill(ctx context.Context) ([]*v1.Skill, error) {
	var resp v1.ListSkillResponse
	if err := c.get(ctx, "/v1/agent/skills", &resp); err != nil {
		return nil, err
	}
	return resp.GetSkills(), nil
}

// ContextSkills returns the skills an agent has exposed as commands.
//
// It is not ListSkill with a filter: that returns the repository records, and
// a record can hold several skills. What a person types after the slash is the
// skill's own name, which only the agent service can enumerate.
func (c *Client) ContextSkills(ctx context.Context, agentName string) ([]*v1.ContextSkill, error) {
	var resp v1.ListContextSkillResponse
	if err := c.get(ctx, "/v1/agent/agents/"+agentName+"/context-skills", &resp); err != nil {
		return nil, err
	}
	return resp.GetSkills(), nil
}

// ContextCustomCommands returns the user-defined commands an agent exposes.
func (c *Client) ContextCustomCommands(
	ctx context.Context, agentName string,
) ([]*v1.ContextCustomCommand, error) {
	var resp v1.ListContextCustomCommandResponse
	if err := c.get(ctx, "/v1/agent/agents/"+agentName+"/context-commands", &resp); err != nil {
		return nil, err
	}
	return resp.GetCustomCommands(), nil
}

// ApproveCustomCommand answers a parked tool invocation.
func (c *Client) ApproveCustomCommand(
	ctx context.Context, name string, req *v1.ApproveCustomCommandRequest,
) (*v1.ApproveCustomCommandResponse, error) {
	path := "/v1/agent/customcommands/" + name + "/approve"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, false)
	if err != nil {
		return nil, err
	}
	var out v1.ApproveCustomCommandResponse
	r, err := rq.SetResult(&out).Post(path)
	if err != nil {
		return nil, fmt.Errorf("agent approve custom command %q: %w", name, err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent approve custom command %q returned %d: %s", name, r.StatusCode(), r.String())
	}
	return &out, nil
}

// ListCustomCommand returns every custom command definition.
func (c *Client) ListCustomCommand(ctx context.Context) ([]*v1.CustomCommand, error) {
	var resp v1.ListCustomCommandResponse
	if err := c.get(ctx, "/v1/agent/customcommands", &resp); err != nil {
		return nil, err
	}
	return resp.GetCustomCommands(), nil
}

// InvokeCustomCommand runs one user-defined command.
//
// The body goes through prepare, which marshals it once and signs those very
// bytes; setting it afterwards would sign an empty body and send another.
func (c *Client) InvokeCustomCommand(
	ctx context.Context, name string, req *v1.InvokeCustomCommandRequest,
) (*v1.InvokeCustomCommandResponse, error) {
	path := "/v1/agent/customcommands/" + name + "/invoke"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, false)
	if err != nil {
		return nil, err
	}
	var out v1.InvokeCustomCommandResponse
	r, err := rq.SetResult(&out).Post(path)
	if err != nil {
		return nil, fmt.Errorf("agent invoke custom command %q: %w", name, err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent invoke custom command %q returned %d: %s", name, r.StatusCode(), r.String())
	}
	return &out, nil
}

// ListMCP returns all MCP servers.
func (c *Client) ListMCP(ctx context.Context) ([]*v1.MCP, error) {
	var resp v1.ListMCPResponse
	if err := c.get(ctx, "/v1/agent/mcpservers", &resp); err != nil {
		return nil, err
	}
	return resp.GetMcps(), nil
}

// ListKnowledgeBase returns all knowledge bases.
func (c *Client) ListKnowledgeBase(ctx context.Context) ([]*v1.KnowledgeBase, error) {
	var resp v1.ListKnowledgeBaseResponse
	if err := c.get(ctx, "/v1/agent/knowledgebases", &resp); err != nil {
		return nil, err
	}
	return resp.GetKnowledgeBases(), nil
}

// ClearSession resets the conversation memory for a session.
func (c *Client) ClearSession(ctx context.Context, sessionID string) error {
	path := "/v1/chat/sessions/" + sessionID
	rq, err := c.prepare(ctx, http.MethodDelete, path, nil, false)
	if err != nil {
		return err
	}
	r, err := rq.Delete(path)
	if err != nil {
		return fmt.Errorf("agent clear session: %w", err)
	}
	if r.IsStatusFailure() {
		return fmt.Errorf("agent returned %d: %s", r.StatusCode(), r.String())
	}
	return nil
}

// CheckAgentAccess asks the agent service whether subjectID may use agentName.
//
// # Why this asks rather than decides
//
// The authorization rules live with the agent service, and it is the only thing
// that evaluates them. Reimplementing the check here would be a second answer to
// the same question, and the two would drift — which is the failure mode the
// agent service's own design notes call out. A caller that cannot reach it gets
// an error, not a default: silently allowing would grant access nobody approved,
// and silently denying would refuse one that was.
func (c *Client) CheckAgentAccess(ctx context.Context, subjectID, agentName string) (bool, string, error) {
	const path = "/v1/authz/decisions"
	// The body goes through prepare, which marshals it once and signs those very
	// bytes. Setting it afterwards would sign one body (the empty one) and send
	// another, and the server — which recomputes over what it receives — would
	// answer 401 "signature verification failed".
	rq, err := c.prepare(ctx, http.MethodPost, path, AgentAccessRequest{SubjectID: subjectID, AgentName: agentName}, false)
	if err != nil {
		return false, "", err
	}
	var out AgentAccessResponse
	r, err := rq.SetResult(&out).Post(path)
	if err != nil {
		return false, "", fmt.Errorf("agent authz decision: %w", err)
	}
	if r.IsStatusFailure() {
		return false, "", fmt.Errorf("agent authz decision returned %d: %s", r.StatusCode(), r.String())
	}
	return out.Allowed, out.Reason, nil
}

// UserGroupCodes returns the enabled user groups an identity belongs to.
func (c *Client) UserGroupCodes(ctx context.Context, identity string) ([]string, error) {
	if identity == "" {
		return nil, nil
	}
	const path = "/v1/agent/usergroup-memberships"
	rq, err := c.prepare(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Codes []string `json:"codes"`
	}
	r, err := rq.SetQueryParam("identity", identity).SetResult(&out).Get(path)
	if err != nil {
		return nil, fmt.Errorf("agent user group memberships: %w", err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent user group memberships returned %d: %s", r.StatusCode(), r.String())
	}
	return out.Codes, nil
}

// FetchAssetContent returns an asset's bytes and media type.
func (c *Client) FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error) {
	if assetID == "" {
		return nil, "", fmt.Errorf("agent fetch asset: no asset id")
	}
	path := "/v1/assets/" + url.PathEscape(assetID) + "/content"
	rq, err := c.prepare(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return nil, "", err
	}
	r, err := rq.Get(path)
	if err != nil {
		return nil, "", fmt.Errorf("agent fetch asset content: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, "", newHTTPError(r)
	}
	// The streamed body is the image itself, so the response is read rather than
	// parsed; the media type is what the server sniffed at upload time, not
	// anything the caller declared.
	defer r.RawResponse.Body.Close()
	data, err := io.ReadAll(r.RawResponse.Body)
	if err != nil {
		return nil, "", fmt.Errorf("agent fetch asset content: read body: %w", err)
	}
	return data, r.Header().Get("Content-Type"), nil
}

// UploadAsset stores image bytes on the backend.
func (c *Client) UploadAsset(ctx context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error) {
	const path = "/v1/assets"
	rq, err := c.prepare(ctx, http.MethodPost, path, req, false)
	if err != nil {
		return nil, err
	}
	var resp v1.CreateAssetResponse
	r, err := rq.SetResult(&resp).Post(path)
	if err != nil {
		return nil, fmt.Errorf("agent upload asset: %w", err)
	}
	if r.IsStatusFailure() {
		return nil, fmt.Errorf("agent upload asset returned %d: %s", r.StatusCode(), r.String())
	}
	return resp.GetAsset(), nil
}

// get performs a GET request and decodes the JSON response into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	rq, err := c.prepare(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return err
	}
	r, err := rq.SetResult(out).Get(path)
	if err != nil {
		return fmt.Errorf("agent get %s: %w", path, err)
	}
	if r.IsStatusFailure() {
		// Keep the typed error so callers can tell a missing object (404) from
		// a refusal or an outage, instead of matching on the status string.
		return fmt.Errorf("agent get %s: %w", path, newHTTPError(r))
	}
	return nil
}

// scanSSE reads an SSE stream and invokes onChunk for each data payload. It
// stops at the [DONE] sentinel and returns the first onChunk error or any
// scanner error.
func scanSSE(body io.Reader, onChunk func(*v1.ChatCompletionStreamResponse) error) error {
	return scanSSELines(body, onChunk)
}
