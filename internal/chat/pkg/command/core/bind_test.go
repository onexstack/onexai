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

package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// bindReply keeps what /bind answered, so a test can read it as the user would.
//
// SendResult replaces rather than appends: a command that answered twice is a
// bug, and appending would hide it.
type bindReply struct {
	mu   sync.Mutex
	text string
	sent bool
}

func (r *bindReply) Write(p []byte) (int, error) { return len(p), nil }
func (r *bindReply) SendText(_ context.Context, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text, r.sent = content, true
	return nil
}
func (r *bindReply) SendCard(context.Context, string, map[string]any) error { return nil }
func (r *bindReply) SendMarkdownCard(context.Context, string, string, string) error {
	return nil
}
func (r *bindReply) SendInteractiveCard(context.Context, any) error { return nil }
func (r *bindReply) SendResult(_ context.Context, res *runtime.CommandResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text, r.sent = res.PlainText, true
	return nil
}

func (r *bindReply) answer() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text
}

// stubClaims is a PeerBindClaimStore that records what it was asked to claim and
// answers from a scripted state.
type stubClaims struct {
	store.PeerBindClaimStore

	// won is what Claim reports. A lost race is the ordinary case for the
	// branches that read the row back, so it is the zero value.
	won bool
	// confirm is what Claim hands back, whatever won says. Keeping the two
	// independent is deliberate: the losing branches must not render a
	// confirmation code even if one were handed to them, and the only way to
	// assert that is to hand them one. See the taken/stale tests.
	confirm string

	// reissueConfirm and reissueOK are what Reissue reports; the repeat path
	// turns on both.
	reissueConfirm string
	reissueOK      bool

	// row is what Get reports. Nil means "nothing live under this code".
	row *model.PeerBindClaimM
	// claimErr, when set, is returned by Claim; reissueErr by Reissue.
	claimErr   error
	reissueErr error

	mu           sync.Mutex
	calls        int
	reissueCalls int
	code         string
	peerID       string
	scope        string
	ttl          time.Duration
}

func (s *stubClaims) Claim(_ context.Context, code, backend, scope, peerID string, ttl time.Duration) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.code, s.scope, s.peerID, s.ttl = code, scope, peerID, ttl
	return s.won, s.confirm, s.claimErr
}

func (s *stubClaims) Reissue(_ context.Context, code, peerID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reissueCalls++
	s.code, s.peerID = code, peerID
	return s.reissueConfirm, s.reissueOK, s.reissueErr
}

func (s *stubClaims) GetLive(context.Context, string) (*model.PeerBindClaimM, error) {
	return s.row, nil
}

func (s *stubClaims) CleanupExpired(context.Context) (int64, error) { return 0, nil }

func (s *stubClaims) recorded() (code, scope, peerID string, ttl time.Duration, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code, s.scope, s.peerID, s.ttl, s.calls
}

func (s *stubClaims) reissues() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reissueCalls
}

// bindStore implements store.IStore with only the claim store wired; /bind
// touches nothing else.
type bindStore struct {
	claims store.PeerBindClaimStore
}

func (s *bindStore) DB(context.Context, ...where.Where) *gorm.DB { return nil }
func (s *bindStore) TX(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *bindStore) Session() store.SessionStore                   { return nil }
func (s *bindStore) Ticket() store.TicketStore                     { return nil }
func (s *bindStore) TicketNote() store.TicketNoteStore             { return nil }
func (s *bindStore) Job() store.JobStore                           { return nil }
func (s *bindStore) CronJob() store.CronJobStore                   { return nil }
func (s *bindStore) Messages() store.MessageStore                  { return nil }
func (s *bindStore) ChatMessages() store.ChatMessageStore          { return nil }
func (s *bindStore) PeerAgents() store.PeerAgentStore              { return nil }
func (s *bindStore) PeerBindClaims() store.PeerBindClaimStore      { return s.claims }
func (s *bindStore) Users() store.UserStore                        { return nil }
func (s *bindStore) OncallGroups() store.OncallGroupStore          { return nil }
func (s *bindStore) OncallSchedules() store.OncallScheduleStore    { return nil }
func (s *bindStore) TicketEscalation() store.TicketEscalationStore { return nil }
func (s *bindStore) Metadata() store.MetadataStore                 { return nil }

