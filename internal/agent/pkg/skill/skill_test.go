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

package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// ── frontmatter ─────────────────────────────────────────────────────────────

func TestParseFrontmatter(t *testing.T) {
	t.Run("standard delimiters", func(t *testing.T) {
		fm, body, err := parseFrontmatter("---\nname: demo\ntype: skill\n---\nthe body\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(fm, "name: demo") {
			t.Errorf("frontmatter = %q", fm)
		}
		// The whole document is trimmed before parsing, so the body keeps its
		// internal newlines but not the file's trailing one.
		if body != "the body" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("missing opening delimiter is an error", func(t *testing.T) {
		if _, _, err := parseFrontmatter("name: demo\n---\n"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("missing closing delimiter is an error", func(t *testing.T) {
		if _, _, err := parseFrontmatter("---\nname: demo\n"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestParseSkillMD_RequiresName(t *testing.T) {
	if _, err := parseSkillMD("---\ntype: skill\n---\nbody", "dir"); err == nil {
		t.Fatal("a skill without a name must be rejected: the name is how it is requested")
	}
	entry, err := parseSkillMD("---\nname: demo\ndescription: d\n---\nbody\n", "root/demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.frontMatter.Name != "demo" || entry.content != "body" || entry.baseDir != "root/demo" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

// ── Feishu document skill detection ─────────────────────────────────────────

func TestIsSkillDocument(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantIs   bool
		wantName string
	}{
		{name: "fenced yaml with type skill", in: "```yaml\nname: demo\ntype: skill\n```\nbody", wantIs: true, wantName: "demo"},
		{name: "plain delimiters", in: "---\nname: demo\ntype: skill\n---\nbody", wantIs: true, wantName: "demo"},
		{name: "spacing tolerated", in: "---\nname: demo\ntype:  skill\n---\nbody", wantIs: true, wantName: "demo"},
		{name: "no type marker", in: "---\nname: demo\n---\nbody", wantIs: false},
		{name: "ordinary page", in: "# just a page\n", wantIs: false},
		{name: "marker but no name", in: "---\ntype: skill\n---\nbody", wantIs: false},
		{name: "leading bom", in: "\ufeff---\nname: demo\ntype: skill\n---\nbody", wantIs: true, wantName: "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fm := isSkillDocument(tc.in)
			if got != tc.wantIs {
				t.Fatalf("isSkillDocument = %v, want %v", got, tc.wantIs)
			}
			if got && fm.Name != tc.wantName {
				t.Errorf("name = %q, want %q", fm.Name, tc.wantName)
			}
		})
	}
}

func TestExtractDocToken(t *testing.T) {
	cases := map[string]string{
		"https://acme.feishu.cn/docx/ABC123":            "ABC123",
		"https://acme.feishu.cn/wiki/WIKI9?from=search": "WIKI9",
		"https://acme.feishu.cn/docx/ABC123#heading":    "ABC123",
		"ABC123":                                      "ABC123",
		"https://github.com/org/repo":                 "", // external links are not documents
		"https://example.com/wiki/ABC":                "",
		"https%3A%2F%2Facme.feishu.cn%2Fwiki%2FWIKI9": "WIKI9",
	}
	for in, want := range cases {
		if got := extractDocToken(in); got != want {
			t.Errorf("extractDocToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── archive helpers ─────────────────────────────────────────────────────────

// buildZip creates an in-memory archive from path → content.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractSkillsFromZip(t *testing.T) {
	data := buildZip(t, map[string]string{
		"repo-main/skills/alpha/SKILL.md":        "---\nname: alpha\ndescription: a\n---\nalpha body\n",
		"repo-main/skills/alpha/references/r.md": "reference",
		"repo-main/skills/beta/SKILL.md":         "---\nname: beta\n---\nbeta body\n",
		"repo-main/README.md":                    "readme",
	})

	skills, files, err := extractSkillsFromZip(data, "")
	if err != nil {
		t.Fatalf("extractSkillsFromZip: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("found %d skills, want 2", len(skills))
	}
	names := []string{skills[0].frontMatter.Name, skills[1].frontMatter.Name}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) && !reflect.DeepEqual(names, []string{"beta", "alpha"}) {
		t.Errorf("unexpected skill names %v", names)
	}
	// The whole file map is kept so a skill's reference files are servable.
	if _, ok := files["repo-main/skills/alpha/references/r.md"]; !ok {
		t.Error("reference file was not retained")
	}
	if _, ok := files["repo-main/README.md"]; !ok {
		t.Error("non-skill files must still be retained for the filesystem surface")
	}
}

func TestExtractSkillsFromZip_SubDirFilter(t *testing.T) {
	data := buildZip(t, map[string]string{
		"repo-main/skills/alpha/SKILL.md": "---\nname: alpha\n---\n",
		"repo-main/other/beta/SKILL.md":   "---\nname: beta\n---\n",
	})
	skills, _, err := extractSkillsFromZip(data, "skills")
	if err != nil {
		t.Fatalf("extractSkillsFromZip: %v", err)
	}
	if len(skills) != 1 || skills[0].frontMatter.Name != "alpha" {
		t.Fatalf("subdir filter did not apply: %+v", skills)
	}
}

func TestExtractSkillsFromZip_MalformedSkillIsSkipped(t *testing.T) {
	data := buildZip(t, map[string]string{
		"repo-main/a/SKILL.md": "no frontmatter here",
		"repo-main/b/SKILL.md": "---\nname: good\n---\n",
	})
	skills, _, err := extractSkillsFromZip(data, "")
	if err != nil {
		t.Fatalf("extractSkillsFromZip: %v", err)
	}
	if len(skills) != 1 || skills[0].frontMatter.Name != "good" {
		t.Fatalf("expected only the well-formed skill, got %+v", skills)
	}
}

// ── persistence safety ──────────────────────────────────────────────────────

// TestSaveSkillToDirWithFiles_RejectsTraversal pins that a repository cannot
// write outside the skills root by naming an entry "../..". The prefix filter
// alone does not stop it: "skills/foo/../../etc/x" has the skill directory as a
// prefix and then escapes through the join.
func TestSaveSkillToDirWithFiles_RejectsTraversal(t *testing.T) {
	skillsDir := t.TempDir()
	backend := &archiveBackend{
		skillsDir: skillsDir,
		agentKey:  "agent",
		repoKey:   "github.com/o/r",
		branch:    "main",
	}

	entry := archiveSkillEntry{baseDir: "repo-main/skills/foo"}
	files := map[string]string{
		"repo-main/skills/foo/../../../escaped.txt": "pwned",
		"repo-main/skills/foo/SKILL.md":             "ok",
	}

	if _, err := backend.saveSkillToDirWithFiles(entry, files); err == nil {
		t.Fatal("an entry escaping the skills root was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(skillsDir), "escaped.txt")); err == nil {
		t.Error("a file was written outside the skills root")
	}
}

func TestIsContainedRelPath(t *testing.T) {
	cases := map[string]bool{
		"SKILL.md":        true,
		"references/x.md": true,
		"../escape":       false,
		"a/../../escape":  false,
		"/etc/passwd":     false,
		"":                false,
		"./ok.md":         true,
	}
	for in, want := range cases {
		if got := isContainedRelPath(in); got != want {
			t.Errorf("isContainedRelPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWriteFileIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := writeFileIfChanged(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	info1, _ := os.Stat(path)
	if err := writeFileIfChanged(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("an unchanged write must not touch the file, or a reader sees spurious churn")
	}
	if err := writeFileIfChanged(path, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2" {
		t.Errorf("content = %q, want v2", got)
	}
}

// ── provider registry ───────────────────────────────────────────────────────

func TestRegisteredProviders(t *testing.T) {
	got := RegisteredProviders()
	want := []string{ProviderGitHub, ProviderGitLab, ProviderLarkDoc}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("provider %q is not registered; registered: %v", w, got)
		}
	}
	if _, ok := lookupProvider("skillhub"); ok {
		t.Error("skillhub must no longer be registered")
	}
}

func TestLookupProvider_AliasesAndNormalization(t *testing.T) {
	if _, ok := lookupProvider("LARK_DOC"); !ok {
		t.Error("provider lookup must be case-insensitive")
	}
	if _, ok := lookupProvider("feishu_doc"); !ok {
		t.Error("the feishu_doc alias must resolve to lark_doc")
	}
	if _, ok := lookupProvider("nope"); ok {
		t.Error("an unknown provider must not resolve")
	}
}

func TestDecodeParams(t *testing.T) {
	var p model.GitHubParams
	if err := decodeParams(`{"repo_url":"https://github.com/o/r"}`, &p); err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if p.RepoURL != "https://github.com/o/r" {
		t.Errorf("repo_url = %q", p.RepoURL)
	}
	if err := decodeParams("", &p); err == nil {
		t.Error("empty params must be an error, not silently zero")
	}
	if err := decodeParams("{not json", &p); err == nil {
		t.Error("malformed params must be an error")
	}
}

// ── token resolution ────────────────────────────────────────────────────────

type fakeSecrets struct {
	byID map[string]*model.SecretM
}

func (f fakeSecrets) Get(_ context.Context, opts *where.Options) (*model.SecretM, error) {
	id, _ := opts.Filters["secret_id"].(string)
	m, ok := f.byID[id]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return m, nil
}

func TestResolveToken(t *testing.T) {
	secrets := fakeSecrets{byID: map[string]*model.SecretM{
		"ok":    {SecretID: "ok", SecretKey: "from-store"},
		"empty": {SecretID: "empty"},
	}}
	ctx := context.Background()

	t.Run("inline token is used when no secret is named", func(t *testing.T) {
		got, err := resolveToken(ctx, secrets, "", "inline")
		if err != nil || got != "inline" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})

	t.Run("a named secret wins over the inline value", func(t *testing.T) {
		got, err := resolveToken(ctx, secrets, "ok", "inline")
		if err != nil || got != "from-store" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})

	t.Run("a missing secret is an error, not an empty token", func(t *testing.T) {
		if _, err := resolveToken(ctx, secrets, "absent", "inline"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("an empty secret is an error", func(t *testing.T) {
		if _, err := resolveToken(ctx, secrets, "empty", ""); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a named secret with no store is an error", func(t *testing.T) {
		if _, err := resolveToken(ctx, nil, "ok", ""); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// ── manager ─────────────────────────────────────────────────────────────────

func TestManager_CachesByAgentKeyAndFingerprint(t *testing.T) {
	m := NewManager(t.TempDir(), "", ProviderDeps{})
	record := model.SkillM{Name: "s", Provider: ProviderGitHub, Status: model.StatusEnabled,
		Params: `{"repo_url":"https://github.com/o/r"}`, UpdatedAt: 1}

	// Two different agent keys must not share a backend: they check files out
	// into different directories.
	a := m.GetOrCreateBackend(context.Background(), "agent-a", record)
	b := m.GetOrCreateBackend(context.Background(), "agent-b", record)
	if a == nil || b == nil {
		t.Fatal("expected a backend for each agent key")
	}

	// The same key returns the cached instance.
	if again := m.GetOrCreateBackend(context.Background(), "agent-a", record); again != a {
		t.Error("the same (agentKey, skill) must reuse its cached backend")
	}

	// A changed record must rebuild.
	record.UpdatedAt = 2
	if changed := m.GetOrCreateBackend(context.Background(), "agent-a", record); changed == a {
		t.Error("a changed record must invalidate the cached backend")
	}
}

func TestManager_UnknownProviderYieldsNoBackend(t *testing.T) {
	m := NewManager(t.TempDir(), "", ProviderDeps{})
	record := model.SkillM{Name: "s", Provider: "not-a-provider", Status: model.StatusEnabled}
	if b := m.GetOrCreateBackend(context.Background(), "a", record); b != nil {
		t.Error("an unknown provider must not produce a backend")
	}
}

func TestBuildAll_DisabledRecordIsSkipped(t *testing.T) {
	record := model.SkillM{
		Name: "s", Status: model.StatusDisabled, Params: `{"repo_url":"https://github.com/o/r"}`,
	}
	if b := BuildAll(context.Background(), []*model.SkillM{&record}, BuildSpec{}); b != nil {
		t.Error("a disabled skill must not be built")
	}
}

func TestBuildAll_NoSkillsIsNilNotError(t *testing.T) {
	if b := BuildAll(context.Background(), nil, BuildSpec{}); b != nil {
		t.Error("no skills must yield a nil backend, which the caller reads as 'no skill tool'")
	}
}

func TestAgentDirSegment(t *testing.T) {
	cases := map[string]string{
		"":          "__shared__",
		"plain":     "plain",
		"a/b":       "a_b",
		`a\b`:       "a_b",
		"../../etc": "____etc",
	}
	for in, want := range cases {
		if got := AgentDirSegment(in); got != want {
			t.Errorf("AgentDirSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsolatedSkillsDir_Layout(t *testing.T) {
	got := IsolatedSkillsDir("/root/skills", "a.b")
	want := filepath.Join("/root/skills", "a.b")
	if got != want {
		t.Errorf("IsolatedSkillsDir = %q, want %q", got, want)
	}
}

// ── renderer ────────────────────────────────────────────────────────────────

func TestDocumentLink_DepthBoundsTraversal(t *testing.T) {
	b := &larkDocBackend{}
	refs := map[string]bool{}

	// At depth 0 the target is collected and linked locally.
	got := b.documentLink("title", "wiki:ABC", "https://x.feishu.cn/wiki/ABC", 0, refs)
	if !refs["wiki:ABC"] {
		t.Error("a depth-0 reference must be recorded for download")
	}
	if got != "[title](./references/referenced_ABC.md)" {
		t.Errorf("depth-0 link = %q", got)
	}

	// At depth 1 it stays an ordinary link and is not collected.
	refs = map[string]bool{}
	got = b.documentLink("title", "wiki:ABC", "https://x.feishu.cn/wiki/ABC", 1, refs)
	if len(refs) != 0 {
		t.Error("a nested reference must not be collected; that is what bounds the traversal")
	}
	if got != "[title](https://x.feishu.cn/wiki/ABC)" {
		t.Errorf("depth-1 link = %q", got)
	}
}
