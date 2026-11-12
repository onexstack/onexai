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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	llmagent "github.com/onexstack/onexai/internal/chat/pkg/agent/llm"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	larkclient "github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// fakeLarkServices records the outbound calls the AI admission path makes. Only
// the methods that path can reach are overridden; the embedded interface stays
// nil, so anything else panics loudly instead of passing silently.
type fakeLarkServices struct {
	runtime.LarkServices

	// mu guards everything a second goroutine can reach. Delivering an answer
	// resumes the run through the sink its first half was given, so the card this
	// fake renders is written by the run's goroutine while the test reads it from
	// its own: without this the two race on the very state the assertions are about,
	// and the reader can observe a half-written card. It is the same reason
	// fakeAgent has one.
	mu sync.Mutex

	texts        []string
	cardRunIDs   []string
	closedCardCt int
	// cardReasoning records the showReasoning flag each streaming card was created
	// with — the panel the live card carries, which has to agree with what the
	// backend was asked for.
	cardReasoning []bool

	// updates records every incremental card-element write, keyed by element.
	updates map[string][]string
	// finalCards records the card JSON handed to CloseStreamingCard.
	finalCards []string
	// markdowns records fallback messages: the answer delivered outside a card.
	markdowns []string

	// liveElements is the card as it currently stands: every component a pause
	// added, by element id, and gone again once it is deleted. It is what lets a
	// test ask the questions the feature is about — does the reader still have a
	// button, and what does the decision log say now.
	liveElements map[string]map[string]any
	// inserts records what was added to the live card, in order.
	inserts []insertedElement
	// deleted records the element ids that have been taken off the card.
	deleted []string
	// summaries records every summary written for the card.
	summaries []string

	// images records the bytes of every image sent as a message, and imageErr
	// makes a send fail: the two things a test of the delivery path needs.
	images   [][]byte
	imageErr error

	// failUpdates, failInsert and failClose make the transport fail, which is what
	// a degraded turn has to survive.
	failUpdates bool
	failInsert  bool
	failClose   bool
}

func (f *fakeLarkServices) SendText(_ context.Context, _ *v1.Receiver, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, content)
	return nil
}

func (f *fakeLarkServices) SendMarkdownCard(_ context.Context, _ *v1.Receiver, _, markdown, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markdowns = append(f.markdowns, markdown)
	return nil
}

func (f *fakeLarkServices) CreateStreamingCard(_ context.Context, _ *v1.Receiver, _, _ string, showReasoning bool, stopRunID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cardRunIDs = append(f.cardRunIDs, stopRunID)
	f.cardReasoning = append(f.cardReasoning, showReasoning)
	return "card-1", nil
}

func (f *fakeLarkServices) CloseStreamingCard(_ context.Context, _, cardJSON string, _ *stream.Sequence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failClose {
		return errors.New("card is gone")
	}
	// The card is recorded before the count that says it was: a test waiting for
	// the count to move goes on to read the card, and the two must not be able to
	// disagree about whether it is there yet.
	f.finalCards = append(f.finalCards, cardJSON)
	f.closedCardCt++
	return nil
}

func (f *fakeLarkServices) record(element, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpdates {
		return errors.New("element update rejected")
	}
	if f.updates == nil {
		f.updates = map[string][]string{}
	}
	f.updates[element] = append(f.updates[element], content)
	return nil
}

func (f *fakeLarkServices) UpdateCardText(_ context.Context, _, content string, _ *stream.Sequence) error {
	return f.record("answer", content)
}

func (f *fakeLarkServices) UpdateCardReasoningText(_ context.Context, _, content string, _ *stream.Sequence) error {
	return f.record("reasoning", content)
}

func (f *fakeLarkServices) UpdateCardProgressText(_ context.Context, _, content string, _ *stream.Sequence) error {
	return f.record("progress", content)
}

func (f *fakeLarkServices) UpdateCardElementText(_ context.Context, _, elementID, content string, _ *stream.Sequence) error {
	return f.record(elementID, content)
}