// privateCallerForBind is somebody in a direct conversation with the bot.
func privateCallerForBind(openID string) *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		Sender:   v1.NewSender("", openID),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: openID},
	}
}

// bindFixture is a deployment whose claim store is the given stub.
func bindFixture(claims store.PeerBindClaimStore) *runtime.StaticRuntime {
	return &runtime.StaticRuntime{
		Store:            &bindStore{claims: claims},
		AppID:            "cli_test_app",
		PeerBindClaimTTL: 15 * time.Minute,
	}
}

// runBind drives the three phases in the order the command does, so a phase that
// fails to record its work is caught here rather than in production.
func runBind(t *testing.T, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime, args []string) error {
	t.Helper()

	o := &BindInfoOptions{out: out}
	ctx := runtime.Inject(context.Background(), dr)
	if err := o.Complete(ctx, args); err != nil {
		return err
	}
	if err := o.Validate(ctx, args); err != nil {
		return err
	}
	return o.Run(ctx, sr, dr)
}

func TestBindClaimsCodeForSender(t *testing.T) {
	claims := &stubClaims{won: true, confirm: "483920"}
	sr := bindFixture(claims)
	out := &bindReply{}

	if err := runBind(t, sr, out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}

	code, scope, peerID, ttl, calls := claims.recorded()
	if calls != 1 {
		t.Fatalf("Claim called %d times, want 1", calls)
	}
	// The triple is the key the message path reads back; see schedule.go.
	if code != "CODECODE" || scope != sr.AppID || peerID != "ou_ada" {
		t.Errorf("Claim(code=%q, scope=%q, peerID=%q), want (CODECODE, %s, ou_ada)", code, scope, peerID, sr.AppID)
	}
	if ttl != 15*time.Minute {
		t.Errorf("Claim ttl = %s, want 15m", ttl)
	}
	got := out.answer()
	if !strings.Contains(got, "已收到绑定码") {
		t.Errorf("answer = %q, want it to say the code was received", got)
	}
	// The speaker has to be told the confirmation code, or there is nothing for
	// the console to ask for and the binding can never complete. It must not be
	// the binding code itself: that one is already in the message they sent.
	if !strings.Contains(got, "483920") {
		t.Errorf("answer = %q, want the confirmation code the store handed back", got)
	}
	if strings.Contains(got, "CODECODE") {
		t.Errorf("answer = %q, must not echo the binding code back", got)
	}
	// And whoever is now holding a confirmation code has to be told not to pass
	// it on. This is the one sentence standing behind the residual case: the
	// console cannot tell whose confirm code it was handed, only that the caller
	// owns the binding code — so a confirm code that travels lets an account
	// holder type somebody else's, which is how an attacker would still land a
	// binding without ever touching this account. Nobody who forwards one of
	// these should be able to say they were not told.
	if !strings.Contains(got, "不要转发") {
		t.Errorf("answer = %q, want it to tell the holder not to pass the confirmation code on", got)
	}
}

