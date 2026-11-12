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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// quoteReceiver is the conversation every test here runs in. A chat id rather
// than a user id, so the receiver is a group and the mirroring paths that are
// keyed by group stay on their ordinary branch.
func quoteReceiver() *v1.Receiver {
	return &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_quote_test"}
}

// ---------- the platform, faked ----------

// fakeLarkServer answers the three calls a quoted message costs: the tenant
// token the SDK asks for before anything else, a message read, and an image
// download. It records what was asked for, because "which message id was the
// picture read with" is precisely the fact this feature has to get right — a key
// read with the reply's id is refused by the platform, and a test that only
// checked the bytes would not notice.
type fakeLarkServer struct {
	ts *httptest.Server

	mu sync.Mutex
	// messages is what a read returns, by message id. An id that is absent is a
	// successful read of nothing, which is what the platform does for a message
	// this app may not see.
	messages map[string]*fakeLarkMessage
	// failReads are ids whose read fails with a platform error.
	failReads map[string]bool
	// files is what a download returns, by "<message id>/<image key>".
	files map[string][]byte
	// reads records every message id read, in order.
	reads []string
	// cardContentTypes records the card_msg_content_type each read carried, in
	// the same order as reads. A read of a card is only as good as this
	// parameter: without it the platform answers the degraded rendering, and a
	// test that only looked at the text would not notice the difference.
	cardContentTypes []string
	// downloads records every "<message id>/<image key>" downloaded, in order.
	downloads []string
	// block, when non-nil, makes every read wait until it is closed. It is how
	// the fetch timeout is exercised without a real outage.
	block chan struct{}
}

// fakeLarkMessage is one message as the platform would report it. A zero value
// answers successfully with nothing to read, which is the shape a recalled or
// body-less message has.
type fakeLarkMessage struct {
	msgType  string
	content  string
	parentID string
	mentions []*larkim.Mention
	deleted  bool
	bodyless bool
}

func newFakeLarkServer(t *testing.T) *fakeLarkServer {
	t.Helper()
	s := &fakeLarkServer{
		messages:  map[string]*fakeLarkMessage{},
		failReads: map[string]bool{},
		files:     map[string][]byte{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

// client returns the typed Lark client, pointed at this server. It is the same
// construction production uses, so the SDK's own request building and error
// handling are part of what these tests exercise rather than replaced.
func (s *fakeLarkServer) client() *fakeLarkClient {
	return &fakeLarkClient{c: lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(s.ts.URL))}
}

func (s *fakeLarkServer) serve(w http.ResponseWriter, r *http.Request) {
	// The SDK authenticates first, and a refused token makes every later call
	// fail before it is built — so this one has to answer.
	if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
		writeLarkJSON(w, `{"code":0,"msg":"ok","tenant_access_token":"t-1","expire":7200}`, 0)
		return
	}

	const prefix = "/open-apis/im/v1/messages/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == r.URL.Path {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]

	switch {
	case len(parts) == 1:
		if s.block != nil {
			<-s.block
		}
		s.mu.Lock()
		s.reads = append(s.reads, id)
		s.cardContentTypes = append(s.cardContentTypes, r.URL.Query().Get("card_msg_content_type"))
		s.mu.Unlock()
		if s.failReads[id] {
			writeLarkJSON(w, `{"code":230002,"msg":"message not found"}`, 230002)
			return
		}
		message := s.messages[id]
		if message == nil {
			writeLarkJSON(w, `{"items":[]}`, 0)
			return
		}
		writeLarkJSON(w, fmt.Sprintf(`{"items":[%s]}`, message.json(id)), 0)
	case len(parts) == 3 && parts[1] == "resources":
		key := parts[2]
		s.mu.Lock()
		s.downloads = append(s.downloads, id+"/"+key)
		s.mu.Unlock()
		data := s.files[id+"/"+key]
		if data == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// readsSeen and downloadsSeen return copies, so an assertion never races the
// handler goroutine that produced them.
func (s *fakeLarkServer) readsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

// cardContentTypesSeen returns the card_msg_content_type of every read, in the
// order the reads were made.
func (s *fakeLarkServer) cardContentTypesSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cardContentTypes...)
}

func (s *fakeLarkServer) downloadsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.downloads...)
}

