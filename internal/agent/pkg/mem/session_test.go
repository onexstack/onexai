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

package mem

import (
	"context"
	"testing"
	"time"

	agent "github.com/onexstack/onexai/pkg/agentapi"
)

// newTestStore returns the in-memory implementation, which is the one that can
// be exercised without a Redis server.
func newTestStore(t *testing.T) SessionStore {
	t.Helper()
	store := NewSessionStore(nil, "test", time.Hour)
	if _, ok := store.(*memSessionStore); !ok {
		t.Fatalf("expected the in-memory store, got %T", store)
	}
	return store
}

func message(role string, content string) agent.Message {
	return agent.Message{Role: role, Content: content}
}

func TestSessionStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if got, err := store.Load(ctx, "s"); err != nil || got != nil {
		t.Fatalf("Load on an absent session = (%v, %v), want (nil, nil)", got, err)
	}

	for _, m := range []agent.Message{message(agent.RoleUser, "hi"), message(agent.RoleAssistant, "hello")} {
		if err := store.Append(ctx, "s", m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Content != "hi" || got[1].Content != "hello" {
		t.Fatalf("unexpected history: %+v", got)
	}

	// Load must hand back a copy: a caller mutating what it got must not change
	// the stored history.
	got[0].Content = "mutated"
	again, _ := store.Load(ctx, "s")
	if again[0].Content != "hi" {
		t.Error("Load returned the store's own slice")
	}

	if err := store.Clear(ctx, "s"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if after, _ := store.Load(ctx, "s"); after != nil {
		t.Errorf("Clear left %d messages behind", len(after))
	}
}

// TestTrimOldest_ReportsWhatIsLeft pins the count. The caller that trims is
// usually deciding whether to trim again, and a follow-up Load is a second read
// that can disagree with the trim it is asking about.
func TestTrimOldest_ReportsWhatIsLeft(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, "s", message(agent.RoleUser, string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}

	remaining, err := store.TrimOldest(ctx, "s", 2)
	if err != nil {
		t.Fatalf("TrimOldest: %v", err)
	}
	if remaining != 3 {
		t.Errorf("remaining = %d, want 3", remaining)
	}

	history, _ := store.Load(ctx, "s")
	if len(history) != 3 || history[0].Content != "c" {
		t.Errorf("the oldest two were not the ones dropped: %+v", history)
	}

	// Trimming everything leaves nothing, and says so.
	if remaining, err = store.TrimOldest(ctx, "s", 99); err != nil || remaining != 0 {
		t.Errorf("TrimOldest(99) = (%d, %v), want (0, nil)", remaining, err)
	}

	// A non-positive count is a no-op that still reports the truth.
	if err := store.Append(ctx, "s", message(agent.RoleUser, "x")); err != nil {
		t.Fatal(err)
	}
	if remaining, err = store.TrimOldest(ctx, "s", 0); err != nil || remaining != 1 {
		t.Errorf("TrimOldest(0) = (%d, %v), want (1, nil)", remaining, err)
	}
}

func TestQuery(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for _, m := range []agent.Message{
		message(agent.RoleUser, "How do I restart the Payment service?"),
		message(agent.RoleAssistant, "Use kubectl rollout restart."),
		message(agent.RoleUser, "And the settlement service?"),
	} {
		if err := store.Append(ctx, "s", m); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("matching is case-insensitive", func(t *testing.T) {
		got, err := store.Query(ctx, "s", "payment")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 1 || got[0].Role != agent.RoleUser {
			t.Fatalf("unexpected matches: %+v", got)
		}
	})

	t.Run("every matching message is returned", func(t *testing.T) {
		got, _ := store.Query(ctx, "s", "service")
		if len(got) != 2 {
			t.Errorf("got %d matches, want 2", len(got))
		}
	})

	t.Run("an empty needle matches nothing rather than everything", func(t *testing.T) {
		if got, _ := store.Query(ctx, "s", "   "); got != nil {
			t.Errorf("an empty query returned %d messages; it must not be read as 'match all'", len(got))
		}
	})

	t.Run("a miss is empty", func(t *testing.T) {
		if got, _ := store.Query(ctx, "s", "nonexistent"); got != nil {
			t.Errorf("unexpected matches: %+v", got)
		}
	})
}

// TestLastActive_SurvivesATrim pins the reason last-active is stored beside the
// history rather than derived from it: a session trimmed to nothing is still a
// session that was used.
func TestLastActive_SurvivesATrim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if at, err := store.GetLastActive(ctx, "s"); err != nil || !at.IsZero() {
		t.Fatalf("an unused session's last-active = (%v, %v), want the zero time", at, err)
	}

	when := time.Now().Truncate(time.Millisecond)
	if err := store.SetLastActive(ctx, "s", when); err != nil {
		t.Fatalf("SetLastActive: %v", err)
	}

	if err := store.Append(ctx, "s", message(agent.RoleUser, "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrimOldest(ctx, "s", 99); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetLastActive(ctx, "s")
	if err != nil {
		t.Fatalf("GetLastActive: %v", err)
	}
	if !got.Equal(when) {
		t.Errorf("last-active = %v, want %v (it must survive the history being trimmed away)", got, when)
	}
}

func TestSaveReplacesHistory(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Append(ctx, "s", message(agent.RoleUser, "old")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "s", []agent.Message{message(agent.RoleUser, "new")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := store.Load(ctx, "s")
	if len(got) != 1 || got[0].Content != "new" {
		t.Errorf("Save did not replace the history: %+v", got)
	}
}