// InsertCardElements records the components a pause added to the live card, by
// their element id, so a test can tell what the card was given and in what order
// it arrived.
func (f *fakeLarkServices) InsertCardElements(_ context.Context, _, target string, _ *stream.Sequence, elements ...map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failInsert {
		return errors.New("element insert rejected")
	}
	// An insert is checked the way the platform checks it, because this is the one
	// card write the turn cannot rebuild its way around: the card is already on
	// screen, and the components a pause adds are in no golden. A rule the real
	// cardkit enforces — an element_id of at most 20 characters — has to fail here
	// too, or the tests that cover the pause prove nothing about it.
	raw, err := json.Marshal(map[string]any{"body": map[string]any{"elements": elements}})
	if err != nil {
		return fmt.Errorf("marshal inserted elements: %w", err)
	}
	if violations := larkcard.Card2Violations(raw); len(violations) > 0 {
		return errors.New("cardkit create card element failed: " + strings.Join(violations, "; "))
	}
	for _, elem := range elements {
		id, _ := elem["element_id"].(string)
		tag, _ := elem["tag"].(string)
		f.inserts = append(f.inserts, insertedElement{ID: id, Tag: tag, Target: target})
		if id != "" {
			if f.liveElements == nil {
				f.liveElements = map[string]map[string]any{}
			}
			f.liveElements[id] = elem
		}
	}
	return nil
}

func (f *fakeLarkServices) DeleteCardElements(_ context.Context, _ string, _ *stream.Sequence, ids ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, ids...)
	for _, id := range ids {
		delete(f.liveElements, id)
	}
	return nil
}

func (f *fakeLarkServices) SetCardSummary(_ context.Context, _, summary string, _ *stream.Sequence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries = append(f.summaries, summary)
	return nil
}

// insertedElement is one component a pause put on the live card.
type insertedElement struct {
	ID     string
	Tag    string
	Target string
}

// hasLiveElement reports whether an element is currently on the card, which is
// how a test tells a button that was taken away from one that never left.
func (f *fakeLarkServices) hasLiveElement(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.liveElements[id]
	return ok
}

// elementText returns the text a component on the live card carries.
func (f *fakeLarkServices) elementText(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	elem := f.liveElements[id]
	if elem == nil {
		return ""
	}
	content, _ := elem["content"].(string)
	return content
}

// buttonsUnder returns the button labels of the first component inserted with the
// given id prefix, in the order they were laid out.
func (f *fakeLarkServices) buttonsUnder(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	elem := f.liveElements[id]
	if elem == nil {
		return nil
	}
	var out []string
	for _, col := range columnsOf(elem) {
		for _, sub := range subElements(col) {
			if tag, _ := sub["tag"].(string); tag != "button" {
				continue
			}
			if text, ok := sub["text"].(map[string]any); ok {
				label, _ := text["content"].(string)
				out = append(out, label)
			}
		}
	}
	return out
}

// columnsOf returns a column_set's columns, or none for another component.
func columnsOf(elem map[string]any) []map[string]any {
	raw, _ := elem["columns"].([]map[string]any)
	return raw
}

// subElements returns the components inside one column.
func subElements(col map[string]any) []map[string]any {
	raw, _ := col["elements"].([]map[string]any)
	return raw
}

// lastUpdate returns the content most recently written to an element.
func (f *fakeLarkServices) lastUpdate(element string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.updates[element]
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// closedCount is how many times the card was finalized. It is read through a
// method rather than as a field because the turn that finalizes it is on another
// goroutine from the test.
func (f *fakeLarkServices) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedCardCt
}

// closedCards returns the cards handed to CloseStreamingCard, as a copy: the
// caller reads it while the run's goroutine may still be appending.
func (f *fakeLarkServices) closedCards() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.finalCards...)
}

// lastClosedCard returns the card the turn was last finalized with, and whether
// there is one. It is one call rather than indexing closedCards because the two
// would be separate snapshots, and a card finalized in between would leave the
// index pointing past the end of the slice it was measured against.
func (f *fakeLarkServices) lastClosedCard() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finalCards) == 0 {
		return "", false
	}
	return f.finalCards[len(f.finalCards)-1], true
}

// summaryList returns the summaries written for the card, as a copy.
func (f *fakeLarkServices) summaryList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.summaries...)
}

// insertList returns what was added to the live card, as a copy.
func (f *fakeLarkServices) insertList() []insertedElement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]insertedElement(nil), f.inserts...)
}

// SendImage records the picture and, when imageErr is set, reports a failure.
//
// It is here rather than in a fake of its own because this is the package's one
// implementation of the port: a second one would leave the two disagreeing about
// what "the fake" records.
func (f *fakeLarkServices) SendImage(_ context.Context, _ *v1.Receiver, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.imageErr != nil {
		return f.imageErr
	}
	f.images = append(f.images, data)
	return nil
}

// sentImages returns a copy of the pictures sent so far.
func (f *fakeLarkServices) sentImages() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.images...)
}

// deletedIDs returns the element ids taken off the card, as a copy.
func (f *fakeLarkServices) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// fakeClientset supplies the two clients the AI turn reaches for: the agent
// backend it asks, and the Lark platform it reads a quoted message from.
type fakeClientset struct {
	clientset.Interface
	llm  agentclient.Interface
	lark larkclient.Interface
}

