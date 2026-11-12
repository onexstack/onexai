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

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// flusher is the ad-hoc interface both production call sites assert to. The
// tests assert to the same one so a change to the real call sites that stopped
// matching would not leave them passing against a method nobody calls.
type flusher interface {
	Flush(context.Context) error
}

var errPlatformRejected = errors.New("platform rejected the message")

// fakeLark records outbound text. failAt is the zero-based index of the send
// that should fail, or -1 for "never fail" — the failure has to be injectable
// because a send that cannot fail cannot show that failures are now surfaced.
type fakeLark struct {
	sent        []string
	markdowns   []string
	interactive []any
	chatNames   []string
	receiver    *v1.Receiver
	failAt      int
}

func (f *fakeLark) SendText(_ context.Context, recv *v1.Receiver, content string) error {
	if f.failAt >= 0 && len(f.sent) == f.failAt {
		return errPlatformRejected
	}
	f.sent = append(f.sent, content)
	f.receiver = recv
	return nil
}

func (f *fakeLark) SendCard(context.Context, *v1.Receiver, string, map[string]interface{}) error {
	return nil
}

func (f *fakeLark) SendMarkdownCard(_ context.Context, _ *v1.Receiver, title, content, _ string) error {
	f.markdowns = append(f.markdowns, title+"\n"+content)
	return nil
}

func (f *fakeLark) SendTextAndGetID(context.Context, *v1.Receiver, string) (string, error) {
	return "", nil
}

func (f *fakeLark) PatchCard(context.Context, string, string, map[string]interface{}) error {
	return nil
}

func (f *fakeLark) CreateChat(context.Context, string, string, *v1.Watcher) (string, error) {
	return "", nil
}

func (f *fakeLark) InviteMembers(context.Context, string, string, []string) error { return nil }

// ResolveUserName answers with a fixed name so tests can assert on it without a
// contact API.
func (f *fakeLark) ResolveUserName(_ context.Context, userID string) string {
	return "name-of-" + userID
}

func (f *fakeLark) UpdateChatName(_ context.Context, chatID, name string) error {
	f.chatNames = append(f.chatNames, chatID+"="+name)
	return nil
}

func (f *fakeLark) UpdateChatAvatar(context.Context, string, string) error { return nil }

func (f *fakeLark) CreateStreamingCard(context.Context, *v1.Receiver, string, string, bool, string) (string, error) {
	return "", nil
}

func (f *fakeLark) UpdateCardText(context.Context, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) UpdateCardReasoningText(context.Context, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) UpdateCardProgressText(context.Context, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) CloseStreamingCard(context.Context, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) UpdateCardElementText(context.Context, string, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) InsertCardElements(context.Context, string, string, *stream.Sequence, ...map[string]any) error {
	return nil
}

func (f *fakeLark) DeleteCardElements(context.Context, string, *stream.Sequence, ...string) error {
	return nil
}

func (f *fakeLark) SetCardSummary(context.Context, string, string, *stream.Sequence) error {
	return nil
}

func (f *fakeLark) GetMessage(context.Context, string) (string, error) { return "", nil }

func (f *fakeLark) GetRecentMessages(context.Context, *v1.Receiver, int) (string, error) {
	return "", nil
}

func (f *fakeLark) SendRichText(context.Context, *v1.Receiver, *RichTextContent) error { return nil }

func (f *fakeLark) SendInteractiveCard(_ context.Context, recv *v1.Receiver, content interface{}) error {
	f.interactive = append(f.interactive, content)
	f.receiver = recv
	return nil
}

func (f *fakeLark) SendImage(context.Context, *v1.Receiver, []byte) error { return nil }

func (f *fakeLark) SendFile(context.Context, *v1.Receiver, string, string, []byte) error {
	return nil
}

func (f *fakeLark) AddReaction(context.Context, string, string) (string, error) { return "", nil }

func (f *fakeLark) RemoveReaction(context.Context, string, string) error { return nil }

func newTestResponder(f *fakeLark, recv *v1.Receiver) Responder {
	return NewLarkResponder(&DynamicRuntime{LarkServices: f, Receiver: recv})
}

func TestFlushNoopWhenEmpty(t *testing.T) {
	f := &fakeLark{failAt: -1}
	r := newTestResponder(f, &v1.Receiver{Id: "oc_empty"})

	if err := r.(flusher).Flush(context.Background()); err != nil {
		t.Fatalf("Flush on an empty buffer: %v", err)
	}
	if len(f.sent) != 0 {
		t.Errorf("sent %d messages for an empty buffer, want none", len(f.sent))
	}
}

