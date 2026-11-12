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
	"slices"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/media"
)

// A reply is a message plus what it points at.
//
// The platform hands this process the reply and, separately, the id of the
// message it answers (Lark's parent_id). What that message said is not in the
// event, so a question like "这个怎么解决？" (how do I solve this?) arrives with no referent at all: the
// subject of the sentence is precisely the part the platform did not send. This
// file is where the referent is fetched, rendered and folded back into the turn.
//
// # Why the resolution happens here and not in the ladder
//
// funnel.go decides a message's fate without touching the platform — that is
// what makes the ladder readable and testable, and it is what lets a rung that
// drops a message cost nothing. A reply's body is already in hand, so the ladder
// can decide allowlist, addressability and command-ness without knowing what was
// quoted; fetching there would pay a platform round trip for messages the ladder
// then drops. The fetch therefore belongs to the two handoffs that actually start
// a turn.
const (
	// quoteFetchTimeout bounds the whole chain walk. The context an inbound
	// request runs under is deliberately detached from cancellation
	// (handler/bot.go), so without a bound of its own a stalled platform call
	// would keep the turn — and the card that promises an answer — from ever
	// starting. It bounds the walk, not each hop, which is why it is generous
	// enough for several.
	quoteFetchTimeout = 5 * time.Second

	// maxQuoteDepth bounds how far up a chain of replies is followed. Lark
	// answers with the message a reply directly points at, so a chain is built
	// by walking parent_id. The bound is the same one the history path used.
	maxQuoteDepth = 10

	// maxQuotedTextRunes bounds the quoted words a turn carries, in runes
	// rather than bytes: the content is mostly Chinese, and slicing by byte
	// would cut a character in half.
	maxQuotedTextRunes = 20000

	// maxQuotedImages bounds how many quoted pictures ride along with a turn.
	maxQuotedImages = 10
)

// quoteTruncatedMarker is appended to quoted text that was cut to fit the
// budget, so a reader of the prompt can tell a short quote from a clipped one.
const quoteTruncatedMarker = "\n… [引用内容过长，已截断]"

// defaultQuotedQuestion is what a reply that carries no words of its own is
// asked with. A reply of nothing but the bot's @mention is a question about
// what it quotes, and the quoted words alone are not an instruction.
const defaultQuotedQuestion = "请结合上面引用的消息回答。"

// quotedLevel is one message in the chain a reply quotes: the words it carried,
// and references to its pictures.
//
// The image references are bound to the message they came from rather than to
// the reply, because that is the id the platform needs to hand the bytes back —
// a key read with the reply's id is refused.
type quotedLevel struct {
	text   string
	images []imageRef
}