func (f *fakeClientset) Agent() agentclient.Interface { return f.llm }
func (f *fakeClientset) L() larkclient.Interface      { return f.lark }

// llmRegistry wires the llm backend over a fake client — the one agent the
// package's tests talk to. A registry rather than a bare client because that is
// what the turn resolves through: the port is the contract under test, and
// handing it a fake backend directly would leave the resolution untested.
func llmRegistry(llm agentclient.Interface) *agent.Registry {
	return agent.NewRegistry(agent.BackendLLM, llmagent.New(llm))
}

// fakeAgent replays a scripted event stream. The default script is empty, so
// a run that only exercises admission finishes immediately.
type fakeAgent struct {
	agentclient.Interface
	requests []*llmv1.AgentAskRequest
	events   []*llmv1.AgentStreamEvent

	// mu guards approvals, which are written by the goroutine that reads a
	// resumed run and read by the test that answered it — the two are different
	// goroutines on purpose (see AnswerPermission).
	mu sync.Mutex
	// approvals records every answer delivered to a paused run.
	approvals []fakeApproval
	// approveErr makes delivering that answer fail, which is what a run that has
	// already ended looks like from here.
	approveErr error
	// askErr makes the first half fail before it streams anything, which is what
	// the backend's refusal of a request that never became a run looks like from
	// here.
	askErr error
	// approveEvents is what the resumed run streams once the answer has been
	// accepted: the second half of a turn, arriving through the sink the first
	// half was given.
	approveEvents []*llmv1.AgentStreamEvent

	// stops records every run asked to stop. Nothing else in this fake is a
	// request that leaves the process, so the recorded calls are the evidence
	// that a click reached the backend at all.
	stops []string
	// stopErr makes a stop fail, which is what the backend reports when the run
	// is gone — the one case a toast must not call "stopped".
	stopErr error

	// assets records every image handed to the backend, and assetErr makes the
	// store refuse: the two things a caller of UploadAsset can observe.
	assets   []*llmv1.CreateAssetRequest
	assetErr error
}

// fakeApproval is one answer delivered to a paused run.
type fakeApproval struct {
	runID     string
	requestID string
	action    string
	answers   map[string]string
}

// Answer returns the text answer, the way a backend with no addresses reads one.
func (a fakeApproval) Answer() string {
	for _, v := range a.answers {
		return v
	}
	return ""
}

// AgentApproveStream is the resume call: the answer goes up, and the rest of the
// run comes back down the same stream shape the first half used.
func (f *fakeAgent) AgentApproveStream(
	_ context.Context, req *llmv1.AgentApproveRequest, onEvent func(*llmv1.AgentStreamEvent) error,
) error {
	f.mu.Lock()
	f.approvals = append(f.approvals, fakeApproval{
		runID:     req.GetRunID(),
		requestID: req.GetRequestID(),
		action:    req.GetDecision(),
		answers:   req.GetAnswers(),
	})
	f.mu.Unlock()

	if f.approveErr != nil {
		return f.approveErr
	}
	for _, ev := range f.approveEvents {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

// approvalsSeen returns a copy of what has been delivered so far.
func (f *fakeAgent) approvalsSeen() []fakeApproval {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeApproval(nil), f.approvals...)
}

// awaitApprovals waits until n answers have reached the backend, or fails the
// test.
//
// It is a wait rather than an assertion because delivering an answer really does
// happen elsewhere: the port sends it and returns, and the run it continued is
// read on a goroutine of its own, so a test that looked at the fake immediately
// would be racing that goroutine rather than testing it.
func (f *fakeAgent) awaitApprovals(t *testing.T, n int) []fakeApproval {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := f.approvalsSeen()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the backend received %d answers, want %d: %+v", len(got), n, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// UploadAsset stores image bytes, recording them so a test can assert which
// picture was handed over and under whose name.
func (f *fakeAgent) UploadAsset(_ context.Context, req *llmv1.CreateAssetRequest) (*llmv1.Asset, error) {
	if f.assetErr != nil {
		return nil, f.assetErr
	}
	f.mu.Lock()
	f.assets = append(f.assets, req)
	n := len(f.assets)
	f.mu.Unlock()
	return &llmv1.Asset{
		AssetId:  fmt.Sprintf("img_%d", n),
		Name:     req.GetFilename(),
		MimeType: "image/png",
		Size:     int64(len(req.GetData())),
	}, nil
}

// assetsSeen returns a copy of what has been stored so far.
func (f *fakeAgent) assetsSeen() []*llmv1.CreateAssetRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*llmv1.CreateAssetRequest(nil), f.assets...)
}

