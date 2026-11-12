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
	"reflect"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
)

// preferenceBot builds a bot over a session manager in a temporary directory.
// The bot is constructed directly rather than with New, which builds a Lark
// retrier from a client this test does not have: nothing under test here talks
// to Lark.
func preferenceBot(t *testing.T) (*botBiz, *session.Manager) {
	t.Helper()

	mgr := session.NewManager(context.Background(), session.Options{DataDir: t.TempDir()})
	return &botBiz{sr: &runtime.StaticRuntime{Sessions: mgr}}, mgr
}

// TestReasoningPreferenceSurvivesANewBot pins why the toggle moved off the bot.
//
// It used to be a map on the bot, which meant the preference lasted exactly as
// long as the process: a restart silently turned reasoning back on. Reading it
// back through a second bot — the same thing a restart produces — is what shows
// the preference now belongs to the conversation.
func TestReasoningPreferenceSurvivesANewBot(t *testing.T) {
	first, mgr := preferenceBot(t)

	// Nothing has been toggled, so the historical default stands.
	if !first.reasoningEnabled("oc_1") {
		t.Error("a fresh conversation reports reasoning off, want the default on")
	}

	first.setReasoning("oc_1", false)

	// A new bot over the same manager is what a restart looks like from here.
	next := &botBiz{sr: &runtime.StaticRuntime{Sessions: mgr}}
	if next.reasoningEnabled("oc_1") {
		t.Error("reasoning is back on after a new bot was built, want the stored preference honoured")
	}
	// The other direction is stored too, so the toggle is a preference rather
	// than a one-way switch.
	next.setReasoning("oc_1", true)
	if !first.reasoningEnabled("oc_1") {
		t.Error("reasoning stayed off after being turned back on")
	}
}

// The agent override's own persistence test used to live here. It is gone with
// the mechanism it pinned: the override was a per-process snapshot, and it is now
// a column on chat.session (session.refs), so "does it survive a new bot" is not
// a question any more — two bots cannot disagree about a value they both read
// from the row. What did replace it is the scheduler's tests, which cover what
// the value resolves to.

// TestPreferencesDoNotCreateConversationsOnRead pins the direction a read fails
// in. Rendering a card is a read; if it created the conversation it named, a
// snapshot would be written for every conversation anyone ever looked at, and an
// idle scan would then have to reap them all.
func TestPreferencesDoNotCreateConversationsOnRead(t *testing.T) {
	b, mgr := preferenceBot(t)

	b.reasoningEnabled("oc_never_seen")
	b.sessionRefs(context.Background(), "oc_never_seen")

	if _, ok := mgr.Lookup("oc_never_seen"); ok {
		t.Error("reading a preference brought a conversation into existence")
	}
}

// TestBotKeepsNoConversationPreferences is the structural guard for the two
// tests above. A preference put back on the bot as a map would still pass them —
// they read through the accessors — so the shape of the struct is asserted
// directly.
//
// The six maps it may hold all key on data with no life beyond this process: a
// card message ID to refresh in place, a recalled message ID to ignore, a batch
// of images waiting for the coalescing window, the turns parked on a human
// decision, what those turns were waiting on when their waits ran out, and the
// messages that have already produced a turn. Anything else keyed by conversation
// is a preference, and a preference here is lost on restart and invisible to /new,
// /switch and /sessions.
//
// The parked turns are on the right side of that line by construction rather than
// by choice: what is held for each of them is a live context, a streaming preview
// and the sink a paused run's continuation is delivered to — none of which can be
// written to a snapshot. A pause is answered on the replica that started it, and a
// restart abandons it, exactly as it abandons the goroutines it was reading on.
//
// The expired pauses are the same fact one step later, and are the reason the
// parked turns cannot be the only record: expiry closes the card and deregisters
// the turn, so from that moment on the only thing left in this process that can
// tell a late click it is too late is the note of which pause it gave up on. That
// note is a run id held by another process — it means nothing after a restart, and
// a restart abandons the click's premise along with it.
//
// The claimed messages are on the right side of the line by the same two tests as
// the recalled ones beside them: they hold no intent of the user's, and they are
// not expected to outlive anything — a message re-delivered to a freshly started
// process is a message that process has no record of and goes on to answer, which
// is why the window is measured in seconds and the entry expires in an hour. It is
// keyed by conversation as well as by message so that one busy group cannot evict
// another conversation's entry, not because the entry belongs to the conversation.
func TestBotKeepsNoConversationPreferences(t *testing.T) {
	allowed := map[string]bool{
		"cardMsgIDs":    true,
		"recalled":      true,
		"imageBatches":  true,
		"suspended":     true,
		"expiredPauses": true,
		"claims":        true,
	}

	for field := range reflect.TypeFor[botBiz]().Fields() {
		if field.Type.Kind() != reflect.Map || allowed[field.Name] {
			continue
		}
		t.Errorf("botBiz.%s is a conversation-keyed map that is not one of the per-process caches: "+
			"conversation state belongs in the session manager", field.Name)
	}
}