// resolveQuoted reads the messages quotedID points at, following parent_id, and
// returns them oldest-first.
//
// # Best-effort by construction
//
// Nothing here returns an error, and nothing here fails a turn. A quoted message
// that was recalled, deleted, or that this app may not read is a reason to answer
// without it, never a reason to answer nothing — the user's own words are still
// there, and they are the thing being asked. Every degradation is logged and
// counted (see RecordQuotedReply) so that "the bot ignored what I replied to" is
// answerable from a metric rather than only from a reproduction.
func (b *botBiz) resolveQuoted(ctx context.Context, quotedID string) []quotedLevel {
	// Not a reply at all: nothing to resolve, and nothing to count. The metric
	// is about replies, so a plain message must not appear in its denominator.
	if quotedID == "" {
		return nil
	}
	if b.isMessageRecalled(quotedID) {
		metrics.M.RecordQuotedReply(ctx, "empty")
		return nil
	}
	// A process with no platform connection — the console path — cannot read a
	// quoted message. That is not a failure of the reply and is not counted as
	// one; it is simply a surface where quoting does not exist.
	if b.sr.Clientset == nil || b.sr.Clientset.L() == nil || b.lark() == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()

	var (
		levels    []quotedLevel
		remaining = maxQuotedTextRunes
		truncated bool
		failed    bool
	)
	for id, depth := quotedID, 0; id != "" && depth < maxQuoteDepth; depth++ {
		// Nothing further can fit, so the walk stops rather than spending a
		// round trip per remaining level to be told the same thing.
		if remaining <= 0 {
			truncated = true
			break
		}
		item, err := b.getMessage(ctx, id)
		if err != nil {
			slog.WarnContext(ctx, "could not read a quoted message", "message_id", id, "error", err)
			failed = true
			break
		}
		// The platform answered, but with nothing usable: a recalled or
		// body-less message. A chain that reaches one simply stops here.
		if item == nil {
			break
		}

		level := quotedLevelFrom(id, item)
		// A message this process can make nothing of contributes nothing, and
		// the chain carries on past it rather than leaving a blank level for a
		// reader to wonder about.
		if level.text == "" && len(level.images) == 0 {
			id = ptr.FromOr(item.ParentId, "")
			continue
		}
		if runes := []rune(level.text); len(runes) > remaining {
			level.text = string(runes[:remaining]) + quoteTruncatedMarker
			truncated = true
			remaining = 0
		} else {
			remaining -= len(runes)
		}
		levels = append(levels, level)
		id = ptr.FromOr(item.ParentId, "")
	}

	switch {
	case len(levels) == 0 && failed:
		metrics.M.RecordQuotedReply(ctx, "failed")
		return nil
	case len(levels) == 0:
		metrics.M.RecordQuotedReply(ctx, "empty")
		return nil
	case truncated:
		metrics.M.RecordQuotedReply(ctx, "truncated")
	default:
		metrics.M.RecordQuotedReply(ctx, "resolved")
	}

	// Collected newest-first; oldest-first reads the way the conversation did.
	slices.Reverse(levels)
	return levels
}

// getMessage reads one message by id.
//
// It reports an error only for a call that failed — a transport error, or a
// refusal by the platform. A nil message with a nil error means the platform
// answered and there is nothing to use: the message was recalled, carried no
// body, or was not returned. The caller treats both the same way, but only the
// first is worth a warning, and the difference is why this returns two values.
func (b *botBiz) getMessage(ctx context.Context, messageID string) (*larkim.Message, error) {
	// Asked for the card's own JSON rather than the platform's default answer.
	// Left out, a card comes back as the degraded rendering — its visible lines,
	// a screenshot, and a "请升级至最新版本客户端" (please upgrade to the latest client) notice — which reads as a card
	// nobody wrote. The parameter changes nothing for a message that is not a
	// card, so it is asked for unconditionally.
	req := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		CardMsgContentType(larkcard.CardContentTypeUser).
		Build()
	resp, err := b.lark().Im.Message.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark: get message %s: %w", messageID, err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark: get message %s failed, error=%s", messageID, larkcore.Prettify(resp.CodeError))
	}
	// Every field below is optional in the platform's own schema: a response
	// that is successful and empty is a shape this has to survive rather than
	// dereference.
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return nil, nil
	}
	item := resp.Data.Items[0]
	if ptr.FromOr(item.Deleted, false) || item.Body == nil || item.Body.Content == nil {
		return nil, nil
	}
	return item, nil
}

// quotedLevelFrom renders one fetched message as a level of the chain.
//
// id is the id that was asked for, used when the response omits its own: the
// pictures are downloaded with it, so an empty one here would silently lose
// them.
func quotedLevelFrom(id string, item *larkim.Message) quotedLevel {
	mc := parseMessageWithMentions(ptr.From(item.MsgType), ptr.From(item.Body.Content), item.Mentions)
	if mc == nil {
		return quotedLevel{}
	}

	level := quotedLevel{text: strings.TrimSpace(cleanFeishuAutoLinks(mc.Text))}
	// A level that is only a picture says so. An empty line in the chain would
	// read as a message nobody sent, rather than as one this process chose not
	// to describe.
	if level.text == "" && len(mc.ImageKeys) > 0 {
		level.text = "[图片]"
	}

	messageID := ptr.FromOr(item.MessageId, id)
	for _, key := range mc.ImageKeys {
		level.images = append(level.images, imageRef{messageID: messageID, imageKey: key})
	}
	return level
}

