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

package agentbuilder

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/onexstack/onexstack/pkg/ptr"
)

func newTestBackend(t *testing.T, skillsDir string) *SafeLocalBackend {
	t.Helper()
	localFS, err := localbk.NewBackend(context.Background(), &localbk.Config{})
	if err != nil {
		t.Fatalf("failed to create local backend: %v", err)
	}
	return &SafeLocalBackend{Local: localFS, SkillsDir: skillsDir}
}

func TestSafeLocalBackend_CheckPath(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("failed to create skills dir: %v", err)
	}

	backend := newTestBackend(t, "")

	// 1. Unconfigured SkillsDir refuses everything.
	err := backend.Write(context.Background(), &filesystem.WriteRequest{
		FilePath: filepath.Join(skillsDir, "file.txt"), Content: "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "SkillsDir 未配置") {
		t.Errorf("expected SkillsDir unconfigured error, got: %v", err)
	}

	backend.SkillsDir = skillsDir

	// 2. Safe access (inside SkillsDir).
	inside := filepath.Join(skillsDir, "file.txt")
	if err := backend.Write(context.Background(), &filesystem.WriteRequest{
		FilePath: inside, Content: "hello",
	}); err != nil {
		t.Errorf("expected write to succeed, got error: %v", err)
	}
	fc, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: inside})
	if err != nil {
		t.Errorf("expected read to succeed, got error: %v", err)
	} else if fc.Content != "hello" {
		t.Errorf("expected content 'hello', got: %q", fc.Content)
	}

	// 3. Outside SkillsDir: Write errors, Read degrades to a model-visible refusal.
	outside := filepath.Join(tmpDir, "unsafe.txt")
	err = backend.Write(context.Background(), &filesystem.WriteRequest{FilePath: outside, Content: "unsafe"})
	if err == nil || !strings.Contains(err.Error(), "操作的文件或目录") {
		t.Errorf("expected path security interception error, got: %v", err)
	}
	fcUnsafe, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: outside})
	if err != nil {
		t.Errorf("expected read of unsafe path to return nil error, got: %v", err)
	} else if !strings.Contains(fcUnsafe.Content, blockedPrefix) {
		t.Errorf("expected read content to contain the block message, got: %q", fcUnsafe.Content)
	}

	// 4. Directory traversal.
	err = backend.Write(context.Background(), &filesystem.WriteRequest{
		FilePath: filepath.Join(skillsDir, "../unsafe.txt"), Content: "unsafe",
	})
	if err == nil || !strings.Contains(err.Error(), "操作的文件或目录") {
		t.Errorf("expected directory traversal interception, got: %v", err)
	}
}

// TestSafeLocalBackend_SymlinkEscape pins that a symlink inside SkillsDir
// pointing outside it cannot be used to read or write outside the sandbox —
// the check must resolve symlinks on both sides before comparing.
// TestSafeLocalBackend_EmptyPathStaysInTheSandbox pins the fix for a real
// bypass: the underlying backend reads an empty path as "." for a listing and
// as "/" for a glob, so an empty path used to walk the process working
// directory — or the whole filesystem — from inside the sandbox.
func TestSafeLocalBackend_EmptyPathStaysInTheSandbox(t *testing.T) {
	skillsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillsDir, "inside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newTestBackend(t, skillsDir)
	ctx := context.Background()

	t.Run("a listing with no path lists the sandbox root", func(t *testing.T) {
		infos, err := backend.LsInfo(ctx, &filesystem.LsInfoRequest{Path: ""})
		if err != nil {
			t.Fatalf("LsInfo: %v", err)
		}
		if len(infos) != 1 || infos[0].Path != "inside.txt" {
			t.Fatalf("an empty path did not resolve to the sandbox root: %+v", infos)
		}
	})

	t.Run("a glob with no path cannot walk the filesystem", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &filesystem.GlobInfoRequest{Path: "", Pattern: "*"})
		if err != nil {
			t.Fatalf("GlobInfo: %v", err)
		}
		for _, info := range infos {
			if strings.Contains(info.Path, "etc/") || strings.HasPrefix(info.Path, "/") {
				t.Fatalf("a glob with no path escaped the sandbox: %s", info.Path)
			}
		}
	})

	t.Run("a file operation with no path is refused", func(t *testing.T) {
		if err := backend.Write(ctx, &filesystem.WriteRequest{FilePath: "", Content: "x"}); err == nil {
			t.Error("writing to an empty path must be refused, not resolved to the sandbox root")
		}
		if fc, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "  "}); err != nil {
			t.Fatalf("Read returned an error instead of a refusal payload: %v", err)
		} else if !strings.Contains(fc.Content, blockedPrefix) {
			t.Errorf("reading an empty path was not blocked: %q", fc.Content)
		}
	})
}

