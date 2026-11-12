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

package agentcli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionWorkspace returns the working directory for one agent session under
// root, creating it if needed.
//
// Each session gets its own directory. A shared directory would let one
// conversation's agent read and overwrite another's work — and, since the agent
// runs commands, a shared tree is a shared blast radius for whatever those
// commands do.
//
// root is required: an empty root means the caller has not decided where
// sessions may run, and returning the process's own working directory there
// would quietly grant the agent the whole checkout.
func SessionWorkspace(root, key string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("agent workspace root is not configured")
	}
	if strings.TrimSpace(key) == "" {
		return "", errors.New("agent session key is empty")
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", root, err)
	}

	dir := filepath.Join(abs, workspaceName(key))
	// Belt and braces: workspaceName already cannot produce a separator, so this
	// can only fail if that changes — which is exactly when it should.
	if !within(abs, dir) {
		return "", fmt.Errorf("session key %q resolves outside the workspace root %q", key, abs)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create workspace %q: %w", dir, err)
	}
	return dir, nil
}

// Within reports whether path is inside root, which is how a caller checks a
// directory the agent asked for (a "/dir" command) against the tree it is
// allowed to use.
func Within(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return within(absRoot, absPath)
}

// AllowedRoots returns the trees a session may be pointed at: the workspace root
// plus whatever extra directories the deployment allows.
//
// The extra directories exist because a workspace root is chosen for isolation,
// not for usefulness — an operator who wants a conversation to work on a checkout
// that already lives elsewhere should not have to move it under the root. Empty
// entries are dropped and duplicates removed, so a caller can pass the
// configured list straight through.
func AllowedRoots(root string, extra ...string) []string {
	roots := make([]string, 0, 1+len(extra))
	seen := make(map[string]struct{}, 1+len(extra))
	for _, candidate := range append([]string{root}, extra...) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		roots = append(roots, abs)
	}
	return roots
}

// WithinAny reports whether path is inside one of roots.
func WithinAny(roots []string, path string) bool {
	for _, root := range roots {
		if Within(root, path) {
			return true
		}
	}
	return false
}

// ResolveDir turns a requested working directory into the absolute path a turn
// will run in, or explains why it cannot.
//
// It is the authority on the confinement, not the caller: whoever accepts a
// directory — a command, a stored snapshot, an HTTP client — may be out of date
// or wrong, and this is where a path that escaped is stopped. The directory must
// already exist: creating it would make a typo look like a working directory, and
// the agent would run in an empty tree wondering where the files went.
func ResolveDir(roots []string, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("agent working directory is empty")
	}
	if len(roots) == 0 {
		return "", errors.New("no directory is allowed: the workspace root is not configured")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", path, err)
	}
	if !WithinAny(roots, abs) {
		return "", fmt.Errorf("working directory %q is outside %s", path, strings.Join(roots, ", "))
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("working directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", path)
	}
	return abs, nil
}

// within reports whether path is root itself or inside it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// Rel is ".." or "../x" for anything above root. Testing the prefix rather
	// than comparing with ".." also catches "..foo", which is a sibling
	// directory whose name merely starts with dots.
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// workspaceName is the directory name for a session key: a readable prefix for
// a human looking at the directory, plus a digest for uniqueness.
//
// The key arrives from a chat platform and is not a path, so it is reduced to
// characters a single path element can hold. The digest is what makes the
// reduction safe: two different keys that reduce to the same readable prefix
// still get different directories.
//
// The keys themselves are not reversible from the name, which is deliberate —
// a directory listing should not disclose chat IDs.
func workspaceName(key string) string {
	sum := sha256.Sum256([]byte(key))
	digest := hex.EncodeToString(sum[:])[:16]

	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	// Trim the filler the substitution produces at either end, so a key made
	// entirely of non-alphanumerics leaves just the digest rather than a name
	// that reads as "nothing here".
	prefix := strings.Trim(b.String(), "-_")
	if prefix == "" {
		return digest
	}
	return prefix + "-" + digest
}