func (f *fakeAgent) Stop(_ context.Context, runID string) error {
	f.stops = append(f.stops, runID)
	return f.stopErr
}

func (f *fakeAgent) AgentAskStream(_ context.Context, req *llmv1.AgentAskRequest, onEvent func(*llmv1.AgentStreamEvent) error) error {
	f.requests = append(f.requests, req)
	if f.askErr != nil {
		return f.askErr
	}
	for _, ev := range f.events {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func answerEvent(delta string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeAnswer, Delta: delta}
}

func reasoningEvent(delta string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeReasoning, Delta: delta}
}

// interruptedEvent asks for approval of one tool. Its run id is deliberately not
// the turn's — the turn generates its own — because nothing on the pause path
// reads it: the turn records the run it started, which is the one an answer has
// to name.
func interruptedEvent(requestID, tool string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{
		Type: llmv1.AgentEventType_agentEventTypeInterrupted,
		Interrupt: &llmv1.AgentInterrupt{
			RequestID: requestID,
			Requests: []*llmv1.AgentInterruptRequest{{
				Address:  "tool:call-1",
				ToolName: tool,
				Question: "允许执行 " + tool + " 吗？",
			}},
		},
	}
}

// suspendedEvent is the terminal of a stream that ended with the run still
// parked. It is not an ending, and the card must not be finalized on it.
func suspendedEvent() *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeSuspended}
}

// larkClientThatRefuses answers every Lark API call with a permanent error code.
//
// It exists so a pause can run the real card-sending path without a network: the
// approval card is built, addressed and handed to the retrier, and the refusal
// comes back as a clean API error rather than a transient one — a dead port would
// be retried with backoff and make this test slow. The pause then carries on, as
// it does for any transport failure: the card is logged and dropped, and the
// pending decision the buttons would answer is recorded regardless.
func larkClientThatRefuses(t *testing.T) *lark.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":230013,"msg":"bot is not enabled"}`)
	}))
	t.Cleanup(srv.Close)
	return lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(srv.URL))
}

// doneEvent closes a run cleanly.
func doneEvent(final string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{
		Type:  llmv1.AgentEventType_agentEventTypeDone,
		Final: final,
	}
}

// toolStepEvent reports one finished tool invocation, which is what puts a row in
// the tool panel on both the live card and the final one.
func toolStepEvent(name, status string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{
		Type: llmv1.AgentEventType_agentEventTypeToolStep,
		Steps: []*llmv1.AgentToolStep{
			{Id: "call-1", Name: name, Status: status},
		},
	}
}

// aiQueueFixture is one conversation wired to a real session manager, so the
// admission path runs against the real lock and queue rather than a stub.
type aiQueueFixture struct {
	biz   *botBiz
	lark  *fakeLarkServices
	dr    *runtime.DynamicRuntime
	sess  *session.Session
	llm   *fakeAgent
	group string
}

// triggerMessage makes the runtime look like the call arrived from one specific
// message, which is what a turn remembers to tell a re-delivery apart from a new
// question. The receiver is replaced whole rather than patched: it is what a
// caller builds from an event, and nothing outside pkg/api/chat/v1 fills one in.
func (f *aiQueueFixture) triggerMessage(id string) {
	f.dr.Receiver = &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: f.group, MessageId: id}
}

func newAIQueueFixture(t *testing.T, maxQueued, maxConcurrent int) *aiQueueFixture {
	t.Helper()

	const convID = "oc_test_conv"
	mgr := session.NewManager(context.Background(), session.Options{
		DataDir:            t.TempDir(),
		MaxQueuedMessages:  maxQueued,
		MaxConcurrentTurns: maxConcurrent,
	})
	t.Cleanup(mgr.Close)

	sess, err := mgr.Session(convID)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	lark := &fakeLarkServices{}
	llm := &fakeAgent{}

	return &aiQueueFixture{
		biz: &botBiz{sr: &runtime.StaticRuntime{
			Sessions:  mgr,
			Clientset: &fakeClientset{llm: llm},
			Agents:    llmRegistry(llm),
			BotName:   "Helper",
			// The scheduler resolves @default to one of these. Both are set to
			// the value this service hardcoded before the setting existed, so
			// these tests see the baseline they were written against whatever
			// audience the fixture's receiver implies — a fixture that set only
			// one would resolve to nothing for half of them, and the turn would
			// stop at the empty-agent card before reaching the backend they are
			// actually about.
			InternalDefaultAgent: "default",
			ExternalDefaultAgent: "default",
		}},
		lark:  lark,
		llm:   llm,
		sess:  sess,
		group: convID,
		dr: &runtime.DynamicRuntime{
			Source:       runtime.RequestSourceLark,
			Receiver:     &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: convID},
			LarkServices: lark,
		},
	}
}

