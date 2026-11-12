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

// Package fake provides an in-memory mock of the oai-agent-apiserver client.
package fake

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Client is an in-memory agent.Interface used in tests.
type Client struct {
	Agents []*v1.Agent
	Skills []*v1.Skill
	// ContextSkillMap holds the per-agent skill commands ContextSkills answers
	// with, keyed by agent name.
	ContextSkillMap map[string][]*v1.ContextSkill
	// ContextCommandMap holds the per-agent custom commands ContextCustomCommands
	// answers with, keyed by agent name.
	ContextCommandMap map[string][]*v1.ContextCustomCommand
	// CustomCommands is what ListCustomCommand answers with.
	CustomCommands []*v1.CustomCommand
	// InvocationResponses holds the answer InvokeCustomCommand gives, keyed by
	// command name. A missing entry answers a successful prompt invocation.
	InvocationResponses map[string]*v1.InvokeCustomCommandResponse
	// InvocationErr, when set, is returned from every invocation.
	InvocationErr error
	// InvocationRequests records what was invoked, so a test can assert the
	// params that crossed the wire.
	InvocationRequests []*v1.InvokeCustomCommandRequest
	// ApprovalRequests records approval answers.
	ApprovalRequests []*v1.ApproveCustomCommandRequest
	MCPs             []*v1.MCP
	KnowledgeBases   []*v1.KnowledgeBase
	Completion       *v1.ChatCompletionResponse
	StreamChunks     []*v1.ChatCompletionStreamResponse
	StreamErr        error
	AgentEvents      []*v1.AgentStreamEvent
	AgentStreamErr   error
	ApproveCalls     []ApproveCall
	ApproveEvents    []*v1.AgentStreamEvent
	StopCalls        []string
	ClearSessionIDs  []string
	// AccessChecks records each authorization question, and AccessAllowed /
	// AccessReason are what it answers with. The zero value answers "not
	// allowed", which is the safe default for a test that did not think about
	// it: a run admitted by accident is a test that proves less than it looks
	// like it does.
	AccessChecks  []AccessCheck
	AccessAllowed bool
	AccessReason  string
	// Assets is what UploadAsset hands back, keyed by nothing in particular:
	// the fake returns one row so a caller has an id to pass on. When it is
	// empty the fake answers with a row built from the request, so a test that
	// asserts on what it uploaded sees its own values rather than a constant.
	Assets []*v1.Asset
	// AssetContent is what FetchAssetContent answers with, keyed by asset id.
	AssetContent map[string][]byte
	// AssetMimeType is the media type FetchAssetContent reports. Empty means
	// "image/png". It is configurable because the real client reports whatever
	// the server sniffed, and a test about content types needs to move that.
	AssetMimeType string
	// UploadedRecords the upload requests, so a test can assert what was sent.
	Uploaded []*v1.CreateAssetRequest
	// UserGroupCodesByUser is what UserGroupCodes answers with, keyed by
	// identity. A missing identity belongs to no group.
	UserGroupCodesByUser map[string][]string
	// UserGroupCodesErr, when set, is returned from every membership lookup.
	UserGroupCodesErr error
}

// AccessCheck records one authorization question.
type AccessCheck struct {
	SubjectID string
	AgentName string
}

// ApproveCall records a single approval answer.
type ApproveCall struct {
	RunID     string
	RequestID string
	Decision  string
	Answers   map[string]string
	Reason    string
}

var _ agentInterface = (*Client)(nil)

