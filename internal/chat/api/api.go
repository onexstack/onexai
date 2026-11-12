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

// Package api holds the request and response shapes shared by the chat
// handler, its validators and the business layer.
//
// They live here rather than in pkg/api/chat/v1 because several of them embed
// the internal row models — the ticket and on-call wire contracts are the
// model's JSON tags, and a second declaration of those fields would be a second
// thing to keep in step.
package api

import (
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/pkg/media"
)

// OncallGroup is the on-call group wire shape. Its JSON tags are the model's.
type OncallGroup = model.OncallGroupM

// GetOncallGroupRequest names one on-call group.
type GetOncallGroupRequest struct {
	Name string `uri:"name"`
}

// DeleteOncallGroupRequest names one on-call group.
type DeleteOncallGroupRequest struct {
	Name string `uri:"name"`
}

// ListOncallGroupQuery is the empty query of the group list.
type ListOncallGroupQuery struct{}

// ListTicketQuery is the ticket read surface's query string.
type ListTicketQuery struct {
	// Status filters by model.TicketStatus: open/processing/saved/resolved/closed.
	Status string `form:"status"`
	// GroupID filters by the duty group the ticket is routed to.
	GroupID string `form:"group_id"`
	// AssigneeID filters by the primary handler's id.
	AssigneeID string `form:"assignee_id"`
	// Page is 1-indexed; PageSize is clamped by the biz layer.
	Page     int `form:"page"`
	PageSize int `form:"page_size"`
}

// ListTicketResponse is one page of tickets.
type ListTicketResponse struct {
	Total   int64            `json:"total"`
	Page    int              `json:"page"`
	Tickets []*model.TicketM `json:"tickets"`
}

// GetTicketRequest names one ticket.
type GetTicketRequest struct {
	TicketID string `uri:"ticketID"`
}

// ListTicketNoteRequest names the ticket whose notes are read.
type ListTicketNoteRequest struct {
	TicketID string `uri:"ticketID"`
}

// ListTicketNoteResponse is one ticket's handling log.
type ListTicketNoteResponse struct {
	TicketID string               `json:"ticket_id"`
	Total    int64                `json:"total"`
	Notes    []*model.TicketNoteM `json:"notes"`
}

// GetCommandRequest names the command to read and the namespace it lives in.
type GetCommandRequest struct {
	Name      string `uri:"name"`
	Namespace string `form:"namespace"`
}

// GetPeerBindClaimRequest names a binding claim.
type GetPeerBindClaimRequest struct {
	Code string `uri:"code"`
}

// PeerBindClaimResponse is the claim a business-side caller collects.
type PeerBindClaimResponse struct {
	Backend     string `json:"backend"`
	Scope       string `json:"scope"`
	PeerID      string `json:"peerID"`
	ConfirmHash string `json:"confirmHash"`
}

// PeerAgentRequest names one peer binding by its path triple.
type PeerAgentRequest struct {
	Backend string `uri:"backend"`
	Scope   string `uri:"scope"`
	PeerID  string `uri:"peerID"`
}

// Default normalizes the URL spelling of an empty scope. A path segment cannot
// be empty, so an unscoped binding travels as "-"; the rest of the service uses
// the empty string.
func (rq *PeerAgentRequest) Default() { rq.Scope = normalizeScope(rq.Scope) }

// PutPeerAgentRequest is the binding upsert body plus the path triple.
type PutPeerAgentRequest struct {
	Backend   string   `json:"-" uri:"backend"`
	Scope     string   `json:"-" uri:"scope"`
	PeerID    string   `json:"-" uri:"peerID"`
	Canonical string   `json:"canonical"`
	Agents    []string `json:"agents"`
	// Source is the authenticated caller, stamped by the binder. It is not
	// read from the body.
	Source string `json:"-"`
}

// Default is the PUT counterpart of PeerAgentRequest.Default.
func (rq *PutPeerAgentRequest) Default() { rq.Scope = normalizeScope(rq.Scope) }

