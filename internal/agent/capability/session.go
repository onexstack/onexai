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

package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
)

// SessionStore is the session data the control-plane tools read and write.
//
// It is deliberately narrower than the run store: these tools own conversations,
// not runs, so they need the session row and its messages and nothing else. A
// narrower interface is also what makes the ownership rule below auditable —
// every path into the store is here.
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*model.SessionM, error)
	ListSession(ctx context.Context, owner string, limit int) ([]*model.SessionM, error)
	ListMessage(ctx context.Context, sessionID string) ([]*model.SessionMessageM, error)
	CreateSession(ctx context.Context, session *model.SessionM) error
	ReplaceMessages(ctx context.Context, sessionID string, messages []*model.SessionMessageM) error
}

// SessionProvider exposes the caller's own conversations over MCP.
//
// Every tool here is scoped to the caller: a session is visible only when its
// owner is the subject the token was issued for. That check is repeated on
// every path rather than trusted to the listing, because a model that learns an
// id from anywhere else must still not reach someone else's conversation.
type SessionProvider struct {
	Sessions SessionStore
	// Invoker continues a conversation. It is the same invoker delegation uses,
	// so a continued session goes through the ordinary Ask path.
	Invoker AgentInvoker
	// Delegation bounds a chain that continues sessions in a loop.
	Delegation *AgentProvider
}

// DefaultSessionListLimit caps a listing that does not say how many.
const DefaultSessionListLimit = 20

// Namespace implements Provider.
func (p *SessionProvider) Namespace() string { return "session" }

// Tools implements Provider.
func (p *SessionProvider) Tools() []Tool {
	if p.Sessions == nil || p.Invoker == nil {
		return nil
	}
	return []Tool{
		{
			Namespace:   "session",
			Name:        "list",
			Description: "List your recent conversations, newest first.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "description": "How many to return. Defaults to 20."},
				},
			},
			Handler: p.list,
		},
		{
			Namespace:   "session",
			Name:        "history",
			Description: "Read the messages of one of your conversations.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session"},
				"properties": map[string]any{
					"session": map[string]any{"type": "string", "description": "The conversation id."},
					"limit":   map[string]any{"type": "integer", "description": "Return only the newest N messages. 0 returns all."},
				},
			},
			Handler: p.history,
		},
		{
			Namespace:   "session",
			Name:        "invoke",
			Description: "Continue a conversation of yours with a new message and return the answer.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session", "message"},
				"properties": map[string]any{
					"session": map[string]any{"type": "string", "description": "The conversation id."},
					"message": map[string]any{"type": "string", "description": "What to say next."},
				},
			},
			Handler: p.invoke,
		},
		{
			Namespace:   "session",
			Name:        "fork",
			Description: "Copy one of your conversations into a new one, optionally up to a message, and leave the original untouched.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session"},
				"properties": map[string]any{
					"session": map[string]any{"type": "string", "description": "The conversation to copy."},
					"upToSeq": map[string]any{"type": "integer", "description": "Copy only messages up to this sequence number. 0 copies everything."},
					"name":    map[string]any{"type": "string", "description": "A name for the new conversation."},
				},
			},
			Handler: p.fork,
		},
	}
}

// owned loads a session and checks that the caller owns it.
//
// A session that exists but belongs to someone else answers exactly like one
// that does not exist: a distinguishable "not yours" would confirm the id.
func (p *SessionProvider) owned(ctx context.Context, claims Claims, id string) (*model.SessionM, error) {
	if id == "" {
		return nil, fmt.Errorf("session: a session id is required")
	}
	session, err := p.Sessions.GetSession(ctx, id)
	if err != nil || session == nil {
		return nil, fmt.Errorf("session: %q not found", id)
	}
	if claims.Subject == "" || session.Owner != claims.Subject {
		return nil, fmt.Errorf("session: %q not found", id)
	}
	if session.DeletedAt > 0 {
		return nil, fmt.Errorf("session: %q not found", id)
	}
	return session, nil
}