// agentInterface mirrors the real agent.Interface so the fake can be
// compiled against the same contract without importing the concrete package.
type agentInterface interface {
	ChatCompletions(ctx context.Context, req *v1.ChatCompletionRequest) (*v1.ChatCompletionResponse, error)
	ChatCompletionsStream(ctx context.Context, req *v1.ChatCompletionRequest, onChunk func(*v1.ChatCompletionStreamResponse) error) error
	AgentAskStream(ctx context.Context, req *v1.AgentAskRequest, onEvent func(*v1.AgentStreamEvent) error) error
	AgentApproveStream(ctx context.Context, req *v1.AgentApproveRequest, onEvent func(*v1.AgentStreamEvent) error) error
	Stop(ctx context.Context, runID string) error
	ListAgent(ctx context.Context) ([]*v1.Agent, error)
	GetAgent(ctx context.Context, name string) (*v1.Agent, error)
	ListSkill(ctx context.Context) ([]*v1.Skill, error)
	ContextSkills(ctx context.Context, agentName string) ([]*v1.ContextSkill, error)
	ListMCP(ctx context.Context) ([]*v1.MCP, error)
	ListKnowledgeBase(ctx context.Context) ([]*v1.KnowledgeBase, error)
	ClearSession(ctx context.Context, sessionID string) error
	CheckAgentAccess(ctx context.Context, subjectID, agentName string) (bool, string, error)
	UploadAsset(ctx context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error)
	FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error)
	UserGroupCodes(ctx context.Context, identity string) ([]string, error)
}

func (c *Client) ChatCompletions(_ context.Context, _ *v1.ChatCompletionRequest) (*v1.ChatCompletionResponse, error) {
	return c.Completion, nil
}

