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

package duty

import (
	"context"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/spf13/cobra"
	"gorm.io/gorm"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/dutyservice"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/clientset/agent"
)

// fakeAgent replays one scripted answer for the smart-routing call and
// records the requests it was asked to answer. The embedded interface supplies
// the methods the duty commands never call.
type fakeAgent struct {
	agent.Interface

	answer string
	err    error
	reqs   []*llmv1.ChatCompletionRequest
}

func (f *fakeAgent) ChatCompletions(_ context.Context, req *llmv1.ChatCompletionRequest) (*llmv1.ChatCompletionResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	return &llmv1.ChatCompletionResponse{
		Choices: []*llmv1.ChatCompletionChoice{{
			Message: &llmv1.ChatCompletionMessage{Role: "assistant", Content: f.answer},
		}},
	}, nil
}

// fakeClientset supplies the one client the smart-routing path reaches for.
type fakeClientset struct {
	clientset.Interface
	llm agent.Interface
}

func (f *fakeClientset) Agent() agent.Interface { return f.llm }

// fakeLark records the outbound Lark calls the duty commands make. The embedded
// interface supplies the methods these tests never call.
type fakeLark struct {
	runtime.LarkServices

	chatNames []string
	invites   [][]string
	created   []string
	// texts is every SendText, which is how the closing summary reports back to
	// the group after the command's own reply has been flushed.
	texts []string
	// markdowns is every SendMarkdownCard, which is how a ticket group's
	// welcome message reaches the new chat.
	markdowns []sentMarkdown
}

type sentMarkdown struct {
	ChatID  string
	Title   string
	Content string
	Color   string
}

func (f *fakeLark) UpdateChatName(_ context.Context, chatID, name string) error {
	f.chatNames = append(f.chatNames, chatID+"="+name)
	return nil
}

func (f *fakeLark) UpdateChatAvatar(context.Context, string, string) error { return nil }

func (f *fakeLark) InviteMembers(_ context.Context, _ string, _ string, members []string) error {
	f.invites = append(f.invites, members)
	return nil
}

func (f *fakeLark) CreateChat(_ context.Context, _, title string, _ *v1.Watcher) (string, error) {
	f.created = append(f.created, title)
	return "oc_new", nil
}

func (f *fakeLark) SendText(_ context.Context, _ *v1.Receiver, content string) error {
	f.texts = append(f.texts, content)
	return nil
}

// ResolveUserName answers with a deterministic display name, so tests can
// assert on reporter/author names without a contact API.
func (f *fakeLark) ResolveUserName(_ context.Context, userID string) string {
	return "name-of-" + userID
}

func (f *fakeLark) SendCard(context.Context, *v1.Receiver, string, map[string]interface{}) error {
	return nil
}

func (f *fakeLark) SendMarkdownCard(_ context.Context, recv *v1.Receiver, title, content, color string) error {
	f.markdowns = append(f.markdowns, sentMarkdown{
		ChatID: recv.GetId(), Title: title, Content: content, Color: color,
	})
	return nil
}

// fakeTicketStore is an in-memory TicketStore.
type fakeTicketStore struct {
	store.TicketStore
	ticket *model.TicketM
}

func (s *fakeTicketStore) Create(_ context.Context, obj *model.TicketM) error {
	s.ticket = obj
	return nil
}

func (s *fakeTicketStore) Update(_ context.Context, obj *model.TicketM) error {
	s.ticket = obj
	return nil
}

func (s *fakeTicketStore) Get(context.Context, *where.Options) (*model.TicketM, error) {
	if s.ticket == nil {
		return nil, store.ErrRecordNotFound
	}
	return s.ticket, nil
}

// UpdateSummary writes the summary if the ticket is still empty, which is the part
// of the real store's contract a caller branches on — see the SQL store for what
// the conditional UPDATE does under concurrency.
func (s *fakeTicketStore) UpdateSummary(_ context.Context, ticketID, summary string, at time.Time) (bool, error) {
	if s.ticket == nil || s.ticket.TicketID != ticketID || s.ticket.Summary != "" {
		return false, nil
	}
	s.ticket.Summary = summary
	s.ticket.UpdatedAt = at
	return true, nil
}

// fakeSessionStore is an in-memory SessionStore.
type fakeSessionStore struct {
	store.SessionStore
	session *model.SessionM
}

func (s *fakeSessionStore) Update(_ context.Context, obj *model.SessionM) error {
	s.session = obj
	return nil
}