// holdRunningTurn takes the conversation's busy lock, as a turn in flight would.
func (f *aiQueueFixture) holdRunningTurn(t *testing.T) {
	t.Helper()
	if _, ok := f.sess.Lock().TryLock("running-turn"); !ok {
		t.Fatal("TryLock failed on an unlocked session lock")
	}
}

// TestRunAIRejectsWhenQueueFull pins the answer a user gets when the
// conversation is saturated: a plain notice, and no card. Creating the card
// first — which the turn used to do — is what made a busy conversation look like
// a failure.
func TestRunAIRejectsWhenQueueFull(t *testing.T) {
	f := newAIQueueFixture(t, 0, 1)
	f.holdRunningTurn(t)

	if err := f.biz.runAI(context.Background(), f.dr, "第二条消息", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if len(f.lark.cardRunIDs) != 0 {
		t.Errorf("a rejected message created a streaming card (%v); it must not", f.lark.cardRunIDs)
	}
	if len(f.lark.texts) != 1 {
		t.Fatalf("sent %d texts, want exactly 1 queue notice: %v", len(f.lark.texts), f.lark.texts)
	}
	if !strings.Contains(f.lark.texts[0], "排队") {
		t.Errorf("notice %q does not tell the user the conversation is queued/full", f.lark.texts[0])
	}
	if f.llm.requests != nil {
		t.Error("a rejected message reached agent")
	}
}

// TestRunAIQueuesSecondMessage is the fix for the second message of a pair
// turning red: it is queued behind the running turn and acknowledged, with no
// card of its own.
func TestRunAIQueuesSecondMessage(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)
	f.holdRunningTurn(t)

	if err := f.biz.runAI(context.Background(), f.dr, "第二条消息", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if len(f.lark.cardRunIDs) != 0 {
		t.Errorf("a queued message created a streaming card (%v); the card belongs to the turn that runs", f.lark.cardRunIDs)
	}
	if f.sess.QueueDepth() != 1 {
		t.Errorf("QueueDepth() = %d, want 1", f.sess.QueueDepth())
	}
	if len(f.lark.texts) != 1 {
		t.Fatalf("sent %d texts, want exactly 1 queue notice: %v", len(f.lark.texts), f.lark.texts)
	}
	if !strings.Contains(f.lark.texts[0], "排队") {
		t.Errorf("notice %q does not tell the user their message is queued", f.lark.texts[0])
	}
	if f.llm.requests != nil {
		t.Error("a queued message reached agent before the running turn finished")
	}
}

// TestRunAIRunsTurnWhenConversationIsFree is the counterweight to the two tests
// above: a message that arrives when nothing is running still gets its card and
// its answer, with no queue notice.
func TestRunAIRunsTurnWhenConversationIsFree(t *testing.T) {
	f := newAIQueueFixture(t, 5, 1)

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if len(f.lark.cardRunIDs) != 1 {
		t.Fatalf("created %d streaming cards, want 1", len(f.lark.cardRunIDs))
	}
	if len(f.lark.texts) != 0 {
		t.Errorf("an admitted message sent a queue notice: %v", f.lark.texts)
	}
	if f.lark.closedCount() != 1 {
		t.Errorf("the streaming card was closed %d times, want 1", f.lark.closedCount())
	}
	if f.sess.Busy() {
		t.Error("the session is still busy after the turn returned; the lock was not released")
	}
	if len(f.llm.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(f.llm.requests))
	}
	if got := f.llm.requests[0].GetSessionId(); got != "oc_test_conv" {
		t.Errorf("agent session key = %q, want oc_test_conv", got)
	}
}

// TestRunAIAnswersConcurrentlyWhenCapacityAllows pins the concurrency mode: when
// the conversation has room for more than one turn, a second question is answered
// in its own turn — its own card, its own agent request — instead of being queued
// behind the first, which is what a capacity of 1 does (see the tests above).
func TestRunAIAnswersConcurrentlyWhenCapacityAllows(t *testing.T) {
	f := newAIQueueFixture(t, 5, 2)
	// One turn is already in flight, so one of the two slots is taken.
	f.holdRunningTurn(t)

	if err := f.biz.runAI(context.Background(), f.dr, "第二个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if len(f.lark.cardRunIDs) != 1 {
		t.Errorf("created %d streaming cards, want 1 for the second concurrent turn", len(f.lark.cardRunIDs))
	}
	if len(f.lark.texts) != 0 {
		t.Errorf("a concurrently answered message sent a queue notice: %v", f.lark.texts)
	}
	if f.sess.QueueDepth() != 0 {
		t.Errorf("QueueDepth() = %d, want 0 (the turn ran rather than queued)", f.sess.QueueDepth())
	}
	if len(f.llm.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1 for the second concurrent turn", len(f.llm.requests))
	}
}