func (c *Client) ChatCompletionsStream(_ context.Context, _ *v1.ChatCompletionRequest, onChunk func(*v1.ChatCompletionStreamResponse) error) error {
	if c.StreamErr != nil {
		return c.StreamErr
	}
	for _, chunk := range c.StreamChunks {
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) AgentAskStream(_ context.Context, _ *v1.AgentAskRequest, onEvent func(*v1.AgentStreamEvent) error) error {
	if c.AgentStreamErr != nil {
		return c.AgentStreamErr
	}
	for _, ev := range c.AgentEvents {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) AgentApproveStream(_ context.Context, req *v1.AgentApproveRequest, onEvent func(*v1.AgentStreamEvent) error) error {
	c.ApproveCalls = append(c.ApproveCalls, ApproveCall{
		RunID:     req.GetRunID(),
		RequestID: req.GetRequestID(),
		Decision:  req.GetDecision(),
		Answers:   req.GetAnswers(),
		Reason:    req.GetReason(),
	})
	if c.AgentStreamErr != nil {
		return c.AgentStreamErr
	}
	for _, ev := range c.ApproveEvents {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

// CheckAgentAccess records the question and answers with the configured verdict.
func (c *Client) CheckAgentAccess(_ context.Context, subjectID, agentName string) (bool, string, error) {
	c.AccessChecks = append(c.AccessChecks, AccessCheck{SubjectID: subjectID, AgentName: agentName})
	return c.AccessAllowed, c.AccessReason, nil
}

func (c *Client) Stop(_ context.Context, runID string) error {
	c.StopCalls = append(c.StopCalls, runID)
	return nil
}

// UserGroupCodes answers the membership lookup from the configured map.
func (c *Client) UserGroupCodes(_ context.Context, identity string) ([]string, error) {
	if c.UserGroupCodesErr != nil {
		return nil, c.UserGroupCodesErr
	}
	return c.UserGroupCodesByUser[identity], nil
}

func (c *Client) ListAgent(_ context.Context) ([]*v1.Agent, error) { return c.Agents, nil }

func (c *Client) GetAgent(_ context.Context, name string) (*v1.Agent, error) {
	for _, a := range c.Agents {
		if a.GetName() == name {
			return a, nil
		}
	}
	return nil, nil
}

func (c *Client) ListSkill(_ context.Context) ([]*v1.Skill, error) { return c.Skills, nil }

// ContextSkills returns the fake's per-agent skill commands. An agent with none
// configured resolves to nil rather than to an error, which is what the real
// client does for an agent that exposes no commands.
func (c *Client) ContextSkills(_ context.Context, agentName string) ([]*v1.ContextSkill, error) {
	return c.ContextSkillMap[agentName], nil
}

// ContextCustomCommands returns the fake's per-agent user-defined commands.
func (c *Client) ContextCustomCommands(
	_ context.Context, agentName string,
) ([]*v1.ContextCustomCommand, error) {
	return c.ContextCommandMap[agentName], nil
}

// InvokeCustomCommand records the request and answers with the configured
// response for the command.
func (c *Client) InvokeCustomCommand(
	_ context.Context, name string, req *v1.InvokeCustomCommandRequest,
) (*v1.InvokeCustomCommandResponse, error) {
	c.InvocationRequests = append(c.InvocationRequests, req)
	if c.InvocationErr != nil {
		return nil, c.InvocationErr
	}
	if resp, ok := c.InvocationResponses[name]; ok {
		return resp, nil
	}
	return &v1.InvokeCustomCommandResponse{
		Name: name,
		Kind: "prompt",
		Status: &v1.InvocationStatus{
			Phase:  "Succeeded",
			Prompt: &v1.PromptInvocation{Prompt: "fake prompt"},
		},
	}, nil
}

// ApproveCustomCommand records the approval and answers a successful tool call.
func (c *Client) ApproveCustomCommand(
	_ context.Context, name string, req *v1.ApproveCustomCommandRequest,
) (*v1.ApproveCustomCommandResponse, error) {
	c.ApprovalRequests = append(c.ApprovalRequests, req)
	if c.InvocationErr != nil {
		return nil, c.InvocationErr
	}
	return &v1.ApproveCustomCommandResponse{
		ApprovalId: req.GetApprovalId(),
		Kind:       "tool",
		Name:       name,
		Status: &v1.InvocationStatus{
			Phase:  "Succeeded",
			Tool:   &v1.ToolInvocation{McpServer: "fake", Tool: "tool", Result: structpb.NewStringValue("ok")},
			Reason: "OK",
		},
	}, nil
}

// ListCustomCommand returns the fake's whole command set.
func (c *Client) ListCustomCommand(_ context.Context) ([]*v1.CustomCommand, error) {
	return c.CustomCommands, nil
}

func (c *Client) ListMCP(_ context.Context) ([]*v1.MCP, error) { return c.MCPs, nil }

func (c *Client) ListKnowledgeBase(_ context.Context) ([]*v1.KnowledgeBase, error) {
	return c.KnowledgeBases, nil
}

// UploadAsset records the request and answers with the next configured asset.
//
// With no Assets configured the answer is built from the request rather than
// being a constant: the real server echoes back the kind, media type and name
// it stored, and a fake that ignored them would let a test pass on values the
// deployment would never see.
func (c *Client) UploadAsset(_ context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error) {
	c.Uploaded = append(c.Uploaded, req)
	if len(c.Assets) > 0 {
		return c.Assets[0], nil
	}
	kind := req.GetKind()
	if kind == "" {
		kind = "image"
	}
	mimeType := req.GetMimeType()
	if mimeType == "" {
		mimeType = "image/png"
	}
	return &v1.Asset{
		AssetId:  "img_fake",
		Kind:     kind,
		Url:      "/media/fake",
		MimeType: mimeType,
		Name:     req.GetFilename(),
		Source:   req.GetSource(),
		Size:     int64(len(req.GetData())),
	}, nil
}

// FetchAssetContent answers with the configured bytes for an asset id.
//
// An empty id is refused, as the real client refuses it (agent.go:382): a
// caller that passes one has a bug, and a fake that answered would hide it
// until production.
func (c *Client) FetchAssetContent(_ context.Context, assetID string) ([]byte, string, error) {
	if assetID == "" {
		return nil, "", fmt.Errorf("fake: no asset id")
	}
	data, ok := c.AssetContent[assetID]
	if !ok {
		return nil, "", fmt.Errorf("fake: no content for asset %q", assetID)
	}
	mimeType := c.AssetMimeType
	if mimeType == "" {
		mimeType = "image/png"
	}
	return data, mimeType, nil
}

func (c *Client) ClearSession(_ context.Context, sessionID string) error {
	c.ClearSessionIDs = append(c.ClearSessionIDs, sessionID)
	return nil
}