func (s *fakeSessionStore) Get(context.Context, *where.Options) (*model.SessionM, error) {
	if s.session == nil {
		return nil, store.ErrRecordNotFound
	}
	return s.session, nil
}

// fakeTicketNoteStore is an in-memory TicketNoteStore, appended to by /mark.
type fakeTicketNoteStore struct {
	store.TicketNoteStore
	notes []*model.TicketNoteM
}

func (s *fakeTicketNoteStore) Create(_ context.Context, obj *model.TicketNoteM) error {
	s.notes = append(s.notes, obj)
	return nil
}

// ListByTicket returns one ticket's notes oldest-first, as the real store does.
// It has no limit semantics beyond "0 means all", because that is the only call
// the summary path makes.
func (s *fakeTicketNoteStore) ListByTicket(_ context.Context, ticketID string, limit int) (int64, []*model.TicketNoteM, error) {
	mine := make([]*model.TicketNoteM, 0, len(s.notes))
	for _, n := range s.notes {
		if n != nil && n.TicketID == ticketID {
			mine = append(mine, n)
		}
	}
	total := int64(len(mine))
	if limit > 0 && limit < len(mine) {
		mine = mine[:limit]
	}
	return total, mine, nil
}

// fakeStore implements the slice of store.IStore the duty commands touch.
type fakeStore struct {
	store.IStore
	tickets     *fakeTicketStore
	ticketNotes *fakeTicketNoteStore
	sessions    *fakeSessionStore
}

func (s *fakeStore) DB(context.Context, ...where.Where) *gorm.DB { return nil }
func (s *fakeStore) TX(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *fakeStore) Ticket() store.TicketStore         { return s.tickets }
func (s *fakeStore) TicketNote() store.TicketNoteStore { return s.ticketNotes }
func (s *fakeStore) Session() store.SessionStore       { return s.sessions }

// testEnv bundles the runtime, fakes and runtime.DynamicRuntime a command test
// drives.
type testEnv struct {
	sr        *runtime.StaticRuntime
	lark      *fakeLark
	store     *fakeStore
	llm       *fakeAgent
	dr        *runtime.DynamicRuntime
	dutyStore *dutystore.Store
}

// setDutyStore swaps the concrete store the tests manipulate and rewraps it as
// the legacy-only service the commands consume.
func (e *testEnv) setDutyStore(store *dutystore.Store) {
	e.dutyStore = store
	e.sr.Duty = dutyservice.NewLegacyOnly(store)
}

// newTestEnv builds a StaticRuntime with one duty group ("eks" with primary
// "go.kong") and a ticket-group session.
func newTestEnv(t *testing.T, ticket *model.TicketM, session *model.SessionM) *testEnv {
	t.Helper()

	lark := &fakeLark{}
	llm := &fakeAgent{}
	st := &fakeStore{
		tickets:     &fakeTicketStore{ticket: ticket},
		ticketNotes: &fakeTicketNoteStore{},
		sessions:    &fakeSessionStore{session: session},
	}

	dutyStore := dutystore.New()
	dutyStore.RegisterGroup(&v1.OnDutyGroup{
		GroupId:   "eks",
		GroupName: "EKS",
		ShortDesc: "容器平台",
	})
	dutyStore.SetOncall("eks", []*v1.Watcher{{UserId: "go.kong", UserName: "Lingfei Kong"}})

	sr := &runtime.StaticRuntime{
		Store:     st,
		Duty:      dutyservice.NewLegacyOnly(dutyStore),
		Clientset: &fakeClientset{llm: llm},
		// The closing summary runs beside the reply in a deployment and would then
		// still be running when the test asserts. Running it inline makes the
		// command's whole effect observable at the point the command returns —
		// see StaticRuntime.Async.
		Async: func(f func()) { f() },
	}

	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		LarkServices: lark,
		Sender:       v1.NewSender("go.kong", ""),
		Receiver:     &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_1"},
		SessionM:     session,
		TicketM:      ticket,
	}
	return &testEnv{sr: sr, lark: lark, store: st, llm: llm, dr: dr, dutyStore: dutyStore}
}

// exec builds a command with a fresh structured responder, runs it with the
// test's runtime injected into the context, and returns the captured result plus
// the command error.
func (e *testEnv) exec(t *testing.T, build func(runtime.Responder) *cobra.Command, args ...string) (*runtime.CommandResult, error) {
	t.Helper()
	out := runtime.NewStructuredResponder(e.dr)
	cmd := build(out)
	ctx := runtime.Inject(context.Background(), e.dr)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.Result(), err
}
