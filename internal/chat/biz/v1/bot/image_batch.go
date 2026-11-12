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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// imageRef identifies one inbound image (messageID + image key) so it can be
// downloaded at flush time.
type imageRef struct {
	messageID string
	imageKey  string
}

// imageBatch accumulates consecutive image messages for one session. Images
// arriving within ImageBatchWindow collapse into a single flush, mirroring
// cc-connect's #1395 handling of mobile multi-image bursts. At flush time each
// image is stored and the turn runs with the references.
type imageBatch struct {
	recv *v1.Receiver
	dr   *runtime.DynamicRuntime
	refs []imageRef // image (messageID, imageKey) pairs to store
	// text is the sentence the images arrived with, when they arrived with one.
	//
	// A rich-text post carries words and pictures in one message, and the words
	// are the question: "这个报错怎么解决？" (how to fix this error?) beside a screenshot asks about that
	// screenshot, and replacing it with the generic prompt would answer a
	// question the user did not ask. The first non-empty text in the batch wins;
	// a burst of bare images keeps the generic prompt.
	text string
	// parentID is the message the batch replies to, when one of its messages
	// does. It is recorded rather than read off dr because dr is the runtime of
	// whichever message created the entry, and a burst can open with a plain
	// picture and end with a reply.
	parentID string
	timer    *time.Timer
}

