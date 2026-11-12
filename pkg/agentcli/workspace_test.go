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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceIsolatedPerSession(t *testing.T) {
	root := t.TempDir()

	first, err := SessionWorkspace(root, "oc_chat_1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	second, err := SessionWorkspace(root, "oc_chat_2")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if first == second {
		t.Fatalf("two sessions share the workspace %q: one conversation's agent could read and overwrite another's work", first)
	}
	for _, dir := range []string{first, second} {
		if !Within(root, dir) {
			t.Errorf("workspace %q is not under the root %q", dir, root)
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("the workspace must be created: %v", err)
		}
		if !fi.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}

	// Asking for a session's workspace twice must land in the same place: the
	// whole point is that a second turn rejoins the first turn's tree.
	again, err := SessionWorkspace(root, "oc_chat_1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if again != first {
		t.Errorf("the same session got two workspaces: %q then %q", first, again)
	}
}

func TestWorkspaceIsPrivateToItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on windows")
	}
	dir, err := SessionWorkspace(t.TempDir(), "oc_chat_1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("workspace mode = %o, want 700: the agent's working files are not for other users", got)
	}
}

func TestWorkspaceRejectsEscape(t *testing.T) {
	root := t.TempDir()

	// A session key arrives from a chat platform. It is a name, not a path, and
	// none of these may produce a directory outside the root.
	for _, key := range []string{
		"../../etc",
		"..",
		"../sibling",
		"/etc/passwd",
		"a/../../b",
		"./../..",
		`..\..\windows`,
		strings.Repeat("../../", 20) + "deep",
	} {
		dir, err := SessionWorkspace(root, key)
		if err != nil {
			// Refusing outright is an acceptable answer.
			continue
		}
		if !Within(root, dir) {
			t.Errorf("key %q produced %q, outside the root %q", key, dir, root)
		}
		if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("key %q resolved to %q (rel %q), which leaves the root", key, dir, rel)
		}
	}
}

func TestWorkspaceRequiresARootAndAKey(t *testing.T) {
	// An empty root is the dangerous case: it would quietly hand the agent the
	// process's own working directory, which is the whole checkout.
	if _, err := SessionWorkspace("", "oc_chat_1"); err == nil {
		t.Error("an empty workspace root must be refused, not defaulted")
	}
	if _, err := SessionWorkspace("   ", "oc_chat_1"); err == nil {
		t.Error("a blank workspace root must be refused, not defaulted")
	}
	if _, err := SessionWorkspace(t.TempDir(), ""); err == nil {
		t.Error("an empty session key must be refused")
	}
	if _, err := SessionWorkspace(t.TempDir(), "  "); err == nil {
		t.Error("a blank session key must be refused")
	}
}