// A claim is a claim regardless of who wins the insert race: what the reply may
// not say is that the binding succeeded, because this service cannot know.
func TestBindNeverClaimsTheBindingSucceeded(t *testing.T) {
	sr := bindFixture(&stubClaims{won: true})
	out := &bindReply{}

	if err := runBind(t, sr, out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	if got := out.answer(); !strings.Contains(got, "控制台") {
		t.Errorf("answer = %q, want it to send the user back to the console", got)
	}
}

// There is deliberately no test here for a code spoken in a group. That caller
// no longer reaches this command at all: /bind carries x-p2p-only, so the root's
// PersistentPreRunE refuses it before Run is called (see the gate tests in
// package command, and TestBindRefusedInAGroupChat there for this exact
// command). What is left to test in this file is what Run does for the caller
// who does get this far, and every one of them is in a direct conversation.

// TestBindIdempotentResend covers the repeat. Only the digest of the first
// confirmation code is kept, so "say the same thing again" is not available —
// the second send has to mint a new one, and the reply has to say that the older
// code stopped working. A user holding two codes and filling in the first would
// otherwise read the mismatch as a broken feature.
func TestBindIdempotentResend(t *testing.T) {
	claims := &stubClaims{
		won:            false,
		reissueOK:      true,
		reissueConfirm: "513748",
		row:            &model.PeerBindClaimM{Code: "CODECODE", PeerID: "ou_ada", ExpiresAt: time.Now().Add(time.Minute)},
	}
	out := &bindReply{}

	if err := runBind(t, bindFixture(claims), out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	got := out.answer()
	if !strings.Contains(got, "之前已经发送过") {
		t.Errorf("answer = %q, want it to read as a repeat rather than a refusal", got)
	}
	if !strings.Contains(got, "513748") {
		t.Errorf("answer = %q, want the reissued confirmation code", got)
	}
	// Same obligation as the first send: a reply that hands out a confirmation
	// code is a reply that has to say not to pass it on.
	if !strings.Contains(got, "不要转发") {
		t.Errorf("answer = %q, want it to tell the holder not to pass the confirmation code on", got)
	}
	if claims.reissues() != 1 {
		t.Errorf("Reissue called %d times, want 1 — a repeat has to mint a code the user can read", claims.reissues())
	}
}

// TestBindRepeatWithoutALiveRowIsStale covers the gap between the read and the
// rotation: the row lapsed, so there is nothing to confirm against. Answering
// "this is a repeat, here is a new code" would send the user to a console that
// has already given up on the code.
func TestBindRepeatWithoutALiveRowIsStale(t *testing.T) {
	claims := &stubClaims{
		won: false,
		// Reissue declines because the row expired between Get and here.
		reissueOK: false,
		row:       &model.PeerBindClaimM{Code: "CODECODE", PeerID: "ou_ada", ExpiresAt: time.Now().Add(time.Minute)},
	}
	out := &bindReply{}

	if err := runBind(t, bindFixture(claims), out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	if got := out.answer(); !strings.Contains(got, "已失效或已过期") {
		t.Errorf("answer = %q, want the stale answer when there is nothing left to rotate", got)
	}
}

// TestBindCodeTakenByAnotherPeer is the invariant the second half of the
// handshake rests on. The claim on the row names somebody else, so this caller
// did not make it — and a confirmation code is a code for the attempt the bot
// heard, which was not this one.
//
// The stub is deliberately scripted to hand back a code anyway. The store never
// does (see TestPeerBindClaimLoserGetsNoConfirmationCode); asserting it here is
// what keeps that a property of two layers rather than one, so a future refactor
// of either cannot quietly start speaking a stranger's code into this chat.
func TestBindCodeTakenByAnotherPeer(t *testing.T) {
	claims := &stubClaims{
		won:     false,
		confirm: "313131",
		row:     &model.PeerBindClaimM{Code: "CODECODE", PeerID: "ou_somebody_else", ExpiresAt: time.Now().Add(time.Minute)},
	}
	out := &bindReply{}

	if err := runBind(t, bindFixture(claims), out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	got := out.answer()
	if !strings.Contains(got, "已经被另一个飞书账号使用") {
		t.Errorf("answer = %q, want it to say the code was taken", got)
	}
	// The code may be perfectly valid; saying it is invalid would be a lie the
	// user acts on by retrying the same message.
	if strings.Contains(got, "无效") {
		t.Errorf("answer = %q, must not call a valid code invalid", got)
	}
	if strings.Contains(got, "313131") {
		t.Errorf("answer = %q, must not hand out a confirmation code for a claim this caller did not make", got)
	}
	if claims.reissues() != 0 {
		t.Errorf("Reissue called %d times for a claim naming somebody else", claims.reissues())
	}
}

func TestBindExpiredClaim(t *testing.T) {
	claims := &stubClaims{won: false, confirm: "313131", row: nil}
	out := &bindReply{}

	if err := runBind(t, bindFixture(claims), out, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	got := out.answer()
	if !strings.Contains(got, "已失效或已过期") {
		t.Errorf("answer = %q, want it to report the code as no longer live", got)
	}
	if strings.Contains(got, "313131") {
		t.Errorf("answer = %q, must not hand out a confirmation code for a claim that does not exist", got)
	}
}

func TestBindRejectsUnknownSender(t *testing.T) {
	claims := &stubClaims{won: true}
	dr := &runtime.DynamicRuntime{
		// A direct receiver — the only kind that can still reach Run, see the
		// note above — carrying no sender identity at all. A card action whose
		// clicker resolved to no id arrives like this, and the claim it would
		// write names nobody.
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeUserID},
	}

	err := runBind(t, bindFixture(claims), &bindReply{}, dr, []string{"CODECODE"})
	if !runtime.IsUserError(err) {
		t.Fatalf("err = %v, want a UserError", err)
	}
	if _, _, _, _, calls := claims.recorded(); calls != 0 {
		t.Errorf("Claim called %d times, want 0 — there is no identity to record", calls)
	}
}

func TestBindWithoutStore(t *testing.T) {
	sr := &runtime.StaticRuntime{PeerBindClaimTTL: time.Minute}

	err := runBind(t, sr, &bindReply{}, privateCallerForBind("ou_ada"), []string{"CODECODE"})
	if !runtime.IsUserError(err) {
		t.Fatalf("err = %v, want a UserError", err)
	}
}

func TestBindFallsBackToADefaultTTL(t *testing.T) {
	claims := &stubClaims{won: true}
	sr := &runtime.StaticRuntime{Store: &bindStore{claims: claims}} // no PeerBindClaimTTL

	if err := runBind(t, sr, &bindReply{}, privateCallerForBind("ou_ada"), []string{"CODECODE"}); err != nil {
		t.Fatalf("runBind returned error: %v", err)
	}
	if _, _, _, ttl, _ := claims.recorded(); ttl != defaultClaimTTL {
		t.Errorf("Claim ttl = %s, want the %s fallback", ttl, defaultClaimTTL)
	}
}

// The arity is checked in Validate rather than by cobra's ExactArgs because
// cobra's message is English and is not a UserError, so it would reach the user
// wrapped in a trace id.
func TestBindValidateRejectsWrongArity(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		err := runBind(t, bindFixture(&stubClaims{}), &bindReply{}, privateCallerForBind("ou_ada"), args)
		if !runtime.IsUserError(err) {
			t.Errorf("args %v: err = %v, want a UserError", args, err)
		}
	}
}

func TestBindValidateRejectsBlankCode(t *testing.T) {
	err := runBind(t, bindFixture(&stubClaims{}), &bindReply{}, privateCallerForBind("ou_ada"), []string{"   "})
	if !runtime.IsUserError(err) {
		t.Fatalf("err = %v, want a UserError", err)
	}
}

// A binding code is base64url of 32 random bytes, whose alphabet includes "-",
// so about one code in 64 starts with one. Without DisableFlagParsing, pflag
// reads it as a cluster of shorthand flags and the command fails — which looks
// exactly like the bot ignoring the user, one time in 64.
//
// This drives the real cobra command rather than Run, because flag parsing is
// precisely the layer Run sits below.
func TestBindAcceptsCodeWithLeadingDash(t *testing.T) {
	claims := &stubClaims{won: true}
	sr := bindFixture(claims)
	out := &bindReply{}

	cmd := NewBindCmd(sr, out)
	ctx := runtime.Inject(context.Background(), privateCallerForBind("ou_ada"))
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-ab"})

	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("ExecuteContext returned error: %v", err)
	}

	code, _, _, _, calls := claims.recorded()
	if calls != 1 {
		t.Fatalf("Claim called %d times, want 1", calls)
	}
	if code != "-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-ab" {
		t.Errorf("Claim code = %q, want the code verbatim", code)
	}
}

// Every user has to be able to bind, so no role may gate the command, and it
// must not be offered as an MCP tool: a tool call carries no chat identity.
func TestBindCommandAnnotations(t *testing.T) {
	cmd := NewBindCmd(bindFixture(&stubClaims{}), &bindReply{})

	var annotations map[string]string = cmd.Annotations
	if annotations[chat.AdminOnly] == chat.True || annotations[chat.SREOnly] == chat.True {
		t.Errorf("annotations = %v, want no role gate", annotations)
	}
	if annotations[chat.MCPTool] == chat.True {
		t.Errorf("annotations = %v, want no MCP tool exposure", annotations)
	}
	if annotations[chat.LarkOnly] != chat.True {
		t.Errorf("annotations = %v, want LarkOnly — a claim needs an open_id", annotations)
	}
	// The one restriction that stays. A binding code is won by whoever says it
	// to the bot first, so a code spoken in a group is one the room can claim.
	if annotations[chat.P2POnly] == "" {
		t.Errorf("annotations = %v, want P2POnly — a group can claim a code it can read", annotations)
	}
	// And its value has to be the refusal itself: the generic one would send the
	// caller off to repeat a code that is already in other people's hands.
	if annotations[chat.P2POnly] == chat.True {
		t.Error("the P2POnly value is the generic refusal, but this command has something of its own to say")
	}
}