// json renders one message the way the platform's read response carries it.
func (m *fakeLarkMessage) json(id string) string {
	message := map[string]any{"message_id": id, "msg_type": m.msgType}
	if m.parentID != "" {
		message["parent_id"] = m.parentID
	}
	if m.deleted {
		message["deleted"] = true
	}
	if !m.bodyless {
		message["body"] = map[string]any{"content": m.content}
	}
	if len(m.mentions) > 0 {
		message["mentions"] = m.mentions
	}
	b, _ := json.Marshal(message)
	return string(b)
}

// writeLarkJSON writes a response in the envelope the SDK expects. code is
// carried inside the body as well, because that is where the SDK reads it.
func writeLarkJSON(w http.ResponseWriter, data string, code int) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"code":%d,"msg":"x","data":%s}`, code, data)
}

// fakeLarkClient is the typed Lark client, which exposes exactly one method.
type fakeLarkClient struct{ c *lark.Client }

func (f *fakeLarkClient) C() *lark.Client { return f.c }

// quoteBiz is a bot with a platform connection and nothing else, which is all
// the resolution path touches.
func quoteBiz(t *testing.T, s *fakeLarkServer, recalled ...string) *botBiz {
	t.Helper()
	killed := make(map[string]time.Time, len(recalled))
	for _, id := range recalled {
		killed[id] = time.Now()
	}
	return &botBiz{
		sr:       &runtime.StaticRuntime{Clientset: &fakeClientset{lark: s.client()}},
		recalled: killed,
	}
}

// ---------- the pure half ----------

func TestComposeQuotedQuestion(t *testing.T) {
	tests := []struct {
		name     string
		chain    string
		question string
		want     string
	}{
		{name: "no quote leaves the question alone", question: "这个怎么解决", want: "这个怎么解决"},
		{name: "a quote is context beside the words", chain: "[引用消息]\n出错了", question: "怎么解决",
			want: "[引用消息]\n出错了\n\n【我的消息】\n怎么解决"},
		{
			// The case the whole feature exists for: a reply that is only the
			// bot's @mention. Normalisation leaves nothing behind, and the quote
			// is the only thing that says what is being asked about.
			name: "a quote with no words of its own gets an instruction", chain: "[引用消息]\n出错了",
			want: "[引用消息]\n出错了\n\n【我的消息】\n" + defaultQuotedQuestion,
		},
		{name: "whitespace is not words", chain: "c", question: "  \n ", want: "c\n\n【我的消息】\n" + defaultQuotedQuestion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeQuotedQuestion(tt.chain, tt.question); got != tt.want {
				t.Errorf("composeQuotedQuestion() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestFormatQuotedChain(t *testing.T) {
	if got := formatQuotedChain(nil); got != "" {
		t.Errorf("formatQuotedChain(nil) = %q, want empty", got)
	}
	// The order is the resolver's, and the resolver hands them over oldest-first
	// so the block reads the way the conversation did.
	got := formatQuotedChain([]quotedLevel{{text: "最早"}, {text: "较近"}})
	want := "[引用消息]\n--- 第 1 层消息 ---\n最早\n\n--- 第 2 层消息 ---\n较近\n[/引用消息]"
	if got != want {
		t.Errorf("formatQuotedChain() =\n%q\nwant\n%q", got, want)
	}
}

func TestParseMessageWithMentions(t *testing.T) {
	// The placeholder the platform reported is what is substituted — not the one
	// derived from the mention's position. The two agree in practice, and where
	// they do not the platform's own is the one that is really in the text.
	got := parseMessageWithMentions("text", `{"text":"@_user_2 看下这个"}`,
		[]*larkim.Mention{{Key: ptr.To("@_user_2"), Name: ptr.To("go.kong")}})
	if got == nil || got.Text != "@go.kong 看下这个" {
		t.Errorf("Text = %v, want the mention name substituted by its own key", got)
	}

	// A mention with no key falls back to the index-derived placeholder, which
	// is the form the platform uses for the message types that omit it.
	got = parseMessageWithMentions("text", `{"text":"@_user_1 在吗"}`,
		[]*larkim.Mention{{Name: ptr.To("李四")}})
	if got == nil || got.Text != "@李四 在吗" {
		t.Errorf("Text = %v, want the index-derived placeholder substituted", got)
	}

	// A mention nobody can name is left as it stands rather than becoming a
	// bare "@", which would read as something the user never wrote.
	got = parseMessageWithMentions("text", `{"text":"@_user_1 在吗"}`, []*larkim.Mention{{Key: ptr.To("@_user_1")}})
	if got == nil || got.Text != "@_user_1 在吗" {
		t.Errorf("Text = %v, want the placeholder left alone", got)
	}

	// An image-only body has no text to rewrite and its keys must survive: they
	// are what the picture is fetched by.
	got = parseMessageWithMentions("image", `{"image_key":"img_1"}`,
		[]*larkim.Mention{{Key: ptr.To("@_user_1"), Name: ptr.To("go.kong")}})
	if got == nil || len(got.ImageKeys) != 1 || got.ImageKeys[0] != "img_1" {
		t.Errorf("ImageKeys = %v, want the picture kept", got)
	}
}

func TestQuotedLevelFrom(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		message   *fakeLarkMessage
		itemID    string
		wantText  string
		wantImgs  []string
		wantBound string
	}{
		{
			name: "text", message: &fakeLarkMessage{msgType: "text", content: `{"text":"报错了"}`},
			wantText: "报错了",
		},
		{
			// Words and pictures in one message: both are kept, which is what a
			// screenshot in a rich-text post used to lose.
			name: "rich text keeps its pictures",
			message: &fakeLarkMessage{msgType: "post", content: `{"zh_cn":{"content":[[` +
				`{"tag":"text","text":"看这个"},{"tag":"img","image_key":"img_a"},{"tag":"img","image_key":"img_b"}]]}}`},
			wantText: "看这个", wantImgs: []string{"img_a", "img_b"},
		},
		{
			// A quoted message that is only a picture must still say something,
			// or the chain has a silent gap where a message was.
			name:     "a bare picture says so",
			message:  &fakeLarkMessage{msgType: "image", content: `{"image_key":"img_c"}`},
			wantText: "[图片]", wantImgs: []string{"img_c"},
		},
		{
			// The image is bound to the message it came from: the platform hands
			// the bytes back by that id, and would refuse the reply's.
			name:    "a picture is bound to its own message",
			message: &fakeLarkMessage{msgType: "image", content: `{"image_key":"img_c"}`},
			id:      "om_asked_for", itemID: "om_reported",
			wantText: "[图片]", wantImgs: []string{"img_c"}, wantBound: "om_reported",
		},
		{
			// A card is a message too, and its text is what a follow-up about it
			// is asking about. The rendering is the card model's own — the same
			// one the TUI mirror shows — so the title is a markdown heading and
			// the body follows it.
			name: "a legacy card is read",
			message: &fakeLarkMessage{msgType: "interactive", content: `{"header":{"title":{"tag":"plain_text","content":"结论"}},` +
				`"elements":[{"tag":"div","text":{"tag":"lark_md","content":"磁盘满了"}}]}`},
			wantText: "**结论**\n\n磁盘满了",
		},
		{
			// Card 2.0 keeps its body one level deeper, which is the shape the
			// read API answers with once the card's own JSON is asked for.
			name: "a schema 2.0 card is read",
			message: &fakeLarkMessage{msgType: "interactive", content: `{"schema":"2.0","body":{"elements":[` +
				`{"tag":"markdown","content":"**Pod**：running"},` +
				`{"tag":"column_set","columns":[{"tag":"column","elements":[{"tag":"markdown","content":"重启 3 次"}]}]}]}}`},
			wantText: "**Pod**：running\n\n重启 3 次",
		},
		{
			// A picture on a card is content as well, and rides along the same
			// way a picture in a post does — keyed by the message it came from.
			name: "a card's picture rides along",
			message: &fakeLarkMessage{msgType: "interactive", content: `{"schema":"2.0","body":{"elements":[` +
				`{"tag":"markdown","content":"看这张图"},{"tag":"img","img_key":"img_card"}]}}`},
			wantText: "看这张图\n\n[图片]", wantImgs: []string{"img_card"},
		},
		{
			// A card whose content the platform will not hand back — a remote
			// template — still says that a card was there. The alternative is a
			// silent gap where the thing being replied to was.
			name: "a template card is named",
			message: &fakeLarkMessage{msgType: "interactive", content: `{"type":"template","data":{` +
				`"template_id":"AAqXVPGVTmWpR","template_variable":{"chat_name":"值班群"}}}`},
			wantText: "**帮助卡片**\n\n模板 ID：AAqXVPGVTmWpR\n\n变量：\n- chat_name：值班群",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			if id == "" {
				id = "om_1"
			}
			itemID := tt.itemID
			if itemID == "" {
				itemID = id
			}
			item := &larkim.Message{
				MessageId: ptr.To(itemID),
				MsgType:   ptr.To(tt.message.msgType),
				Body:      &larkim.MessageBody{Content: ptr.To(tt.message.content)},
			}
			level := quotedLevelFrom(id, item)
			if level.text != tt.wantText {
				t.Errorf("text = %q, want %q", level.text, tt.wantText)
			}
			if len(level.images) != len(tt.wantImgs) {
				t.Fatalf("images = %v, want %v", level.images, tt.wantImgs)
			}
			wantBound := tt.wantBound
			if wantBound == "" {
				wantBound = id
			}
			for i, ref := range level.images {
				if ref.imageKey != tt.wantImgs[i] {
					t.Errorf("images[%d].imageKey = %q, want %q", i, ref.imageKey, tt.wantImgs[i])
				}
				if ref.messageID != wantBound {
					t.Errorf("images[%d].messageID = %q, want %q", i, ref.messageID, wantBound)
				}
			}
		})
	}
}

// ---------- resolution ----------

func TestResolveQuotedIsSilentWithoutAReply(t *testing.T) {
	s := newFakeLarkServer(t)
	b := quoteBiz(t, s)

	if got := b.resolveQuoted(context.Background(), ""); got != nil {
		t.Errorf("resolveQuoted(\"\") = %v, want nil", got)
	}
	// Not a reply is not a failure, and must not cost a round trip either.
	if reads := s.readsSeen(); len(reads) != 0 {
		t.Errorf("the platform was read %v for a message that quotes nothing", reads)
	}

	// The same for a process with no platform connection: the console path.
	b = &botBiz{sr: &runtime.StaticRuntime{}}
	if got := b.resolveQuoted(context.Background(), "om_1"); got != nil {
		t.Errorf("resolveQuoted() with no client = %v, want nil", got)
	}
}

func TestResolveQuotedReadsTheChain(t *testing.T) {
	s := newFakeLarkServer(t)
	s.messages["om_reply"] = &fakeLarkMessage{msgType: "text", content: `{"text":"第二层"}`, parentID: "om_root"}
	s.messages["om_root"] = &fakeLarkMessage{msgType: "text", content: `{"text":"第一层"}`}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_reply")
	if len(levels) != 2 {
		t.Fatalf("levels = %v, want the whole chain", levels)
	}
	// Oldest first: the block reads the way the conversation did.
	if levels[0].text != "第一层" || levels[1].text != "第二层" {
		t.Errorf("levels = %v, want oldest first", levels)
	}
	if chain := formatQuotedChain(levels); !strings.Contains(chain, "第一层") {
		t.Errorf("chain = %q, want the quoted words in it", chain)
	}
}

func TestResolveQuotedAsksForTheCardsOwnJSON(t *testing.T) {
	s := newFakeLarkServer(t)
	s.messages["om_card"] = &fakeLarkMessage{
		msgType: "interactive",
		content: `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"磁盘满了"}]}}`,
	}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_card")
	if len(levels) != 1 || !strings.Contains(levels[0].text, "磁盘满了") {
		t.Fatalf("levels = %v, want the card's own text", levels)
	}

	// Every read asks for it — a read that does not gets the platform's
	// degraded rendering of the card instead of the card.
	asked := s.cardContentTypesSeen()
	if len(asked) != 1 {
		t.Fatalf("reads = %v, want one read", asked)
	}
	if asked[0] != larkcard.CardContentTypeUser {
		t.Errorf("card_msg_content_type = %q, want %q", asked[0], larkcard.CardContentTypeUser)
	}
}

func TestResolveQuotedSkipsAnEmptyLevel(t *testing.T) {
	s := newFakeLarkServer(t)
	// A message with nothing in it sits in the middle of the chain. The chain
	// carries on past it rather than leaving a blank level behind.
	s.messages["om_new"] = &fakeLarkMessage{msgType: "text", content: `{"text":"最新"}`, parentID: "om_blank"}
	s.messages["om_blank"] = &fakeLarkMessage{msgType: "text", content: `{"text":"   "}`, parentID: "om_old"}
	s.messages["om_old"] = &fakeLarkMessage{msgType: "text", content: `{"text":"最早"}`}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_new")
	if len(levels) != 2 {
		t.Fatalf("levels = %v, want the two that carry something", levels)
	}
	if levels[0].text != "最早" || levels[1].text != "最新" {
		t.Errorf("levels = %v, want oldest first and the blank one gone", levels)
	}
}

func TestResolveQuotedStopsAtTheDepthBound(t *testing.T) {
	s := newFakeLarkServer(t)
	// A cycle, which the platform should never produce and this must not follow
	// forever: the bound is what makes the walk terminate regardless.
	for i := 0; i < maxQuoteDepth+5; i++ {
		s.messages[fmt.Sprintf("om_%d", i)] = &fakeLarkMessage{
			msgType: "text", content: `{"text":"x"}`, parentID: fmt.Sprintf("om_%d", i+1),
		}
	}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_0")
	if len(levels) != maxQuoteDepth {
		t.Errorf("levels = %d, want the bound %d", len(levels), maxQuoteDepth)
	}
	if reads := s.readsSeen(); len(reads) != maxQuoteDepth {
		t.Errorf("reads = %d, want %d", len(reads), maxQuoteDepth)
	}
}

func TestResolveQuotedDegrades(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeLarkServer)
		id    string
	}{
		{name: "the platform refuses the read", id: "om_1", setup: func(s *fakeLarkServer) { s.failReads["om_1"] = true }},
		{name: "the message is gone", id: "om_1"},
		{name: "the message was recalled", id: "om_1", setup: func(s *fakeLarkServer) {
			s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: `{"text":"x"}`, deleted: true}
		}},
		{name: "the body is absent", id: "om_1", setup: func(s *fakeLarkServer) {
			s.messages["om_1"] = &fakeLarkMessage{msgType: "text", bodyless: true}
		}},
		{name: "the message has nothing in it", id: "om_1", setup: func(s *fakeLarkServer) {
			s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: `{"text":"  "}`}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeLarkServer(t)
			if tt.setup != nil {
				tt.setup(s)
			}
			b := quoteBiz(t, s)
			if got := b.resolveQuoted(context.Background(), tt.id); got != nil {
				t.Errorf("resolveQuoted() = %v, want nil: a failure to read a quote must not fail the turn", got)
			}
		})
	}
}

func TestResolveQuotedSkipsARecalledMessage(t *testing.T) {
	s := newFakeLarkServer(t)
	s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: `{"text":"x"}`}
	b := quoteBiz(t, s, "om_1")

	if got := b.resolveQuoted(context.Background(), "om_1"); got != nil {
		t.Errorf("resolveQuoted() = %v, want nil", got)
	}
	// A message the sender withdrew is not context, and reading it would be a
	// round trip spent to fetch what is already known to be gone.
	if reads := s.readsSeen(); len(reads) != 0 {
		t.Errorf("a recalled message was read anyway: %v", reads)
	}
}

func TestResolveQuotedTruncatesWithinTheBudget(t *testing.T) {
	s := newFakeLarkServer(t)
	huge, _ := json.Marshal(map[string]string{"text": strings.Repeat("长", maxQuotedTextRunes+500)})
	s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: string(huge)}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_1")
	if len(levels) != 1 {
		t.Fatalf("levels = %v, want one", levels)
	}
	// Cut by rune, not by byte: slicing CJK by byte would split a character and
	// put a replacement glyph in front of the model.
	if !strings.HasSuffix(levels[0].text, quoteTruncatedMarker) {
		t.Errorf("the text was not marked as truncated: %q", tail(levels[0].text, 40))
	}
	if got := len([]rune(strings.TrimSuffix(levels[0].text, quoteTruncatedMarker))); got != maxQuotedTextRunes {
		t.Errorf("kept %d runes, want %d", got, maxQuotedTextRunes)
	}
	if !utf8.ValidString(levels[0].text) {
		t.Error("the truncated text is not valid UTF-8")
	}
}

func TestResolveQuotedStopsWhenTheBudgetIsSpent(t *testing.T) {
	s := newFakeLarkServer(t)
	huge, _ := json.Marshal(map[string]string{"text": strings.Repeat("长", maxQuotedTextRunes)})
	s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: string(huge), parentID: "om_0"}
	s.messages["om_0"] = &fakeLarkMessage{msgType: "text", content: `{"text":"更早"}`}
	b := quoteBiz(t, s)

	levels := b.resolveQuoted(context.Background(), "om_1")
	// Nothing further can fit, so the walk stops rather than spending a round
	// trip per remaining level to be told the same thing.
	if reads := s.readsSeen(); len(reads) != 1 {
		t.Errorf("reads = %v, want the walk to stop once the budget is spent", reads)
	}
	if len(levels) != 1 {
		t.Errorf("levels = %v, want only what fits", levels)
	}
}

func TestResolveQuotedGivesUpOnATimeout(t *testing.T) {
	s := newFakeLarkServer(t)
	release := make(chan struct{})
	s.block = release
	s.messages["om_1"] = &fakeLarkMessage{msgType: "text", content: `{"text":"x"}`}
	// Closed before the server is, so Close does not wait on the blocked handler.
	t.Cleanup(func() { close(release) })
	b := quoteBiz(t, s)

	// The inbound context carries no deadline of its own (handler/bot.go detaches
	// it), so without this bound a stalled platform call would keep the turn —
	// and the card promising an answer — from ever starting.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := b.resolveQuoted(ctx, "om_1"); got != nil {
		t.Errorf("resolveQuoted() = %v, want nil", got)
	}
	if waited := time.Since(start); waited > quoteFetchTimeout {
		t.Errorf("resolveQuoted waited %s, want it bounded", waited)
	}
}

// ---------- storing the quoted pictures ----------

func TestStoreQuotedImagesReadsThemByTheirOwnMessage(t *testing.T) {
	s := newFakeLarkServer(t)
	want := []byte("png-bytes")
	s.files["om_quoted/img_a"] = want

	agent := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Clientset: &fakeClientset{llm: agent, lark: s.client()},
	}}
	dr := &runtime.DynamicRuntime{Sender: v1.NewSender("u_1", ""), Receiver: quoteReceiver()}

	refs := b.storeQuotedImages(context.Background(), dr, []quotedLevel{
		{images: []imageRef{{messageID: "om_quoted", imageKey: "img_a"}}},
	})
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want one", refs)
	}
	if got := s.downloadsSeen(); len(got) != 1 || got[0] != "om_quoted/img_a" {
		t.Errorf("downloads = %v, want the picture read by the message it came from", got)
	}
	stored := agent.assetsSeen()
	if len(stored) != 1 {
		t.Fatalf("stored = %v, want one upload", stored)
	}
	if decoded, err := base64.StdEncoding.DecodeString(stored[0].GetData()); err != nil || string(decoded) != string(want) {
		t.Errorf("uploaded %q, want the bytes that were downloaded", decoded)
	}
	if stored[0].GetSource() != media.SourceLark {
		t.Errorf("Source = %q, want %q", stored[0].GetSource(), media.SourceLark)
	}
}

func TestStoreQuotedImagesSkipsWhatItCannotStore(t *testing.T) {
	s := newFakeLarkServer(t)
	s.files["om_q/img_ok"] = []byte("ok")

	agent := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Clientset: &fakeClientset{llm: agent, lark: s.client()},
	}}
	dr := &runtime.DynamicRuntime{Sender: v1.NewSender("u_1", ""), Receiver: quoteReceiver()}

	// The first key has no bytes on the platform, the second does. A picture
	// that cannot be fetched must not cost the one that can.
	refs := b.storeQuotedImages(context.Background(), dr, []quotedLevel{
		{images: []imageRef{{messageID: "om_q", imageKey: "img_missing"}, {messageID: "om_q", imageKey: "img_ok"}}},
	})
	if len(refs) != 1 || refs[0].AssetID == "" {
		t.Errorf("refs = %v, want the one picture that could be stored", refs)
	}
}

func TestStoreQuotedImagesHonoursItsBudget(t *testing.T) {
	s := newFakeLarkServer(t)
	var level quotedLevel
	for i := 0; i < maxQuotedImages+3; i++ {
		key := fmt.Sprintf("img_%d", i)
		s.files["om_q/"+key] = []byte("x")
		level.images = append(level.images, imageRef{messageID: "om_q", imageKey: key})
	}
	agent := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{Clientset: &fakeClientset{llm: agent, lark: s.client()}}}
	dr := &runtime.DynamicRuntime{Sender: v1.NewSender("u_1", ""), Receiver: quoteReceiver()}

	refs := b.storeQuotedImages(context.Background(), dr, []quotedLevel{level})
	if len(refs) != maxQuotedImages {
		t.Errorf("stored %d pictures, want the budget %d", len(refs), maxQuotedImages)
	}
}

// ---------- the turn ----------

// TestRunTurnWithQuoteCarriesBothHalves is the property the feature is for: what
// the user's words ask about arrives with them, whether it was words, a picture,
// or both.
func TestRunTurnWithQuoteCarriesBothHalves(t *testing.T) {
	t.Run("a question can quote a picture", func(t *testing.T) {
		s := newFakeLarkServer(t)
		s.messages["om_quoted"] = &fakeLarkMessage{msgType: "image", content: `{"image_key":"img_a"}`}
		s.files["om_quoted/img_a"] = []byte("png")

		agent, b, dr := turnFixture(t, s)
		if err := b.runTurnWithQuote(context.Background(), dr, "om_quoted", "这个怎么解决", nil); err != nil {
			t.Fatalf("runTurnWithQuote() = %v", err)
		}

		req := onlyRequest(t, agent)
		if !strings.Contains(req.GetInput().GetText(), "这个怎么解决") {
			t.Errorf("Question = %q, want the user's words", req.GetInput().GetText())
		}
		if !strings.Contains(req.GetInput().GetText(), "[引用消息]") {
			t.Errorf("Question = %q, want the quote marked as such", req.GetInput().GetText())
		}
		imgs := req.GetInput().GetImages()
		if len(imgs) != 1 {
			t.Errorf("images = %d, want the quoted picture stored and referenced", len(imgs))
		} else if imgs[0].GetAssetId() == "" && imgs[0].GetUrl() == "" {
			t.Errorf("image = %+v, want an asset id or URL", imgs[0])
		}
	})

	t.Run("a reply of nothing but a mention is still a question", func(t *testing.T) {
		s := newFakeLarkServer(t)
		s.messages["om_quoted"] = &fakeLarkMessage{msgType: "text", content: `{"text":"磁盘满了"}`}

		agent, b, dr := turnFixture(t, s)
		// The words the user typed were only "@OneX", which normalisation strips
		// to nothing. Without the quote there would be no question at all.
		if err := b.runTurnWithQuote(context.Background(), dr, "om_quoted", "", nil); err != nil {
			t.Fatalf("runTurnWithQuote() = %v", err)
		}
		req := onlyRequest(t, agent)
		if !strings.Contains(req.GetInput().GetText(), "磁盘满了") {
			t.Errorf("Question = %q, want the quoted words", req.GetInput().GetText())
		}
		if !strings.Contains(req.GetInput().GetText(), defaultQuotedQuestion) {
			t.Errorf("Question = %q, want the fallback instruction", req.GetInput().GetText())
		}
	})

	t.Run("a message that quotes nothing is untouched", func(t *testing.T) {
		s := newFakeLarkServer(t)
		agent, b, dr := turnFixture(t, s)
		if err := b.runTurnWithQuote(context.Background(), dr, "", "直接问的问题", nil); err != nil {
			t.Fatalf("runTurnWithQuote() = %v", err)
		}
		req := onlyRequest(t, agent)
		if req.GetInput().GetText() != "直接问的问题" {
			t.Errorf("Question = %q, want it unchanged", req.GetInput().GetText())
		}
		if reads := s.readsSeen(); len(reads) != 0 {
			t.Errorf("the platform was read for a message that quotes nothing: %v", reads)
		}
	})

	t.Run("a reply with no words whose quote is unreadable starts no turn", func(t *testing.T) {
		s := newFakeLarkServer(t)
		s.failReads["om_gone"] = true
		agent, b, dr := turnFixture(t, s)
		if err := b.runTurnWithQuote(context.Background(), dr, "om_gone", "", nil); err != nil {
			t.Fatalf("runTurnWithQuote() = %v", err)
		}
		// Nothing to ask: no words, and the quote could not be read. The turn
		// must not be started with an empty question, which the backend would
		// answer by inventing context nobody sent.
		if got := len(agent.requests); got != 0 {
			t.Errorf("the backend was asked %d times, want none", got)
		}
	})
}

// turnFixture wires a bot whose turn runs inline — no session manager — so a
// test can assert on the request the moment the call returns.
//
// The default agents are set because a run that resolves to none stops at the
// empty-agent card, before the backend this test is about is reached: the
// fixture has to give the scheduler something to resolve to, exactly as a
// deployment does.
func turnFixture(t *testing.T, s *fakeLarkServer) (*fakeAgent, *botBiz, *runtime.DynamicRuntime) {
	t.Helper()
	agent := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Clientset:            &fakeClientset{llm: agent, lark: s.client()},
		Agents:               llmRegistry(agent),
		BotName:              "Helper",
		InternalDefaultAgent: "default",
		ExternalDefaultAgent: "default",
	}}
	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		Receiver:     quoteReceiver(),
		LarkServices: &fakeLarkServices{},
	}
	return agent, b, dr
}

// onlyRequest returns the single request the backend received.
func onlyRequest(t *testing.T, agent *fakeAgent) *llmv1.AgentAskRequest {
	t.Helper()
	if len(agent.requests) != 1 {
		t.Fatalf("the backend received %d requests, want one", len(agent.requests))
	}
	return agent.requests[0]
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// ---------- the ladder and the batch ----------

// mentionReplyEvent builds the event for a message whose whole body is the
// bot's @mention, as a reply to parentID (empty for a message that replies to
// nothing).
func mentionReplyEvent(parentID string) *im.Event {
	ev := messageEvent("@_user_1")
	ev.Message.Mentions = []im.Mention{{Key: "@_user_1", Name: "OneX", IsBot: true}}
	ev.Message.ParentID = parentID
	return ev
}

// TestWalkAnswersAWorlessReply pins the one rung the feature changes.
//
// "@OneX" and nothing else normalises to the empty string, and a message with
// nothing in it is dropped — but a reply has something in it even then: the
// message it points at. The rung has to tell the two apart without asking the
// platform, which it can, because the reply's target arrives with the event.
func TestWalkAnswersAWorlessReply(t *testing.T) {
	f := newFunnelFixture(t)

	// No quote: there is genuinely nothing to answer.
	step, outcome, _ := f.walk(t, mentionReplyEvent(""))
	if step != stepPrompt || outcome != outcomeDropped {
		t.Errorf("a wordless message that quotes nothing got %s/%s, want %s/%s", step, outcome, stepPrompt, outcomeDropped)
	}

	// A quote: the same words, and something to answer about.
	step, outcome, act := f.walk(t, mentionReplyEvent("om_quoted"))
	if step != stepPrompt || outcome != outcomeHandled {
		t.Errorf("a wordless reply got %s/%s, want %s/%s", step, outcome, stepPrompt, outcomeHandled)
	}
	if act == nil {
		t.Fatal("a wordless reply was claimed but handed nothing to do")
	}
}

// TestWalkDoesNotReadTheQuote is the ladder's contract, kept: deciding a
// message's fate costs no platform call. The fixture's clientset has no Lark
// client at all, so a rung that reached for one would panic rather than quietly
// pass.
func TestWalkDoesNotReadTheQuote(t *testing.T) {
	f := newFunnelFixture(t)
	for _, ev := range []*im.Event{mentionReplyEvent("om_quoted"), messageEvent("/help"), messageEvent("你好")} {
		if _, _, _, err := f.biz.walk(context.Background(), newInbound(ev, time.Now())); err != nil {
			t.Fatalf("the ladder failed to decide %q: %v", ev.Message.ID, err)
		}
	}
}

// TestBufferImageCarriesTheBurstsQuote covers the two things the batch has to
// get right about pictures that arrive as replies: the quote belongs to whichever
// message in the burst carries it — not, as reading it off the entry's runtime
// would, to whichever message happened to create the entry — and the words are
// normalised the way the text path normalises them, so an @mention never reaches
// the model as the platform's placeholder.
func TestBufferImageCarriesTheBurstsQuote(t *testing.T) {
	b := &botBiz{
		// Long enough that the batch never flushes during the test: this is
		// about what is accumulated, not about what a flush does with it.
		sr:           &runtime.StaticRuntime{ImageBatchWindow: time.Hour},
		imageBatches: map[string]*imageBatch{},
	}
	plain := &runtime.DynamicRuntime{
		Receiver: quoteReceiver(),
		Mentions: []runtime.MentionedUser{{Key: "@_user_1", Name: "OneX", IsBot: true}},
	}
	b.bufferImage(context.Background(), plain,
		&MsgContent{Text: "@_user_1 看看", ImageKeys: []string{"img_1"}})

	replyRecv := quoteReceiver()
	replyRecv.MessageId = "om_2"
	replyRecv.ParentMessageId = "om_quoted"
	b.bufferImage(context.Background(), &runtime.DynamicRuntime{Receiver: replyRecv},
		&MsgContent{ImageKeys: []string{"img_2"}})

	entry := b.imageBatches[quoteReceiver().Id]
	if entry == nil {
		t.Fatal("the burst opened no batch")
	}
	if entry.parentID != "om_quoted" {
		t.Errorf("parentID = %q, want the quote the burst actually carried", entry.parentID)
	}
	if strings.Contains(entry.text, "@_user_1") {
		t.Errorf("text = %q, want the mention resolved before the model sees it", entry.text)
	}
	if len(entry.refs) != 2 {
		t.Errorf("refs = %v, want both pictures", entry.refs)
	}
}
