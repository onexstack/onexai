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

package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleState(key string) State {
	return State{
		Key:      key,
		Platform: PlatformLark,
		Backend:  BackendCLI,
		// No AgentName here on purpose: the field was removed when the
		// conversation's agent declaration moved to chat.session.refs. It had
		// been a per-process snapshot, which meant two instances answering one
		// conversation could disagree about it.
		WorkDir:          "/var/lib/onexai/workspaces/" + key,
		BackendSessionID: "sess-abc-123",
		Mode:             "concise",
		LastUserActivity: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		CreatedAt:        time.Date(2026, 9, 26, 9, 30, 0, 0, time.UTC),
	}
}

// TestStateRoundTripViaDisk pins that every field survives a write and read.
// A field silently dropped here is a conversation that forgets its backend
// across a restart, which looks like a mysterious loss of context.
func TestStateRoundTripViaDisk(t *testing.T) {
	store := NewDiskStore(t.TempDir())
	want := sampleState("oc_abc123")

	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	got, found, err := store.Load(want.Key)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !found {
		t.Fatal("Load reported the snapshot as missing after a successful Save")
	}
	if got != want {
		t.Errorf("round-tripped state differs\n got: %+v\nwant: %+v", got, want)
	}
}

func TestLoadMissingSnapshotIsNotFound(t *testing.T) {
	store := NewDiskStore(t.TempDir())

	got, found, err := store.Load("never-written")
	if err != nil {
		t.Fatalf("Load on a missing snapshot returned error: %v", err)
	}
	if found {
		t.Errorf("Load reported found=true on a missing snapshot (got %+v)", got)
	}
}

// TestCorruptSnapshotIsDiscarded covers the recovery path: a snapshot that
// cannot be decoded must surface ErrCorrupt so the caller can start the
// conversation fresh, rather than either crashing or silently inventing state.
func TestCorruptSnapshotIsDiscarded(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated json", body: `{"key":"oc_abc123","backend":"cli"`},
		{name: "not json at all", body: "\x00\x01 not json"},
		{name: "empty file", body: ""},
		{name: "json of the wrong shape", body: `["oc_abc123"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewDiskStore(dir)

			path := filepath.Join(dir, FileName("oc_abc123"))
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("failed to plant a corrupt snapshot: %v", err)
			}

			_, found, err := store.Load("oc_abc123")
			if err == nil {
				t.Fatal("Load returned no error for a corrupt snapshot")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("Load error = %v, want it to wrap ErrCorrupt", err)
			}
			if found {
				t.Error("Load reported found=true alongside a corrupt error")
			}
		})
	}
}

// TestLoadRejectsMismatchedKey covers a snapshot whose stored key disagrees
// with the file it was found in. Trusting it would attach one conversation's
// backend and workspace to another.
func TestLoadRejectsMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskStore(dir)

	// Write valid JSON for a different key into this key's file.
	data, err := json.Marshal(sampleState("oc_someone_else"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName("oc_abc123")), data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, found, err := store.Load("oc_abc123")
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("Load error = %v, want it to wrap ErrCorrupt", err)
	}
	if found {
		t.Error("Load reported found=true for a snapshot belonging to another key")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store := NewDiskStore(t.TempDir())

	// Deleting something that was never written must not be an error; the
	// recovery path deletes corrupt snapshots and cannot assume they exist.
	if err := store.Delete("never-written"); err != nil {
		t.Errorf("Delete on a missing snapshot returned error: %v", err)
	}

	st := sampleState("oc_abc123")
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(st.Key); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, found, err := store.Load(st.Key); err != nil || found {
		t.Errorf("Load after Delete: found=%v err=%v, want found=false err=nil", found, err)
	}
	if err := store.Delete(st.Key); err != nil {
		t.Errorf("second Delete returned error: %v", err)
	}
}

func TestSaveRejectsEmptyKey(t *testing.T) {
	if err := NewDiskStore(t.TempDir()).Save(State{}); err == nil {
		t.Error("Save with an empty key returned no error")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskStore(dir)

	// An absent directory is a fresh install, not a failure.
	if states, err := store.List(); err != nil || len(states) != 0 {
		t.Errorf("List on an absent directory = (%v, %v), want (empty, nil)", states, err)
	}

	for _, key := range []string{"oc_a", "oc_b", "oc_c"} {
		if err := store.Save(sampleState(key)); err != nil {
			t.Fatal(err)
		}
	}
	// A stray non-snapshot file and a corrupt snapshot must not break the
	// listing, nor appear in it.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("scratch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName("oc_broken")), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}

	states, err := store.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("List returned %d snapshots, want 3", len(states))
	}

	seen := map[string]bool{}
	for _, st := range states {
		seen[st.Key] = true
	}
	for _, key := range []string{"oc_a", "oc_b", "oc_c"} {
		if !seen[key] {
			t.Errorf("List did not return snapshot %q", key)
		}
	}
	if seen["oc_broken"] {
		t.Error("List returned a corrupt snapshot")
	}
}

// TestFileNameDistinctKeysNeverCollide is the reason FileName carries a digest
// rather than just sanitising. Two distinct conversation ids that sanitise to
// the same string must still land in different files, or they would share a
// backend binding and a workspace.
func TestFileNameDistinctKeysNeverCollide(t *testing.T) {
	keys := []string{
		"oc_abc123",
		"oc/abc123", // sanitises to the same prefix as the above
		"oc:abc123", // and so does this
		"oc abc123", // and this
		"a/b", "a:b", "a b", "a.b",
		`..`, `/`, `\`, `oc_abc123/../..`,
	}

	seen := map[string]string{}
	for _, key := range keys {
		name := FileName(key)

		if strings.ContainsRune(name, os.PathSeparator) || strings.Contains(name, "/") {
			t.Errorf("FileName(%q) = %q, which contains a path separator", key, name)
		}
		if strings.HasPrefix(name, ".") {
			t.Errorf("FileName(%q) = %q, which is a hidden file", key, name)
		}
		if prev, ok := seen[name]; ok {
			t.Errorf("FileName(%q) and FileName(%q) both = %q; distinct keys must not share a snapshot",
				prev, key, name)
		}
		seen[name] = key
	}
}

func TestFileNameIsStable(t *testing.T) {
	key := "oc_abc123"
	first := FileName(key)
	for range 5 {
		if got := FileName(key); got != first {
			t.Fatalf("FileName(%q) is not stable: %q then %q", key, first, got)
		}
	}
	if filepath.Ext(first) != ".json" {
		t.Errorf("FileName(%q) = %q, want a .json extension", key, first)
	}
}

func TestFileNameSurvivesLongKey(t *testing.T) {
	name := FileName(strings.Repeat("x", 4096))
	if len(name) > 255 {
		t.Errorf("FileName of a 4096-char key is %d bytes, over the 255-byte file name limit", len(name))
	}
}