// newAIStreamFixture turns incremental publishing on with no interval threshold,
// so every delta is observable, and scripts the agent's event stream.
func newAIStreamFixture(t *testing.T, events ...*llmv1.AgentStreamEvent) *aiQueueFixture {
	t.Helper()
	f := newAIQueueFixture(t, 5, 1)
	f.biz.sr.StreamingEnabled = true
	f.biz.sr.Streaming = stream.Config{MinDeltaChars: 1}
	f.llm.events = events
	return f
}

// TestRunAITurnStreamsIntoTheCardThenClosesWithTheWholeAnswer pins the shape of
// a normal turn: each lane reaches its own element as it grows, and the card is
// finalized once, carrying everything.
func TestRunAITurnStreamsIntoTheCardThenClosesWithTheWholeAnswer(t *testing.T) {
	f := newAIStreamFixture(t,
		reasoningEvent("先想"),
		answerEvent("第一段"),
		answerEvent("第二段"),
	)

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if got := f.lark.lastUpdate("answer"); got != "第一段第二段" {
		t.Errorf("the answer element holds %q, want the accumulated answer", got)
	}
	if got := f.lark.lastUpdate("reasoning"); got != "先想" {
		t.Errorf("the reasoning element holds %q, want the accumulated reasoning", got)
	}
	if n := len(f.lark.updates["answer"]); n != 2 {
		t.Errorf("the answer element was written %d times for 2 deltas, want 2", n)
	}
	if f.lark.closedCount() != 1 {
		t.Errorf("the card was closed %d times, want 1", f.lark.closedCount())
	}
	if n := len(f.lark.closedCards()); n == 1 && !strings.Contains(f.lark.closedCards()[0], "第一段第二段") {
		t.Errorf("the final card does not carry the whole answer:\n%s", f.lark.closedCards()[0])
	}
	if len(f.lark.markdowns) != 0 {
		t.Errorf("a successful turn sent a fallback message: %v", f.lark.markdowns)
	}
	// The reasoning outlives the turn. It used to exist only in the live card's
	// panel, which the final card replaced wholesale, so the one part of the turn a
	// reader might want to go back over was the part that was thrown away.
	if n := len(f.lark.closedCards()); n == 1 {
		if !strings.Contains(f.lark.closedCards()[0], "深度思考过程") {
			t.Errorf("the final card has no reasoning panel:\n%s", f.lark.closedCards()[0])
		}
		if !strings.Contains(f.lark.closedCards()[0], "先想") {
			t.Errorf("the final card's reasoning panel is empty:\n%s", f.lark.closedCards()[0])
		}
	}
}

// TestRunAITurnDropsTheReasoningPanelWhenTheSessionTurnedItOff pins the toggle: a
// conversation that turned reasoning off must not be shown a panel for it, and
// must not have reasoning requested of the backend either.
//
// The two used to disagree — the card was created with the panel unconditionally
// while the request read the preference — so a conversation with reasoning off got
// a panel that sat at its placeholder for the whole turn and then vanished.
//
// The scripted reasoning event is deliberate: the toggle has to win over a backend
// that streams reasoning it was not asked for, or the final card would show a panel
// the live card never had.
func TestRunAITurnDropsTheReasoningPanelWhenTheSessionTurnedItOff(t *testing.T) {
	f := newAIStreamFixture(t,
		reasoningEvent("不该出现"),
		toolStepEvent("Read", "ok"),
		answerEvent("答案"),
	)
	f.biz.setReasoning(f.group, false)

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	if len(f.lark.cardReasoning) != 1 || f.lark.cardReasoning[0] {
		t.Errorf("the card was created with showReasoning = %v, want exactly one card with false", f.lark.cardReasoning)
	}
	if len(f.llm.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(f.llm.requests))
	}
	// Presence as well as value. The field is optional, so a turn that left it
	// unset would let the server's show-reasoning default decide — and a default
	// of "on" would ask for reasoning this conversation turned off, reviving the
	// panel the toggle just removed.
	if f.llm.requests[0].GetGeneration() == nil || f.llm.requests[0].GetGeneration().Reasoning == nil {
		t.Error("the turn stated no reasoning preference at all; the server default would override it")
	}
	if f.llm.requests[0].GetGeneration().GetReasoning() {
		t.Error("the backend was asked for reasoning a conversation had turned off")
	}
	if n := len(f.lark.closedCards()); n == 1 {
		if strings.Contains(f.lark.closedCards()[0], "深度思考过程") {
			t.Errorf("a reasoning-off turn closed with a reasoning panel:\n%s", f.lark.closedCards()[0])
		}
		// The tool panel is the control: the point is that the reasoning panel is
		// gone, not that the card lost its panels.
		if !strings.Contains(f.lark.closedCards()[0], "工具调用") {
			t.Errorf("a reasoning-off turn closed without its tool panel:\n%s", f.lark.closedCards()[0])
		}
	}
}

