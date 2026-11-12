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

// Package session owns per-conversation state: which backend a conversation is
// bound to, what it is allowed to touch, and whether a turn is currently
// running.
//
// The state is a disk snapshot (one JSON file per conversation) rather than a
// database row, because it is small, single-writer, and worthless after a
// restart that cannot resume the process it describes. The busy lock is
// deliberately *not* persisted: a lock describes a running goroutine, so a lock
// that outlives the process would be a lock nothing can ever release.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// FileName returns the snapshot file name for a session key.
//
// Keys are platform identifiers and may contain characters that are illegal or
// meaningful in a path ("/", ":", ".."). Sanitising alone would be unsafe:
// "a/b" and "a:b" would collapse to the same file and two conversations would
// silently share state. The sanitised prefix keeps files recognisable by eye
// while a digest of the *original* key guarantees distinct keys never collide.
func FileName(key string) string {
	digest := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(digest[:])[:16]

	prefix := sanitize(key)
	// Leave room for the separator and the digest so the result stays well
	// inside any filesystem's name limit.
	const maxPrefix = 64
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	if prefix == "" {
		return suffix + ".json"
	}
	return prefix + "-" + suffix + ".json"
}

// sanitize maps a key to a filesystem-safe, still-recognisable prefix. Every
// character outside [A-Za-z0-9._-] becomes "_", and a leading "." is dropped
// so the result is never a hidden file or the "." / ".." entries.
func sanitize(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		case r == '.':
			// Keep interior dots (they are common in IDs) but never a leading
			// one, which would hide the file.
			if b.Len() > 0 {
				b.WriteRune('.')
			}
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
