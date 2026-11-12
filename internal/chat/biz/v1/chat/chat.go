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

// Package chat implements chat session business logic.
package chat

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/clientset/agent"
	"github.com/onexstack/onexai/pkg/media"
	"github.com/onexstack/onexai/pkg/wshub"
)

// ProviderSet defines Wire DI providers for the chat biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(ChatBiz), new(*chatBiz)))

// Assets is this service's window onto the media store the agent backend owns.
//
// It is a consumer-side interface rather than a concrete client so this package
// depends on two methods instead of on the whole agent client, and a test can
// answer it without an HTTP server.
//
// # Why the window is here at all
//
// A TUI holds a bearer token for *this* service and has no key to sign an agent
// request with, so an endpoint here is the only way it can reach the store. The
// alternative is a client that cannot attach a picture to a message the server
// already accepts one on.
type Assets interface {
	// UploadAsset stores bytes and returns the reference that names them.
	UploadAsset(ctx context.Context, req *agentv1.CreateAssetRequest) (*agentv1.Asset, error)
	// FetchAssetContent returns an asset's bytes and media type.
	FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error)
}

// ChatBiz defines the chat business logic interface. It is a pure message
// sync layer: it relays Lark messages to subscribed onexai chat TUI clients and
// forwards TUI messages back to Lark. It does not execute commands or run AI.
type ChatBiz interface {
	ProcessIMEvent(ctx context.Context, event *im.Event)
	ListConversation(ctx context.Context, userID string) ([]*im.Conversation, error)
	ListMember(ctx context.Context, req *api.ListMemberRequest) (*api.MembersResponse, error)
	SearchMessages(ctx context.Context, req *api.SearchQuery) (*api.SearchResponse, error)
	GetHistory(ctx context.Context, convID string, limit int) ([]*im.Message, error)
	// GetHistoryView returns the REST wire shape of a conversation's history.
	GetHistoryView(ctx context.Context, req *api.GetHistoryRequest) (*api.HistoryResponse, error)
	SendHistory(ctx context.Context, clientID, convID string)
	SendToIM(ctx context.Context, clientID, convID, content string, images []media.Ref, clientMsgID string)
	// ListAgent returns all enabled agents from oai-agent-apiserver (robots + platforms).
	ListAgent(ctx context.Context) (*api.ListAgentResponse, error)
	// SendToAgent streams an agent answer for a virtual (robot/platform) or bound
	// conversation back to the client via the WS hub.
	//
	// images are references the client uploaded; the agent backend resolves
	// them at the model edge, so nothing here holds the bytes.
	SendToAgent(ctx context.Context, clientID, convID, agentName, content string, images []media.Ref, clientMsgID string)
	// PeerBindClaim returns the live binding claim for a code.
	PeerBindClaim(ctx context.Context, req *api.GetPeerBindClaimRequest) (*api.PeerBindClaimResponse, error)
	// PutPeerAgent upserts a peer's agent binding.
	PutPeerAgent(ctx context.Context, req *api.PutPeerAgentRequest) (*api.PeerRefResponse, error)
	// PeerAgent returns a peer's binding.
	PeerAgent(ctx context.Context, req *api.PeerAgentRequest) (*api.PeerAgentResponse, error)
	// DeletePeerAgent removes a peer's binding. Absence is not an error.
	DeletePeerAgent(ctx context.Context, req *api.PeerAgentRequest) error
	// DownloadImage fetches an image message's raw bytes by resolving the cached
	// message's image_key and downloading it from the IM backend.
	DownloadImage(ctx context.Context, msgID string) ([]byte, string, error)
	// UploadAsset stores an image on the backend that owns the store and returns
	// the reference that names it. It is how a client with only this service's
	// token can attach a picture to a message.
	UploadAsset(ctx context.Context, req *agentv1.CreateAssetRequest) (*agentv1.Asset, error)
	// FetchAssetContent returns an image's bytes by its reference, for a client
	// rendering a message that carries one.
	FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error)
}

var _ ChatBiz = (*chatBiz)(nil)

// chatBiz handles real-time chat message synchronization between Lark and the
// onexai chat TUI clients.
type chatBiz struct {
	lark     *lark.Backend
	hub      *wshub.Hub
	messages store.MessageStore
	llm      agent.Interface
	// assets is this service's window onto the media store the agent backend
	// owns: the TUI's way to store an attachment, and the way a TUI attachment
	// reaches Lark, which addresses an image by a key only its own upload API
	// can issue.
	assets Assets

	// peers is the whole store, not the two sub-stores the peer endpoints use:
	// they are reached through it so a future peer-related table does not need a
	// third constructor parameter.
	peers store.IStore
}