func TestSafeLocalBackend_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	secretDir := filepath.Join(tmpDir, "secret")
	for _, d := range []string{skillsDir, secretDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	secretFile := filepath.Join(secretDir, "credentials.txt")
	if err := os.WriteFile(secretFile, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	link := filepath.Join(skillsDir, "escape")
	if err := os.Symlink(secretDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	backend := newTestBackend(t, skillsDir)

	// Reading through the symlink must be refused.
	fc, err := backend.Read(context.Background(), &filesystem.ReadRequest{
		FilePath: filepath.Join(link, "credentials.txt"),
	})
	if err != nil {
		t.Fatalf("expected nil error with a refusal payload, got: %v", err)
	}
	if !strings.Contains(fc.Content, blockedPrefix) {
		t.Errorf("symlink escape was not blocked; content = %q", fc.Content)
	}
	if strings.Contains(fc.Content, "top-secret") {
		t.Error("secret content leaked through the symlinked directory")
	}

	// Writing through the symlink must be refused with an error.
	if err := backend.Write(context.Background(), &filesystem.WriteRequest{
		FilePath: filepath.Join(link, "pwned.txt"), Content: "x",
	}); err == nil {
		t.Error("expected write through symlink to be refused")
	}
	if _, err := os.Stat(filepath.Join(secretDir, "pwned.txt")); err == nil {
		t.Error("write through symlink actually landed outside the sandbox")
	}
}

func TestSafeLocalBackend_ExecuteWhitelist(t *testing.T) {
	backend := newTestBackend(t, t.TempDir())
	ctx := context.Background()

	assertRejected := func(t *testing.T, resp *filesystem.ExecuteResponse, err error, want string) {
		t.Helper()
		if err != nil {
			t.Errorf("expected nil error (a refusal is a result), got: %v", err)
			return
		}
		if resp == nil || !ptr.EqualTo(resp.ExitCode, 127) {
			t.Errorf("expected exit code 127, got: %+v", resp)
			return
		}
		if !strings.Contains(resp.Output, want) {
			t.Errorf("expected output to contain %q, got: %q", want, resp.Output)
		}
	}

	t.Run("empty command", func(t *testing.T) {
		resp, err := backend.Execute(ctx, &filesystem.ExecuteRequest{Command: ""})
		assertRejected(t, resp, err, "执行命令不能为空")
	})

	t.Run("unallowed command", func(t *testing.T) {
		resp, err := backend.Execute(ctx, &filesystem.ExecuteRequest{Command: "ls -la"})
		assertRejected(t, resp, err, "该命令不在允许执行的白名单中")
	})

	t.Run("unauthorized executable with chained command", func(t *testing.T) {
		resp, err := backend.Execute(ctx, &filesystem.ExecuteRequest{Command: "rm -rf / && rg pattern ."})
		assertRejected(t, resp, err, "该命令不在允许执行的白名单中")
	})

	t.Run("metacharacters glued to a whitelisted executable", func(t *testing.T) {
		// "rg;whoami" tokenizes to a single arg whose filepath.Base is
		// "rg;whoami", which is not on the whitelist, so it must be refused.
		resp, err := backend.Execute(ctx, &filesystem.ExecuteRequest{Command: "rg;whoami"})
		assertRejected(t, resp, err, "该命令不在允许执行的白名单中")
	})

	t.Run("unconfigured skills dir", func(t *testing.T) {
		b := newTestBackend(t, "")
		resp, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: "rg foo"})
		assertRejected(t, resp, err, "SkillsDir 未配置")
	})
}

// TestExecuteStreaming_RejectionIsStreamed pins that the streaming variant
// reports a refusal as a one-element stream rather than an error.
func TestExecuteStreaming_RejectionIsStreamed(t *testing.T) {
	backend := newTestBackend(t, t.TempDir())
	sr, err := backend.ExecuteStreaming(context.Background(), &filesystem.ExecuteRequest{Command: "rm -rf /"})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	defer sr.Close()
	resp, err := sr.Recv()
	if err != nil {
		t.Fatalf("expected one streamed refusal, got error: %v", err)
	}
	if !strings.Contains(resp.Output, blockedPrefix) {
		t.Errorf("expected a block message, got: %q", resp.Output)
	}
}

func TestShellQuoting(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", `'plain'`},
		{"has space", `'has space'`},
		{"semi;colon", `'semi;colon'`},
		{"$(whoami)", `'$(whoami)'`},
		{"`id`", "'`id`'"},
		{"it's", `'it'\''s'`},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	// The dangerous characters must end up inside single quotes, where the
	// shell treats them literally.
	for _, dangerous := range []string{";", "|", "&&", "$(", "`"} {
		args, err := parseCommandLine("rg " + dangerous + " oops")
		if err != nil {
			t.Fatalf("parseCommandLine: %v", err)
		}
		quoted := quoteShellArgs(args)
		for _, a := range args {
			if !strings.Contains(quoted, shellQuote(a)) {
				t.Errorf("arg %q was not quoted in %q", a, quoted)
			}
		}
	}
}

func TestParseCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "simple", in: "rg -n foo", want: []string{"rg", "-n", "foo"}},
		{name: "extra whitespace", in: "  rg   -n   foo  ", want: []string{"rg", "-n", "foo"}},
		{name: "double quotes", in: `rg "a b"`, want: []string{"rg", "a b"}},
		{name: "single quotes", in: `rg 'a b'`, want: []string{"rg", "a b"}},
		{name: "escaped space", in: `rg a\ b`, want: []string{"rg", "a b"}},
		{name: "unclosed quote", in: `rg "a`, wantErr: true},
		{name: "empty", in: "   ", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommandLine(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
