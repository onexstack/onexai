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
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
	"github.com/onexstack/onexai/pkg/wshub"
)

// SendToIM forwards a message typed in the TUI to the IM backend, then
// broadcasts the confirmed message back to all subscribed TUI clients so the
// sender's view is consistent with the rest of the room.
//
// Virtual robot/platform conversations are routed to the agent instead: they
// are addressed to an agent via oai-agent-apiserver, never to Lark.
func (b *chatBiz) SendToIM(ctx context.Context, clientID, convID, content string, images []media.Ref, clientMsgID string) {
	if agentName, ok := agentFromConvID(convID); ok {
		b.SendToAgent(ctx, clientID, convID, agentName, content, images, clientMsgID)
		return
	}

	if b.lark == nil {
		b.hub.SendTo(clientID, wshub.ServerFrame{
			Type:    wshub.FrameTypeAck,
			Payload: wshub.AckPayload{ClientMsgID: clientMsgID, Error: "No Lark backend configured"},
		})
		return
	}

	// The text goes first when there is any, so an image appears under the
	// sentence that explains it. An image-only message sends no text at all —
	// there is no sentence to put it under, and an empty one would be a blank
	// line in the conversation.
	var message *im.Message
	if content != "" {
		var err error
		message, err = b.lark.SendMessage(ctx, &im.SendMessageReq{
			ConvID:  convID,
			Content: im.MessageContent{Type: "text", Text: content},
		})
		if err != nil {
			slog.ErrorContext(ctx, "Could not send message to IM backend", "conv_id", convID, "error", err)
			// Ack with an error so the client can mark the message failed / retry.
			b.hub.SendTo(clientID, wshub.ServerFrame{
				Type:    wshub.FrameTypeAck,
				Payload: wshub.AckPayload{ClientMsgID: clientMsgID, Error: err.Error()},
			})
			return
		}
	} else {
		message = &im.Message{ConvID: convID}
	}

	// The images follow. A failure here is reported but does not fail the
	// message: the text was delivered, and telling the sender the whole thing
	// failed would have them retype a sentence the room has already read.
	for _, ref := range images {
		if err := b.sendImageToConv(ctx, convID, ref); err != nil {
			slog.WarnContext(ctx, "Could not relay an attached image to the IM backend",
				"conv_id", convID, "asset_id", ref.AssetID, "error", err)
			b.hub.SendTo(clientID, wshub.ServerFrame{
				Type: wshub.FrameTypeCommandDone,
				Payload: wshub.CommandDonePayload{
					Error: "图片发送失败，文字已送达",
				},
			})
		}
	}

	// Confirm delivery to the sender (maps client id → server id).
	b.hub.SendTo(clientID, wshub.ServerFrame{
		Type:    wshub.FrameTypeAck,
		Payload: wshub.AckPayload{ClientMsgID: clientMsgID, ServerID: message.ID},
	})

	// Echo the confirmed message to all clients subscribed to this conversation
	// (including the sender). Attribute it to the human who typed it (their
	// authenticated identity) rather than the bot that relayed it to the IM.
	ts := time.Now().Unix()
	if !message.CreateTime.IsZero() {
		ts = message.CreateTime.Unix()
	}
	userID, openID := b.hub.ClientIdentity(clientID)
	senderName := userID
	senderID := openID
	if senderName == "" {
		senderName = message.SenderName
	}
	if senderID == "" {
		senderID = message.SenderID
	}
	b.hub.SendToConv(convID, wshub.ServerFrame{
		Type: wshub.FrameTypeMessage,
		Payload: wshub.MessagePayload{
			ID:          message.ID,
			ConvID:      convID,
			SenderID:    senderID,
			SenderName:  senderName,
			Content:     content,
			Images:      images,
			CreateTime:  ts,
			IsBot:       false,
			ClientMsgID: clientMsgID,
		},
	})

	// Cache the sent message for history/search.
	b.persist(ctx, &im.Message{
		ID:         message.ID,
		ConvID:     convID,
		SenderID:   senderID,
		SenderName: senderName,
		Content:    im.MessageContent{Type: "text", Text: content, Images: images},
		CreateTime: time.Unix(ts, 0),
	})
}

// UploadAsset stores an image and returns the reference that names it.
//
// It is a pass-through to the service that owns the store, with one addition:
// the owner is the caller's identity, stamped by the handler. See the handler
// for why that is the interesting part.
func (b *chatBiz) UploadAsset(
	ctx context.Context, req *agentv1.CreateAssetRequest,
) (*agentv1.Asset, error) {
	if b.assets == nil {
		return nil, errno.ErrNotConfigured.WithMessage("no agent backend is configured to store images")
	}
	asset, err := b.assets.UploadAsset(ctx, req)
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("the agent service could not store the image")
	}
	return asset, nil
}

// FetchAssetContent returns an asset's bytes and media type.
func (b *chatBiz) FetchAssetContent(ctx context.Context, assetID string) ([]byte, string, error) {
	if b.assets == nil {
		return nil, "", errno.ErrNotConfigured.WithMessage("no agent backend is configured to read images from")
	}
	data, contentType, err := b.assets.FetchAssetContent(ctx, assetID)
	if err != nil {
		return nil, "", errno.ErrInternal.WithMessage("the agent service could not read the image")
	}
	return data, contentType, nil
}

// sendImageToConv relays one attached image to an IM conversation.
//
// # Why the bytes are fetched rather than forwarded
//
// The reference names an object the *backend* stores; this service holds no
// bytes. Lark, in turn, will not accept a URL — it addresses an image by a key
// only its own upload API can issue. So the image is read from the backend and
// uploaded to Lark, and this is the one place in the message path where the
// bytes exist here at all. See chatBiz's store port for why they do not exist
// anywhere else.
func (b *chatBiz) sendImageToConv(ctx context.Context, convID string, ref media.Ref) error {
	data, _, err := b.assets.FetchAssetContent(ctx, ref.AssetID)
	if err != nil {
		return err
	}
	return b.lark.SendImage(ctx, convID, data)
}