// bufferImage adds an inbound image message to its session's batch and (re)arms
// the flush timer. The timer is restarted on every arrival so a rapid burst is
// coalesced rather than flushed one message at a time.
func (b *botBiz) bufferImage(_ context.Context, dr *runtime.DynamicRuntime, mc *MsgContent) {
	recv := dr.Receiver
	if recv == nil || recv.Id == "" || len(mc.ImageKeys) == 0 {
		return
	}
	window := b.sr.ImageBatchWindow
	if window <= 0 {
		window = 500 * time.Millisecond
	}

	b.imageBatchMu.Lock()
	defer b.imageBatchMu.Unlock()

	entry := b.imageBatches[recv.Id]
	if entry == nil {
		entry = &imageBatch{recv: recv, dr: dr}
		b.imageBatches[recv.Id] = entry
	}
	if entry.parentID == "" {
		entry.parentID = recv.GetParentMessageId()
	}
	for _, key := range mc.ImageKeys {
		entry.refs = append(entry.refs, imageRef{messageID: recv.MessageId, imageKey: key})
	}
	if entry.text == "" {
		// Normalised exactly as the text path normalises it, so the same
		// sentence reaches the model the same way whichever rung carried it.
		// The raw form is not that: an image post that @-mentions the bot
		// arrives with the platform's placeholder still in its text.
		entry.text = normalizeQuestion(cleanFeishuAutoLinks(mc.Text), dr.Mentions)
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	entry.timer = time.AfterFunc(window, func() {
		b.flushImageBatch(recv.Id)
	})
}

// flushImageBatch flushes a session's pending image batch: it drops recalled
// images, stores the rest, and dispatches them to the AI turn.
func (b *botBiz) flushImageBatch(sessionKey string) {
	b.imageBatchMu.Lock()
	entry := b.imageBatches[sessionKey]
	if entry == nil {
		b.imageBatchMu.Unlock()
		return
	}
	delete(b.imageBatches, sessionKey)
	b.imageBatchMu.Unlock()

	// Drop images whose triggering message was recalled after arrival.
	var liveRefs []imageRef
	for _, ref := range entry.refs {
		if !b.isMessageRecalled(ref.messageID) {
			liveRefs = append(liveRefs, ref)
		}
	}
	if len(liveRefs) == 0 {
		return
	}

	// The flush runs detached from the inbound request (its context was already
	// stripped in the transport layer), so give it a fresh bounded context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	images := make([]media.Ref, 0, len(liveRefs))
	for _, ref := range liveRefs {
		stored, err := b.storeImage(ctx, entry.dr, ref)
		if err != nil {
			slog.WarnContext(ctx, "failed to store image for AI", "message_id", ref.messageID, "error", err)
			continue
		}
		images = append(images, stored)
	}
	if len(images) == 0 {
		if err := b.SendText(ctx, entry.recv, "图片处理失败，请重试"); err != nil {
			slog.WarnContext(ctx, "failed to notify image failure", "conv_id", sessionKey, "error", err)
		}
		return
	}

	// The question is the reply's own words; the quote is folded in after, by
	// the turn. Composing them the other way round would glue the generic
	// "describe this picture" prompt in front of a sentence that did arrive.
	// The question is the reply's own words, or the generic prompt for a bare
	// picture.
	question := strings.TrimSpace(entry.text)
	if question == "" {
		question = defaultImageQuestion
	}
	if err := b.runTurnWithQuote(ctx, entry.dr, entry.parentID, question, images); err != nil {
		slog.WarnContext(ctx, "image AI turn failed", "conv_id", sessionKey, "error", err)
	}
}

// defaultImageQuestion is what a turn asks when an image arrived with no words.
//
// It is a question rather than an instruction to transcribe: "describe this"
// answers with a description the user then has to ask a follow-up about, while
// asking what it shows invites the answer they wanted.
const defaultImageQuestion = "请分析以下图片内容。"

// storeImage downloads an image from Lark and stores it on the agent backend,
// returning the reference that names it.
//
// # Why the bytes leave this process
//
// This service has no store. The backend owns one — it is the process
// configured with a media driver, and the one that resolves a reference when a
// model needs the image — so the bytes are handed over once, here, and every
// turn after this one carries only the reference. That is the difference from
// the arrangement this replaced, where the bytes were re-sent with every turn
// of the conversation.
func (b *botBiz) storeImage(ctx context.Context, dr *runtime.DynamicRuntime, ref imageRef) (media.Ref, error) {
	data, filename, err := b.downloadImage(ctx, ref)
	if err != nil {
		return media.Ref{}, err
	}

	asset, err := b.uploadAsset(ctx, dr, filename, data)
	if err != nil {
		return media.Ref{}, err
	}
	return refFromAsset(asset, filename), nil
}

// maxLarkImageBytes bounds an image downloaded from Lark.
//
// It is a backstop, not the policy: the store's per-kind ceiling is the real
// limit and is applied to the bytes after this. It sits above every kind's
// ceiling so that limit stays the one a user meets, while still stopping a
// download that would otherwise be buffered whole before being refused.
const maxLarkImageBytes = 12 << 20 // 12 MiB

// downloadImage fetches an image message's bytes from Lark.
func (b *botBiz) downloadImage(ctx context.Context, ref imageRef) ([]byte, string, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(ref.messageID).
		FileKey(ref.imageKey).
		Type("image").
		Build()
	resp, err := b.lark().Im.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("lark: download image: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("lark: download image returned HTTP %d", resp.StatusCode)
	}

	// Read one byte past the ceiling: enough to tell an oversized file from one
	// that fits, without buffering the rest of it. The ceiling is well above any
	// image the store accepts, so a legitimate picture is never refused here —
	// and a truncated one never reaches the store, where it would be accepted as
	// a smaller image and shown to the model as half a screenshot.
	body, err := io.ReadAll(io.LimitReader(resp.File, maxLarkImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("lark: read image: %w", err)
	}
	if len(body) > maxLarkImageBytes {
		return nil, "", fmt.Errorf("lark: image %s exceeds %d bytes", ref.imageKey, maxLarkImageBytes)
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("lark: image %s is empty", ref.imageKey)
	}
	// The filename decides the stored object's extension. Lark does not always
	// supply one and never supplies a usable content type here, so the extension
	// is the only hint the store gets; the type itself is sniffed from the bytes
	// on the other side.
	return body, resp.FileName, nil
}

// uploadAsset hands the bytes to the agent backend.
func (b *botBiz) uploadAsset(
	ctx context.Context, dr *runtime.DynamicRuntime, filename string, data []byte,
) (*agentv1.Asset, error) {
	if b.sr.Clientset == nil || b.sr.Clientset.Agent() == nil {
		return nil, errors.New("no agent backend is configured to store images")
	}
	// The identity an image is stored under is the speaker's stable id, not a
	// display name, so the ownership check on a later read compares the same
	// value the upload recorded. An unresolved speaker falls back to the
	// conversation: the bytes still need an owner.
	owner := dr.Identity()
	if owner == "" {
		owner = dr.Address()
	}
	return b.sr.Clientset.Agent().UploadAsset(ctx, &agentv1.CreateAssetRequest{
		Kind:     media.KindImage,
		OwnerID:  owner,
		Filename: filename,
		Data:     base64.StdEncoding.EncodeToString(data),
		Source:   media.SourceLark,
	})
}

// refFromAsset turns a stored asset into the reference the turn carries.
func refFromAsset(a *agentv1.Asset, filename string) media.Ref {
	if a == nil {
		return media.Ref{}
	}
	name := a.GetName()
	if name == "" {
		name = filename
	}
	return media.Ref{
		AssetID:  a.GetAssetId(),
		URL:      a.GetUrl(),
		MimeType: a.GetMimeType(),
		Size:     a.GetSize(),
		Width:    int(a.GetWidth()),
		Height:   int(a.GetHeight()),
		Name:     name,
		Source:   media.SourceLark,
	}
}
