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

package runtimecli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sessionStateVersion is the on-disk format's version. A file with any other
// value is refused rather than guessed at: it was written by a different
// runtime or a different version of this one.
const sessionStateVersion = 1

// sessionState is one conversation's native session identity.
type sessionState struct {
	Version   int    `json:"version"`
	Runtime   string `json:"runtime"`
	SessionID string `json:"session_id,omitempty"`
}

// sessionStore persists the CLI's own conversation id per A2A context.
//
// The id is opaque and belongs to the CLI: this package only remembers it. The
// alternative — replaying the whole transcript into every prompt — is both
// expensive and lossy, because the CLI's own tool history and file edits are
// not in the transcript at all.
//
// One file per context rather than one JSON object: a busy deployment would
// otherwise serialise every conversation behind one lock, and a crash mid-write
// would lose all of them instead of one.
type sessionStore struct {
	dir     string
	runtime string
}

// newSessionStore prepares the directory, or returns nil when the deployment
// did not configure one (continuation off).
func newSessionStore(dir, runtimeName string) (*sessionStore, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("runtimecli: prepare session directory: %w", err)
	}
	return &sessionStore{dir: dir, runtime: runtimeName}, nil
}

// path is the file one context's identity lives in. The context id is hashed
// because it is caller-chosen: it can be longer than a file name allows, and it
// can contain separators that would escape the directory.
func (s *sessionStore) path(contextID string) string {
	sum := sha256.Sum256([]byte(s.runtime + "\x00" + contextID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

// load returns the bound session id, if the context has one.
func (s *sessionStore) load(contextID string) (string, bool) {
	if s == nil || contextID == "" {
		return "", false
	}
	raw, err := os.ReadFile(s.path(contextID))
	if err != nil {
		return "", false
	}
	var state sessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", false
	}
	if state.Version != sessionStateVersion || state.Runtime != s.runtime || state.SessionID == "" {
		return "", false
	}
	return state.SessionID, true
}

// bind records the session id for a context.
//
// Rebinding to a *different* id is refused: a context is one conversation, and
// letting a second process claim it would silently fork the history the
// user thinks they are continuing.
func (s *sessionStore) bind(contextID, sessionID string) error {
	if s == nil || contextID == "" || sessionID == "" {
		return nil
	}
	if existing, ok := s.load(contextID); ok {
		if existing == sessionID {
			return nil
		}
		return fmt.Errorf("runtimecli: context is already bound to another session")
	}
	encoded, err := json.MarshalIndent(sessionState{
		Version:   sessionStateVersion,
		Runtime:   s.runtime,
		SessionID: sessionID,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("runtimecli: encode session state: %w", err)
	}
	// Written to a temporary file and renamed: a reader must never see a
	// half-written identity, and the rename is atomic on every platform this
	// runs on.
	temporary := s.path(contextID) + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("runtimecli: write session state: %w", err)
	}
	return os.Rename(temporary, s.path(contextID))
}