// New creates a chatBiz instance.
func New(lark *lark.Backend, hub *wshub.Hub, messages store.MessageStore, llm agent.Interface, peers store.IStore, assets Assets) *chatBiz {
	return &chatBiz{lark: lark, hub: hub, messages: messages, llm: llm, peers: peers, assets: assets}
}

// PeerBindClaim implements ChatBiz.
func (b *chatBiz) PeerBindClaim(
	ctx context.Context, req *api.GetPeerBindClaimRequest,
) (*api.PeerBindClaimResponse, error) {
	claim, err := b.peers.PeerBindClaims().GetLive(ctx, req.Code)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if claim == nil {
		return nil, errno.ErrNotFound.WithMessage("no live binding claim for that code")
	}
	return &api.PeerBindClaimResponse{
		Backend: claim.Backend,
		Scope:   claim.Scope,
		PeerID:  claim.PeerID,
		// Empty for a row written before the column existed. The business side
		// treats that as "nothing to check against" and refuses, rather than as
		// a match.
		ConfirmHash: claim.ConfirmHash,
	}, nil
}

// PutPeerAgent implements ChatBiz.
func (b *chatBiz) PutPeerAgent(
	ctx context.Context, req *api.PutPeerAgentRequest,
) (*api.PeerRefResponse, error) {
	row := &model.PeerAgentM{
		Backend:   req.Backend,
		Scope:     req.Scope,
		PeerID:    req.PeerID,
		Canonical: req.Canonical,
		Agents:    model.EncodeNameList(req.Agents),
		Source:    req.Source,
	}
	if err := b.peers.PeerAgents().Put(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &api.PeerRefResponse{Backend: req.Backend, Scope: req.Scope, PeerID: req.PeerID}, nil
}

// PeerAgent implements ChatBiz.
func (b *chatBiz) PeerAgent(
	ctx context.Context, req *api.PeerAgentRequest,
) (*api.PeerAgentResponse, error) {
	row, err := b.peers.PeerAgents().GetByPeer(ctx, req.Backend, req.Scope, req.PeerID)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if row == nil {
		return nil, errno.ErrNotFound.WithMessage("no binding for that peer")
	}
	return &api.PeerAgentResponse{
		Backend:   row.Backend,
		Scope:     row.Scope,
		PeerID:    row.PeerID,
		Canonical: row.Canonical,
		Agents:    row.Agents,
		Source:    row.Source,
	}, nil
}

// DeletePeerAgent implements ChatBiz.
func (b *chatBiz) DeletePeerAgent(ctx context.Context, req *api.PeerAgentRequest) error {
	if err := b.peers.PeerAgents().DeleteByPeer(ctx, req.Backend, req.Scope, req.PeerID); err != nil {
		return storeerr.Write(err)
	}
	return nil
}

// ProcessIMEvent is called for every event received from the IM backend.
// It persists the message and pushes it to subscribed TUI clients.
func (b *chatBiz) ProcessIMEvent(ctx context.Context, event *im.Event) {
	if event.Type != im.EventTypeMessage {
		return
	}
	message := event.Message
	if message == nil {
		return
	}

	// The Lark webhook only carries the sender's open_id (no display name).
	// Resolve it from the conversation member list so the TUI and the message
	// cache show real names instead of raw "ou_xxx"/"cli_xxx" IDs.
	if b.lark != nil && message.SenderID != "" {
		if name := b.lark.MemberName(ctx, message.ConvID, message.SenderID); name != "" {
			message.SenderName = name
		}
	}

	// Persist to the local cache (dedup by MsgID) before broadcasting.
	b.persist(ctx, message)

	// Forward to TUI clients subscribed to this conversation.
	b.hub.SendToConv(message.ConvID, wshub.ServerFrame{
		Type: wshub.FrameTypeMessage,
		Payload: wshub.MessagePayload{
			ID:          message.ID,
			ConvID:      message.ConvID,
			SenderID:    message.SenderID,
			SenderName:  message.SenderName,
			Content:     message.Content.Text,
			ContentType: message.Content.Type,
			CreateTime:  message.CreateTime.Unix(),
			IsBot:       message.IsBot,
		},
	})
}

// ListConversation returns the conversations for the given user from the IM backend.
func (b *chatBiz) ListConversation(ctx context.Context, userID string) ([]*im.Conversation, error) {
	if b.lark == nil {
		return nil, nil
	}
	convs, err := b.lark.ListConversation(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list conversations from IM backend", "error", err)
		return nil, errno.ErrInternal.WithMessage("failed to list conversations from the IM backend")
	}
	return convs, nil
}

// persist saves a message to the local cache (best-effort, deduped by MsgID).
func (b *chatBiz) persist(ctx context.Context, message *im.Message) {
	if message == nil || message.ID == "" {
		return
	}
	rec := &model.MessageM{
		MsgID:       message.ID,
		ConvID:      message.ConvID,
		SenderID:    message.SenderID,
		SenderName:  message.SenderName,
		Content:     message.Content.Text,
		ContentType: message.Content.Type,
		Raw:         message.Content.Raw,
		Images:      encodeRefs(message.Content.Images),
		Backend:     message.Backend,
		IsBot:       message.IsBot,
		SendTime:    message.CreateTime,
	}
	if rec.SendTime.IsZero() {
		rec.SendTime = time.Now()
	}
	if err := b.messages.Save(ctx, rec); err != nil {
		slog.WarnContext(ctx, "Could not cache message", "msg_id", message.ID, "error", err)
	}
}

// encodeRefs renders image references for the Images column, and decodeRefs
// reads them back.
//
// A malformed value decodes to nil rather than an error: this is read on the
// path that renders history, and one bad row must not take the conversation
// down with it.
func encodeRefs(refs []media.Ref) string {
	if len(refs) == 0 {
		return ""
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeRefs(raw string) []media.Ref {
	if raw == "" {
		return nil
	}
	var out []media.Ref
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// SearchMessages searches the local message cache across all conversations.
func (b *chatBiz) SearchMessages(ctx context.Context, req *api.SearchQuery) (*api.SearchResponse, error) {
	if req.Q == "" {
		return &api.SearchResponse{Messages: []api.SearchItem{}}, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	recs, err := b.messages.Search(ctx, req.Q, limit)
	if err != nil {
		// Non-fatal: return empty results rather than a 500 on cache errors.
		slog.WarnContext(ctx, "failed to search messages in cache", "query", req.Q, "error", err)
		return &api.SearchResponse{Messages: []api.SearchItem{}}, nil
	}
	items := make([]api.SearchItem, 0, len(recs))
	for _, r := range recs {
		items = append(items, api.SearchItem{
			ID:         r.MsgID,
			ConvID:     r.ConvID,
			SenderID:   r.SenderID,
			SenderName: r.SenderName,
			Content:    r.Content,
			CreateTime: r.SendTime.Unix(),
			IsBot:      r.IsBot,
		})
	}
	return &api.SearchResponse{Messages: items}, nil
}

// ListMember returns the members of a conversation, if the backend supports it.
// Returns an empty slice (no error) when unsupported or unconfigured, so the
// client can fall back to conversation participants for @-mention autocomplete.
func (b *chatBiz) ListMember(ctx context.Context, req *api.ListMemberRequest) (*api.MembersResponse, error) {
	if b.lark == nil {
		return &api.MembersResponse{Members: []im.Member{}}, nil
	}
	members, err := b.lark.GetChatMembers(ctx, req.ConvID)
	if err != nil {
		// Non-fatal: return empty so the client falls back to participants.
		slog.WarnContext(ctx, "failed to list members from IM backend", "conv_id", req.ConvID, "error", err)
		return &api.MembersResponse{Members: []im.Member{}}, nil
	}
	if members == nil {
		members = []im.Member{}
	}
	return &api.MembersResponse{Members: members}, nil
}

// DownloadImage fetches an image message's raw bytes by resolving its cached
// image_key and downloading the resource from the Lark backend.
func (b *chatBiz) DownloadImage(ctx context.Context, msgID string) ([]byte, string, error) {
	if b.lark == nil {
		return nil, "", errno.ErrNotConfigured.WithMessage("image download is not configured in this deployment")
	}
	rec, err := b.messages.GetByMsgID(ctx, msgID)
	if err != nil {
		return nil, "", storeerr.Read(err)
	}
	if rec == nil {
		return nil, "", errno.ErrNotFound.WithMessage("message %q not found in cache", msgID)
	}
	fileKey := lark.ExtractImageKey(rec.Raw)
	if fileKey == "" {
		return nil, "", errno.ErrNotFound.WithMessage("message %q has no image", msgID)
	}
	data, contentType, err := b.lark.DownloadImage(ctx, msgID, fileKey)
	if err != nil {
		return nil, "", errno.ErrInternal.WithMessage("failed to download the image from the IM backend")
	}
	return data, contentType, nil
}
