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

package chat

import (
	"context"
	"log/slog"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/pkg/wshub"
)

const defaultHistoryLimit = 50

// GetHistoryView returns the most recent messages for the given conversation in
// the REST wire shape.
func (b *chatBiz) GetHistoryView(ctx context.Context, req *api.GetHistoryRequest) (*api.HistoryResponse, error) {
	msgs, err := b.GetHistory(ctx, req.ConvID, defaultHistoryLimit)
	if err != nil {
		return nil, err
	}
	items := make([]api.HistoryItem, 0, len(msgs))
	for _, message := range msgs {
		items = append(items, api.HistoryItem{
			ID:          message.ID,
			ConvID:      message.ConvID,
			SenderID:    message.SenderID,
			SenderName:  message.SenderName,
			Content:     message.Content.Text,
			ContentType: message.Content.Type,
			Images:      message.Content.Images,
			CreateTime:  message.CreateTime.Unix(),
			IsBot:       message.IsBot,
		})
	}
	return &api.HistoryResponse{Messages: items}, nil
}

// GetHistory returns the most recent messages for the given conversation. It
// fetches authoritative history from the IM backend and populates the local
// cache as a side effect (for search/dedup). If the backend is unavailable or
// errors, it falls back to the local cache.
func (b *chatBiz) GetHistory(ctx context.Context, convID string, limit int) ([]*im.Message, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if b.lark != nil {
		msgs, err := b.lark.GetHistory(ctx, convID, limit)
		if err == nil {
			for _, m := range msgs {
				b.persist(ctx, m)
			}
			return msgs, nil
		}
		// Backend error — log it and fall back to the local cache below so the
		// client gets (possibly empty) history instead of a 500.
		slog.WarnContext(ctx, "IM backend GetHistory failed; falling back to cache",
			"conv_id", convID, "error", err)
	}
	recs, err := b.messages.ListByConv(ctx, convID, limit)
	if err != nil {
		// Cache miss/unavailable (e.g. table not migrated) must not fail the
		// request — return empty history instead of a 500.
		slog.WarnContext(ctx, "Message cache unavailable", "conv_id", convID, "error", err)
		return nil, nil
	}
	msgs := make([]*im.Message, 0, len(recs))
	for _, r := range recs {
		msgs = append(msgs, &im.Message{
			ID:         r.MsgID,
			ConvID:     r.ConvID,
			SenderID:   r.SenderID,
			SenderName: r.SenderName,
			Content: im.MessageContent{
				Type:   r.ContentType,
				Text:   r.Content,
				Raw:    r.Raw,
				Images: decodeRefs(r.Images),
			},
			CreateTime: r.SendTime,
			IsBot:      r.IsBot,
			Backend:    r.Backend,
		})
	}
	return msgs, nil
}

// SendHistory pushes historical messages to the given TUI client as a batch of
// "message" frames. Silently no-ops when there is no IM backend or history is
// empty. Call this after a client subscribes to a conversation.
func (b *chatBiz) SendHistory(ctx context.Context, clientID, convID string) {
	msgs, err := b.GetHistory(ctx, convID, defaultHistoryLimit)
	if err != nil || len(msgs) == 0 {
		return
	}
	for _, message := range msgs {
		b.hub.SendTo(clientID, wshub.ServerFrame{
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
}
