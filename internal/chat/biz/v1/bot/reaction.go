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

package bot

import (
	"context"
	"fmt"
	"log/slog"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// Reaction emoji types understood by Lark's message-reaction API. "OnIt" is the
// standard "working on it" indicator, "DONE" the completion marker.
const (
	emojiOnIt = "OnIt"
	emojiDone = "DONE"
)

// AddReaction attaches an emoji reaction to a message and returns its reaction
// id. Reactions are the only way to signal in-progress state on a message that
// the bot has not yet answered, so it doubles as a lightweight typing indicator.
func (b *botBiz) AddReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	if messageID == "" || emojiType == "" {
		return "", fmt.Errorf("add reaction: message_id and emoji_type are required")
	}

	var reactionID string
	err := b.retrier.Do(ctx, "add reaction", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		req := larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(&larkim.Emoji{EmojiType: &emojiType}).
				Build()).
			Build()
		resp, err := client.Im.MessageReaction.Create(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("add reaction: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("add reaction failed, code=%d, msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.ReactionId != nil {
			reactionID = *resp.Data.ReactionId
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return reactionID, nil
}

// RemoveReaction removes a previously-added reaction from a message. It is
// best-effort for callers: a missing reaction (already removed, or the message
// was recalled) is not an error worth surfacing, so callers ignore the result.
func (b *botBiz) RemoveReaction(ctx context.Context, messageID, reactionID string) error {
	if messageID == "" || reactionID == "" {
		return fmt.Errorf("remove reaction: message_id and reaction_id are required")
	}

	return b.retrier.Do(ctx, "remove reaction", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		req := larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build()
		resp, err := client.Im.MessageReaction.Delete(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("remove reaction: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("remove reaction failed, code=%d, msg=%s", resp.Code, resp.Msg)
		}
		return nil
	})
}

// withProcessingReaction runs fn with an "OnIt" reaction on the triggering
// message, then swaps it for a "DONE" reaction once fn returns. It is a no-op
// (and never fails the wrapped work) when there is no triggering message, and
// all reaction calls are best-effort so a transient reaction failure cannot
// mask a command result.
func (b *botBiz) withProcessingReaction(ctx context.Context, recv *v1.Receiver, fn func() error) error {
	msgID := ""
	if recv != nil {
		msgID = recv.MessageId
	}
	if msgID == "" {
		return fn()
	}

	// The reaction reports state; it does not decide the answer, so it must not
	// decide when the answer arrives. It used to, twice: an OnIt round trip sat
	// between the message and the work, and the swap to DONE ran before the
	// caller returned. For a command whose whole body is a string join, those
	// three Lark calls were its largest fixed cost.
	//
	// One goroutine owns the sequence, so "OnIt, then DONE" still holds; the
	// command only waits for fn. The context is detached because the request may
	// be done before the swap is, and a cancelled context would turn the DONE
	// into a silent failure.
	bg := context.WithoutCancel(ctx)
	finished := make(chan struct{})
	runtime.GoSafe(bg, "lark.reaction.lifecycle", func() {
		reactionID, err := b.AddReaction(bg, msgID, emojiOnIt)
		if err != nil {
			slog.DebugContext(bg, "failed to add processing reaction", "message_id", msgID, "error", err)
		}
		<-finished
		if reactionID != "" {
			if err := b.RemoveReaction(bg, msgID, reactionID); err != nil {
				slog.DebugContext(bg, "failed to remove processing reaction", "message_id", msgID, "error", err)
			}
		}
		if _, err := b.AddReaction(bg, msgID, emojiDone); err != nil {
			slog.DebugContext(bg, "failed to add done reaction", "message_id", msgID, "error", err)
		}
	})

	defer close(finished)
	return fn()
}