// formatQuotedChain renders the chain as the block a model reads. It is a pure
// function so the rendering can be read and tested without a platform
// connection, and there is exactly one of it so a quoted chain reads the same
// wherever it appears.
func formatQuotedChain(levels []quotedLevel) string {
	if len(levels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[引用消息]\n")
	for i, level := range levels {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "--- 第 %d 层消息 ---\n%s\n", i+1, level.text)
	}
	b.WriteString("[/引用消息]")
	return b.String()
}

// composeQuotedQuestion folds the quoted chain and the reply's own words into
// the single question a turn runs with.
//
// The quote goes first and is labelled, because it is context and the reply is
// the instruction; and because quoted text is somebody else's words, arriving
// through a channel the model should read as reference rather than as a
// request. A reply with no words of its own is still a question about what it
// quotes, so it gets the fallback instruction rather than an empty prompt.
func composeQuotedQuestion(chain, question string) string {
	if chain == "" {
		return question
	}
	if strings.TrimSpace(question) == "" {
		question = defaultQuotedQuestion
	}
	return chain + "\n\n【我的消息】\n" + question
}

// storeQuotedImages hands the quoted pictures to the agent backend and returns
// the references that name them, oldest level first.
//
// A picture that cannot be stored is skipped rather than failing the turn: the
// words are what the question is about, and losing one of several pictures is
// not a reason to lose the words. The budget is applied here rather than in the
// resolver because a level's pictures are only worth spending on once a turn is
// actually starting.
func (b *botBiz) storeQuotedImages(
	ctx context.Context, dr *runtime.DynamicRuntime, levels []quotedLevel,
) []media.Ref {
	var refs []media.Ref
	for _, level := range levels {
		for _, ref := range level.images {
			if len(refs) >= maxQuotedImages {
				return refs
			}
			stored, err := b.storeImage(ctx, dr, ref)
			if err != nil {
				slog.WarnContext(ctx, "could not store a quoted image",
					"message_id", ref.messageID, "image_key", ref.imageKey, "error", err)
				continue
			}
			refs = append(refs, stored)
		}
	}
	return refs
}

// runTurnWithQuote starts the turn an inbound message asks for, with the content
// of the message it replies to folded in.
//
// It is the single entry point for such a turn, so a reply behaves the same
// whether it carried words, a picture, or both — and so the one place that
// reaches for the quoted message is a place every AI turn from an inbound
// message goes through. A message that quotes nothing skips all of it: the
// resolver returns before it makes a call.
func (b *botBiz) runTurnWithQuote(
	ctx context.Context, dr *runtime.DynamicRuntime, quotedID, question string, images []media.Ref,
) error {
	levels := b.resolveQuoted(ctx, quotedID)
	if chain := formatQuotedChain(levels); chain != "" {
		question = composeQuotedQuestion(chain, question)
		images = append(images, b.storeQuotedImages(ctx, dr, levels)...)
	} else if strings.TrimSpace(question) == "" {
		// A reply of nothing but the bot's @mention, whose quote could not be
		// read. There is no question in it and nothing to answer with, and the
		// ladder has already counted it as handled — so this is the only place
		// that can record that the user got silence. RecordQuotedReply has
		// already counted the resolution failure; this line is what says the
		// failure cost the turn.
		convID := ""
		if dr != nil && dr.Receiver != nil {
			convID = dr.Address()
		}
		slog.WarnContext(ctx, "a reply carried no words of its own and its quote could not be read",
			"conv_id", convID, "quoted_message_id", quotedID)
		return nil
	}
	return b.runAI(ctx, dr, question, images)
}