// TestRunAITurnKeepsTheCardOpenForTheContinuation is the whole point of the
// human-in-the-loop turn: a run that pauses ends its stream without ending the
// turn, and the answer that continues it renders into the same card.
//
// Every step here is load-bearing. Finalizing on the suspend would put the Done
// footer under an answer that has not finished arriving; forgetting the parked
// turn would leave the answer with nothing to continue, which is what the second
// half of this test proves by exercising it; and recording the pending decision
// is what lets a plain-text reply answer the same pause the card's buttons do.
func TestRunAITurnKeepsTheCardOpenForTheContinuation(t *testing.T) {
	f := newAIStreamFixture(t,
		answerEvent("前半段"),
		interruptedEvent("req-1", "Bash"),
		suspendedEvent(),
	)
	// The message transport is pointed at an endpoint that refuses every call.
	// Nothing on this path may reach it: the pause is drawn on the card the turn
	// already has, through the card services, so a pause that fell back to sending
	// a message of its own would fail here, be logged, and leave the card with no
	// decision on it — which is what the element check below rules out.
	f.biz.retrier = imlark.NewRetrier(larkClientThatRefuses(t), "", "")

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("a paused turn failed: %v", err)
	}

	if f.lark.closedCount() != 0 {
		t.Errorf("the card was finalized %d times on a pause, want 0", f.lark.closedCount())
	}
	if !f.lark.hasLiveElement(decisionLogElementID) {
		t.Error("the pause drew no decision on the card, so it needed the transport that refuses everything")
	}
	if got := f.lark.lastUpdate("answer"); got != "前半段" {
		t.Errorf("the answer element holds %q, want what had arrived before the pause", got)
	}
	// The turn is remembered so the continuation can find it, and the decision is
	// recorded so a plain "允许" (allow) reaches the same run the buttons would.
	turn := f.biz.suspendedTurn(f.group)
	if turn == nil {
		t.Fatal("the paused conversation has no turn recorded; nothing can continue it")
	}
	pending := f.sess.PendingPermission()
	if pending == nil {
		t.Fatal("the pause recorded no pending decision")
	}
	if pending.RunID != turn.runID || pending.RequestID != "req-1" {
		t.Errorf("the pending decision is (run %q, request %q), want (run %q, request req-1)",
			pending.RunID, pending.RequestID, turn.runID)
	}

	// The human answers. The backend hands the rest of the run to the sink the
	// first half was given, so it lands in the card that is already on screen.
	f.llm.approveEvents = []*llmv1.AgentStreamEvent{answerEvent("后半段"), doneEvent("前半段后半段")}
	if err := f.biz.answerRun(context.Background(), f.group, agent.AnswerRequest{
		RunID:     pending.RunID,
		RequestID: pending.RequestID,
		Decision:  agent.DecisionAllow,
	}); err != nil {
		t.Fatalf("answering the pause failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for f.lark.closedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := f.lark.lastUpdate("answer"); got != "前半段后半段" {
		t.Errorf("the answer element holds %q, want the two halves joined", got)
	}
	if f.lark.closedCount() != 1 {
		t.Fatalf("the card was finalized %d times, want exactly once when the run finished", f.lark.closedCount())
	}
	if n := len(f.lark.closedCards()); n == 1 && !strings.Contains(f.lark.closedCards()[0], "前半段后半段") {
		t.Errorf("the final card does not carry the whole answer:\n%s", f.lark.closedCards()[0])
	}
	// The answer was delivered as a resume, not by answering the same run twice.
	if got := f.llm.approvalsSeen(); len(got) != 1 || got[0].runID != pending.RunID ||
		got[0].requestID != pending.RequestID {
		t.Errorf("the backend was sent %+v, want one answer naming that run and pause", got)
	}
	if f.biz.suspendedTurn(f.group) != nil {
		t.Error("the conversation is still parked after its run finished")
	}
}

