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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCorrupt reports a snapshot file that exists but cannot be understood. It
// is not fatal: the caller discards the snapshot and starts the conversation
// fresh, because refusing to start over a damaged convenience file would be a
// worse outcome than losing a backend binding.
var ErrCorrupt = errors.New("session snapshot is corrupt")

// Store persists conversation snapshots.
type Store interface {
	// Load returns the snapshot for key. found is false when no snapshot
	// exists. A file that exists but cannot be decoded returns ErrCorrupt.
	Load(key string) (st State, found bool, err error)
	// Save writes the snapshot atomically.
	Save(st State) error
	// Delete removes the snapshot. Removing a missing snapshot is not an error.
	Delete(key string) error
	// List returns every snapshot. Unreadable files are skipped.
	List() ([]State, error)
}

// DiskStore is a Store backed by one JSON file per conversation under a
// directory. Writes go to a temporary file that is renamed into place, so a
// snapshot is never observed half-written even if the process dies mid-save.
type DiskStore struct {
	dir string
}

// NewDiskStore creates a DiskStore rooted at dir. The directory is created on
// first write rather than here, so constructing the store cannot fail on a
// read-only or not-yet-mounted filesystem.
func NewDiskStore(dir string) *DiskStore {
	return &DiskStore{dir: dir}
}

// Dir returns the directory snapshots are written to.
func (s *DiskStore) Dir() string { return s.dir }

// Load implements Store.
func (s *DiskStore) Load(key string) (State, bool, error) {
	path := s.path(key)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, false, nil
		}
		return State{}, false, fmt.Errorf("read session snapshot %s: %w", path, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	// A snapshot whose Key disagrees with the file it was found in cannot be
	// trusted to describe the conversation it was looked up under. This is
	// reachable if a file is copied or a digest scheme changes.
	if st.Key != key {
		return State{}, false, fmt.Errorf("%w: %s: key %q does not match lookup %q",
			ErrCorrupt, path, st.Key, key)
	}
	return st, true, nil
}

// Save implements Store.
func (s *DiskStore) Save(st State) error {
	if st.Key == "" {
		return errors.New("cannot save a session snapshot with an empty key")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create session dir %s: %w", s.dir, err)
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session snapshot: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, "snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp session snapshot: %w", err)
	}
	tmpName := tmp.Name()
	// Best effort: if the rename below succeeds the file is gone already.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write session snapshot: %w", err)
	}
	// Flush before the rename so a rename cannot publish a file whose contents
	// are still in the page cache.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync session snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session snapshot: %w", err)
	}

	// Temp files are created 0600 by os.CreateTemp; make the mode explicit so
	// the snapshot is not readable by other users on a shared host.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod session snapshot: %w", err)
	}
	if err := os.Rename(tmpName, s.path(st.Key)); err != nil {
		return fmt.Errorf("publish session snapshot: %w", err)
	}
	return nil
}

// Delete implements Store.
func (s *DiskStore) Delete(key string) error {
	if err := os.Remove(s.path(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete session snapshot: %w", err)
	}
	return nil
}

// List implements Store. A snapshot that cannot be decoded is skipped rather
// than failing the whole listing: one damaged file must not hide the rest.
func (s *DiskStore) List() ([]State, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list session dir %s: %w", s.dir, err)
	}

	states := make([]State, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var st State
		if err := json.Unmarshal(data, &st); err != nil || st.Key == "" {
			continue
		}
		states = append(states, st)
	}
	return states, nil
}

func (s *DiskStore) path(key string) string {
	return filepath.Join(s.dir, FileName(key))
}