// TestSendResultPrefersTheInteractiveCard pins the rendering order the /pod card
// depends on: when a command supplies both a card and a markdown fallback, the
// Lark surface sends the card — one navigable message instead of the wall of
// text the fallback would be — and the card goes out with the session key the
// callback needs to find this conversation.
func TestSendResultPrefersTheInteractiveCard(t *testing.T) {
	f := &fakeLark{failAt: -1}
	recv := &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_pod"}
	r := newTestResponder(f, recv)

	res := &CommandResult{
		Markdown:  &MarkdownContent{Title: "Pod 信息：gateway", Content: "## Pod：`gateway-1`", Color: "blue"},
		PlainText: "## Pod：`gateway-1`",
		// The /pod card as it is built: no title bar, a selector over the matched
		// pods, and the selected pod's rows. The selector is the control the
		// callback has to route back here, so it is what has to carry the key.
		Interactive: larkcard.NewCard().
			Select("选择 Pod", []larkcard.CardSelectOption{
				{Text: "prod/gateway-1", Value: "nav:pod:sel:tok:0"},
				{Text: "test/gateway-2", Value: "nav:pod:sel:tok:1"},
			}, "nav:pod:sel:tok:0").
			Build(),
	}
	if err := r.SendResult(context.Background(), res); err != nil {
		t.Fatalf("SendResult: %v", err)
	}

	if len(f.interactive) != 1 {
		t.Fatalf("sent %d interactive card(s), want 1", len(f.interactive))
	}
	if len(f.markdowns) != 0 {
		t.Errorf("sent %d markdown card(s) alongside the interactive one, want none", len(f.markdowns))
	}
	if f.receiver != recv {
		t.Error("the card went to the wrong receiver")
	}

	rendered, err := json.Marshal(f.interactive[0])
	if err != nil {
		t.Fatalf("marshalling the sent card: %v", err)
	}
	if !strings.Contains(string(rendered), `"session_key":"oc_pod"`) {
		t.Errorf("sent card = %s, want the session key injected for the callback", rendered)
	}

	// Without a card, the markdown fallback is still what goes out.
	f2 := &fakeLark{failAt: -1}
	r2 := newTestResponder(f2, recv)
	if err := r2.SendResult(context.Background(), &CommandResult{Markdown: res.Markdown}); err != nil {
		t.Fatalf("SendResult: %v", err)
	}
	if len(f2.markdowns) != 1 {
		t.Errorf("sent %d markdown cards without an interactive card, want 1", len(f2.markdowns))
	}
}

// TestFlushChunksLongOutput is the behaviour the old single-send did not have:
// output past the budget goes out as several ordered messages, and reassembling
// them reproduces exactly what was written.
func TestFlushChunksLongOutput(t *testing.T) {
	f := &fakeLark{failAt: -1}
	recv := &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_long"}
	r := newTestResponder(f, recv)

	long := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 1000)
	if _, err := r.Write([]byte(long)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := r.(flusher).Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if len(f.sent) < 2 {
		t.Fatalf("sent %d message(s) for %d bytes, want it split", len(f.sent), len(long))
	}
	for i, m := range f.sent {
		if len(m) > larkcard.TextChunkBytes {
			t.Errorf("message %d is %d bytes, over the %d budget", i, len(m), larkcard.TextChunkBytes)
		}
	}
	if got := strings.Join(f.sent, ""); got != long {
		t.Errorf("reassembled %d bytes, want %d — content was lost or duplicated", len(got), len(long))
	}
	if f.receiver != recv {
		t.Errorf("SendText received the wrong receiver")
	}
}

// TestFlushReportsErrorAfterPartialSend is the half that makes the first half
// worth having. Before this, both call sites wrote `_ = flusher.Flush(ctx)`,
// so a rejected send produced no message and no signal anywhere.
func TestFlushReportsErrorAfterPartialSend(t *testing.T) {
	f := &fakeLark{failAt: 1} // first send succeeds, second is rejected
	r := newTestResponder(f, &v1.Receiver{Id: "oc_partial"})

	// A single-line body long enough that the fence-free splitter cuts it into
	// several pieces, so the failure lands after a successful send.
	if _, err := r.Write([]byte(strings.Repeat("x", larkcard.TextChunkBytes*3))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	err := r.(flusher).Flush(context.Background())

	if err == nil {
		t.Fatal("Flush returned nil after a send failure — the failure would be invisible")
	}
	if !errors.Is(err, errPlatformRejected) {
		t.Errorf("error does not wrap the send failure: %v", err)
	}
	if !strings.Contains(err.Error(), "1 of") {
		t.Errorf("error does not say how much was delivered: %v", err)
	}
	if len(f.sent) != 1 {
		t.Errorf("sent %d messages, want exactly 1 before the failure stops the rest", len(f.sent))
	}

	// The buffer is drained up front, so a second flush is a no-op rather than
	// a resend of what already went out.
	if err := r.(flusher).Flush(context.Background()); err != nil {
		t.Fatalf("Flush on the drained buffer: %v", err)
	}
	if len(f.sent) != 1 {
		t.Errorf("sent %d messages in total, want 1 — drained-then-report was broken", len(f.sent))
	}
}