// TestRunAITurnDeliversTheAnswerWhenTheCardCannotBeWritten is the degradation
// policy, end to end: every card write fails, and the user still gets the answer.
// Rendering may be lost; content may not.
func TestRunAITurnDeliversTheAnswerWhenTheCardCannotBeWritten(t *testing.T) {
	f := newAIStreamFixture(t, answerEvent("一个重要答案"))
	f.lark.failUpdates = true
	f.lark.failClose = true

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("a card that cannot be written failed the turn itself: %v", err)
	}

	if len(f.lark.markdowns) != 1 {
		t.Fatalf("sent %d fallback messages, want exactly 1 carrying the answer: %v",
			len(f.lark.markdowns), f.lark.markdowns)
	}
	if f.lark.markdowns[0] != "一个重要答案" {
		t.Errorf("the fallback message carries %q, want the whole answer", f.lark.markdowns[0])
	}
	if f.sess.Busy() {
		t.Error("the session is still busy after a degraded turn; the lock was not released")
	}
}

// TestRunAITurnThrottlesCardWrites is the reason the turn goes through a
// preview at all: a hundred deltas must not be a hundred card updates. Here both
// thresholds block every intermediate publish, so only the closing flush reaches
// the card — and it carries everything.
func TestRunAITurnThrottlesCardWrites(t *testing.T) {
	f := newAIStreamFixture(t)
	f.biz.sr.Streaming = stream.Config{Interval: time.Hour, MinDeltaChars: 1000}

	var full strings.Builder
	for i := range 100 {
		delta := "第" + strconv.Itoa(i) + "段"
		full.WriteString(delta)
		f.llm.events = append(f.llm.events, answerEvent(delta))
	}

	if err := f.biz.runAI(context.Background(), f.dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}

	updates := f.lark.updates["answer"]
	if len(updates) != 1 {
		t.Fatalf("the card was written %d times for 100 deltas, want 1 (the closing flush)", len(updates))
	}
	if updates[0] != full.String() {
		t.Errorf("the closing flush carried %d characters, want all %d",
			len([]rune(updates[0])), len([]rune(full.String())))
	}
}

// TestRunAIWithoutSessionManagerStillAnswers covers the unserialised path: with
// no manager configured the turn runs inline rather than being dropped.
func TestRunAIWithoutSessionManagerStillAnswers(t *testing.T) {
	lark := &fakeLarkServices{}
	llm := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Clientset: &fakeClientset{llm: llm},
		Agents:    llmRegistry(llm),
		BotName:   "Helper",
		// Both audiences, for the reason the queue fixture gives.
		InternalDefaultAgent: "default",
		ExternalDefaultAgent: "default",
	}}
	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		Receiver:     &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_no_manager"},
		LarkServices: lark,
	}

	if err := b.runAI(context.Background(), dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}
	if len(lark.cardRunIDs) != 1 {
		t.Errorf("created %d streaming cards, want 1", len(lark.cardRunIDs))
	}
	if len(llm.requests) != 1 {
		t.Errorf("agent saw %d requests, want 1", len(llm.requests))
	}
}

// TestTheAgentRequestCarriesTheCallersRole pins where the run's role assertion
// comes from: the runtime's own resolution, which is the value the command tree
// already gates on — not a second lookup keyed on the conversation.
//
// The difference is not cosmetic. A group conversation is keyed by its chat id,
// so asking the role lists about that key asked about nobody, and every group
// turn told the agent service its caller was not an SRE while the same person's
// commands were gated as one.
func TestTheAgentRequestCarriesTheCallersRole(t *testing.T) {
	lark := &fakeLarkServices{}
	llm := &fakeAgent{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Clientset: &fakeClientset{llm: llm},
		Agents:    llmRegistry(llm),
		BotName:   "Helper",
		// Both audiences, for the reason the queue fixture gives.
		InternalDefaultAgent: "default",
		ExternalDefaultAgent: "default",
	}}
	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		IsSRE:        true,
		Sender:       v1.NewSender("go.kong", ""),
		Receiver:     &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_group"},
		LarkServices: lark,
	}

	if err := b.runAI(context.Background(), dr, "一个问题", nil); err != nil {
		t.Fatalf("runAI returned error: %v", err)
	}
	if len(llm.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(llm.requests))
	}
	if got := llm.requests[0].GetSubject().GetId(); got != "go.kong" {
		t.Errorf("SenderID = %q, want %q (the run is attributed to the caller)", got, "go.kong")
	}
}