func (p *SessionProvider) list(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil && len(args) > 0 {
		return nil, fmt.Errorf("session_list: decode arguments: %w", err)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = DefaultSessionListLimit
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("session_list: the run has no caller to list sessions for")
	}
	sessions, err := p.Sessions.ListSession(ctx, claims.Subject, limit)
	if err != nil {
		return nil, fmt.Errorf("session_list: %w", err)
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, map[string]any{
			"session":    session.ID,
			"name":       session.Name,
			"agent":      session.Agent,
			"state":      session.State,
			"lastActive": session.LastActiveAt,
		})
	}
	return map[string]any{"sessions": out}, nil
}

func (p *SessionProvider) history(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Session string `json:"session"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("session_history: decode arguments: %w", err)
	}
	session, err := p.owned(ctx, claims, input.Session)
	if err != nil {
		return nil, err
	}
	messages, err := p.Sessions.ListMessage(ctx, session.ID)
	if err != nil {
		return nil, fmt.Errorf("session_history: %w", err)
	}
	if input.Limit > 0 && len(messages) > input.Limit {
		messages = messages[len(messages)-input.Limit:]
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, map[string]any{
			"seq":       message.Seq,
			"role":      message.Role,
			"content":   message.Content,
			"createdAt": message.CreatedAt,
		})
	}
	return map[string]any{
		"session":  session.ID,
		"agent":    session.Agent,
		"messages": out,
	}, nil
}

func (p *SessionProvider) invoke(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Session string `json:"session"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("session_invoke: decode arguments: %w", err)
	}
	if input.Message == "" {
		return nil, fmt.Errorf("session_invoke needs a message")
	}
	session, err := p.owned(ctx, claims, input.Session)
	if err != nil {
		return nil, err
	}
	if p.Delegation == nil {
		return nil, fmt.Errorf("session_invoke: the runtime is not configured")
	}
	// The session's own agent runs, with the session's identity: this continues
	// a conversation, it does not start a different one.
	return p.Delegation.Call(ctx, AgentRequest{
		Arguments: AgentArguments{
			Agent:   session.Agent,
			Message: input.Message,
			Session: session.ID,
		},
		RunID:       claims.RunID,
		RootRunID:   rootRunOf(claims),
		Depth:       claims.Depth,
		Subject:     claims.Subject,
		SubjectKind: claims.SubjectKind,
	})
}

// fork copies a conversation into a new one.
//
// The copy stops at a message, not at a live process: the new session runs the
// same agent, runtime and revision as the source, and from then on the two
// evolve independently.
func (p *SessionProvider) fork(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Session string `json:"session"`
		UpToSeq int64  `json:"upToSeq"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("session_fork: decode arguments: %w", err)
	}
	source, err := p.owned(ctx, claims, input.Session)
	if err != nil {
		return nil, err
	}
	messages, err := p.Sessions.ListMessage(ctx, source.ID)
	if err != nil {
		return nil, fmt.Errorf("session_fork: %w", err)
	}
	now := time.Now().UnixMilli()
	forked := &model.SessionM{
		ID:             uuid.NewString(),
		Namespace:      source.Namespace,
		Agent:          source.Agent,
		RuntimeKind:    source.RuntimeKind,
		RuntimeProfile: source.RuntimeProfile,
		RevisionID:     source.RevisionID,
		Owner:          source.Owner,
		Name:           input.Name,
		State:          model.SessionStateReady,
		LastActiveAt:   now,
		CreatedAt:      now,
	}
	if err := p.Sessions.CreateSession(ctx, forked); err != nil {
		return nil, fmt.Errorf("session_fork: %w", err)
	}
	copied := make([]*model.SessionMessageM, 0, len(messages))
	for _, message := range messages {
		if input.UpToSeq > 0 && message.Seq > input.UpToSeq {
			break
		}
		copied = append(copied, &model.SessionMessageM{
			Role:      message.Role,
			Content:   message.Content,
			Images:    message.Images,
			CreatedAt: message.CreatedAt,
		})
	}
	if err := p.Sessions.ReplaceMessages(ctx, forked.ID, copied); err != nil {
		return nil, fmt.Errorf("session_fork: copy messages: %w", err)
	}
	return map[string]any{"session": forked.ID, "agent": forked.Agent, "messages": len(copied)}, nil
}