// PeerRefResponse names the binding a PUT wrote.
type PeerRefResponse struct {
	Backend string `json:"backend"`
	Scope   string `json:"scope"`
	PeerID  string `json:"peerID"`
}

// PeerAgentResponse is one peer's agent binding. Agents is the encoded column
// the row stores, served verbatim as it always has been.
type PeerAgentResponse struct {
	Backend   string `json:"backend"`
	Scope     string `json:"scope"`
	PeerID    string `json:"peerID"`
	Canonical string `json:"canonical"`
	Agents    string `json:"agents"`
	Source    string `json:"source"`
}

// GetHistoryRequest names the conversation whose history is read.
type GetHistoryRequest struct {
	ConvID string `uri:"convID"`
}

// HistoryItem is one message in a conversation's history.
type HistoryItem struct {
	ID          string      `json:"id"`
	ConvID      string      `json:"conv_id"`
	SenderID    string      `json:"sender_id"`
	SenderName  string      `json:"sender_name"`
	Content     string      `json:"content"`
	ContentType string      `json:"content_type,omitempty"`
	Images      []media.Ref `json:"images,omitempty"`
	CreateTime  int64       `json:"create_time"`
	IsBot       bool        `json:"is_bot"`
}

// HistoryResponse is a page of history.
type HistoryResponse struct {
	Messages []HistoryItem `json:"messages"`
}

// ListMemberRequest names the conversation whose members are read.
type ListMemberRequest struct {
	ConvID string `uri:"convID"`
}

// MembersResponse is a conversation's member list.
type MembersResponse struct {
	Members []im.Member `json:"members"`
}

// AgentItem is the snake_case wire form of an agent.
type AgentItem struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
}

// ListAgentResponse is every agent the service knows.
type ListAgentResponse struct {
	Agents []AgentItem `json:"agents"`
}

// SearchQuery is the cross-conversation message search.
type SearchQuery struct {
	Q     string `form:"q"`
	Limit int    `form:"limit"`
}

// SearchItem is one search hit.
type SearchItem struct {
	ID         string `json:"id"`
	ConvID     string `json:"conv_id"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name"`
	Content    string `json:"content"`
	CreateTime int64  `json:"create_time"`
	IsBot      bool   `json:"is_bot"`
}

// SearchResponse is a page of search hits.
type SearchResponse struct {
	Messages []SearchItem `json:"messages"`
}

// ListOncallTicketRequest is the internal ticket-sync query. The comma
// separated spellings are the wire contract; the validator fills the parsed
// slices so neither the handler nor the biz layer repeats the split.
type ListOncallTicketRequest struct {
	Groups   string `form:"groups"`
	Statuses string `form:"statuses"`
	Since    string `form:"since"`
	Limit    int    `form:"limit"`

	GroupIDs     []string  `form:"-"`
	StatusValues []string  `form:"-"`
	SinceTime    time.Time `form:"-"`
}

// GetOncallTicketRequest is the single-ticket query.
type GetOncallTicketRequest struct {
	TicketID string `uri:"ticketID"`
	Statuses string `form:"statuses"`

	StatusValues []string `form:"-"`
}

// OncallCandidate is one ticket plus what a consumer needs beside it.
type OncallCandidate struct {
	*model.TicketM
	// Eligible is only set by the single-ticket read. On a page from the list it
	// is absent, because everything in that page is eligible by construction.
	Eligible bool `json:"eligible,omitempty"`
}

// ListOncallTicketResponse is a sync page.
type ListOncallTicketResponse struct {
	Backend    string             `json:"backend"`
	AppID      string             `json:"appID"`
	Groups     []string           `json:"groups"`
	Statuses   []string           `json:"statuses"`
	Total      int                `json:"total"`
	Candidates []*OncallCandidate `json:"candidates"`
}

// GetOncallTicketResponse is one ticket with its eligibility.
type GetOncallTicketResponse struct {
	Backend string           `json:"backend"`
	AppID   string           `json:"appID"`
	Ticket  *OncallCandidate `json:"ticket"`
}

func normalizeScope(scope string) string {
	if scope == "-" {
		return ""
	}
	return scope
}