func TestWorkspaceNameDistinguishesKeysThatReduceAlike(t *testing.T) {
	// The readable prefix is capped; the digest is what makes the name unique.
	// Two long keys that agree for their whole prefix still need two directories,
	// or one chat's agent would find itself in another chat's tree.
	base := strings.Repeat("a", 64)
	if got, other := workspaceName(base+"x"), workspaceName(base+"y"); got == other {
		t.Errorf("two distinct keys share the workspace name %q", got)
	}

	// A key of nothing but punctuation keeps the digest rather than a name that
	// reads as empty.
	name := workspaceName("///")
	if name == "" || strings.HasPrefix(name, "-") {
		t.Errorf("workspaceName(%q) = %q, want a readable name starting with the digest", "///", name)
	}
	if strings.ContainsAny(name, `/\`) {
		t.Errorf("workspaceName(%q) = %q, which is not a single path element", "///", name)
	}
}

func TestWithin(t *testing.T) {
	root := t.TempDir()

	if !Within(root, root) {
		t.Error("the root is within itself")
	}
	if !Within(root, filepath.Join(root, "a", "b")) {
		t.Error("a descendant is within the root, whether or not it exists")
	}
	if Within(root, filepath.Join(root, "..")) {
		t.Error("the parent directory is not within the root")
	}
	if Within(root, root+"-sibling") {
		t.Error("a sibling whose name starts with the root's is not within it")
	}
	// A sibling called "..foo" is the case a naive "starts with .." test gets
	// wrong in the other direction, so it is worth naming.
	if Within(root, filepath.Join(filepath.Dir(root), "..foo")) {
		t.Error("a sibling named ..foo is not within the root")
	}
	if Within("", root) {
		t.Error("an empty root contains nothing")
	}
}

// TestAllowedRoots pins what the list a caller offers and a backend enforces is
// made of. It is built from configuration, so the failures it has to absorb are
// the ones configuration produces: a blank entry, the same directory named twice,
// a path written relative to where the process happens to have been started.
func TestAllowedRoots(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()

	for _, tc := range []struct {
		name  string
		root  string
		extra []string
		want  int
	}{
		{name: "the root alone", root: root, want: 1},
		{name: "extra directories are added", root: root, extra: []string{extra}, want: 2},
		{name: "blank entries are dropped", root: root, extra: []string{"", "  "}, want: 1},
		{name: "duplicates are removed", root: root, extra: []string{root, extra, root}, want: 2},
		{name: "an unset root leaves only the extras", root: "", extra: []string{extra}, want: 1},
		{name: "nothing configured is an empty list", root: "", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowedRoots(tc.root, tc.extra...)
			if len(got) != tc.want {
				t.Fatalf("AllowedRoots(%q, %v) = %v, want %d entries", tc.root, tc.extra, got, tc.want)
			}
			for _, r := range got {
				if !filepath.IsAbs(r) {
					t.Errorf("AllowedRoots returned the relative path %q", r)
				}
			}
		})
	}

	// The first root is what a new conversation defaults to, so the workspace root
	// has to stay ahead of the extras however they are written.
	got := AllowedRoots(root, extra)
	if len(got) > 0 && got[0] != root {
		t.Errorf("AllowedRoots put %q first, want the workspace root %q", got[0], root)
	}
}

// TestResolveDir pins the confinement, which is the whole reason this function
// exists: it is what a directory the user asked for is checked against before a
// process is pointed at it.
func TestResolveDir(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	roots := AllowedRoots(root, extra)
	inside := filepath.Join(root, "sub")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if got, err := ResolveDir(roots, inside); err != nil || got != inside {
		t.Errorf("ResolveDir(%q) = %q, %v; want the path itself", inside, got, err)
	}
	if got, err := ResolveDir(roots, root); err != nil || got != root {
		t.Errorf("ResolveDir(%q) = %q, %v; want the root to be usable", root, got, err)
	}
	// An extra directory is usable, and is the reason the list is not just the
	// root: an operator points a conversation at a checkout that already exists.
	if _, err := ResolveDir(roots, extra); err != nil {
		t.Errorf("ResolveDir(%q) = %v, want an allowed directory to be accepted", extra, err)
	}

	for _, tc := range []struct {
		name  string
		path  string
		roots []string
	}{
		{name: "outside every root", path: os.TempDir(), roots: roots},
		{name: "an escape written with a dot-dot", path: filepath.Join(root, "..", filepath.Base(extra)), roots: AllowedRoots(root)},
		{name: "a path that does not exist", path: filepath.Join(root, "gone"), roots: roots},
		{name: "a sibling whose name starts with the root", path: root + "-sibling", roots: AllowedRoots(root)},
		{name: "nothing allowed at all", path: root, roots: nil},
		{name: "an empty path", path: "", roots: roots},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ResolveDir(tc.roots, tc.path); err == nil {
				t.Errorf("ResolveDir(%v, %q) = %q, want a refusal", tc.roots, tc.path, got)
			}
		})
	}
}

// TestResolveDirRefusesAFile pins that the check is about directories: a path to
// a file is inside the root, exists, and would leave the agent unable to start —
// a failure that would surface as an unexplained turn error rather than as a
// rejected /dir.
func TestResolveDirRefusesAFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got, err := ResolveDir(AllowedRoots(root), file); err == nil {
		t.Errorf("ResolveDir(%q) = %q, want a file to be refused", file, got)
	}
}
