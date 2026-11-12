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
// The original repo for this file is https://github.com/onexstack/onex.

package skill

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexstack/pkg/astore"
)

// skillFileName is the file that marks a directory as a skill.
const skillFileName = "SKILL.md"

// archiveSkillEntry is the provider-agnostic shape of a skill discovered inside
// a downloaded repository archive.
type archiveSkillEntry struct {
	frontMatter adkskill.FrontMatter
	content     string
	baseDir     string // path within the archive, including the archive's root prefix
}

// archiveCacheData is the cached payload of one archive download: the skills it
// contained plus every file's content, which is what serves filesystem
// operations against virtual (not-yet-persisted) paths.
type archiveCacheData struct {
	skills []archiveSkillEntry
	files  map[string]string
}

// archiveBackend holds everything the zip-based providers (github, gitlab)
// share: downloading and caching one archive, discovering the skills
// inside it, persisting a skill's files to disk, and serving filesystem
// operations either from that on-disk copy or from the in-memory archive.
//
// A concrete provider embeds this and implements only how to fetch its archive.
type archiveBackend struct {
	// local serves every operation on a path that exists on disk — which, after
	// a skill has been persisted, is the whole skill directory.
	local *localbk.Local
	// repoKey is the local directory segment derived from the repository
	// location, e.g. "github.com/owner/repo".
	repoKey string
	branch  string
	// skillsDir is the root the skills are persisted under. Empty disables
	// persistence, leaving skills to be served from the archive alone.
	skillsDir string
	// agentKey is the agent set this backend was built for. It is inserted as a
	// directory segment so unrelated agent sets never share — and therefore
	// never clobber — the same checked-out skill files.
	agentKey string
	// logPrefix tags log lines with the owning provider, e.g. "[skill/github]".
	logPrefix string

	// cache holds the downloaded archive as one immutable generation. A stale
	// generation is served while the next one downloads, and a failed download
	// leaves the previous archive in place.
	cache  *astore.Store[*archiveCacheData]
	diskMu sync.Mutex
}

// archiveData returns the cached archive, downloading it when stale.
func (b *archiveBackend) archiveData(ctx context.Context) (*archiveCacheData, error) {
	gen, err := b.cache.LoadE(ctx)
	if err != nil {
		return nil, err
	}
	if gen.Value == nil {
		return nil, fmt.Errorf("skill: the archive cache returned no data")
	}
	return gen.Value, nil
}

// loadSkills returns the cached skills, fetching the archive when stale.
func (b *archiveBackend) loadSkills(ctx context.Context) ([]archiveSkillEntry, error) {
	data, err := b.archiveData(ctx)
	if err != nil {
		return nil, err
	}
	return data.skills, nil
}

// loadFiles returns the cached file map, fetching the archive when stale.
func (b *archiveBackend) loadFiles(ctx context.Context) (map[string]string, error) {
	data, err := b.archiveData(ctx)
	if err != nil {
		return nil, err
	}
	return data.files, nil
}

// list returns every skill front matter found in the archive.
func (b *archiveBackend) list(ctx context.Context) ([]adkskill.FrontMatter, error) {
	skills, err := b.loadSkills(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]adkskill.FrontMatter, len(skills))
	for i, s := range skills {
		result[i] = s.frontMatter
	}
	return result, nil
}

// get returns one skill by name, persisting its files to disk first (when a
// skills directory is configured) and pointing BaseDirectory at the local copy
// so the agent reads the real files rather than a virtual path.
func (b *archiveBackend) get(ctx context.Context, name string) (adkskill.Skill, error) {
	skills, err := b.loadSkills(ctx)
	if err != nil {
		return adkskill.Skill{}, err
	}
	for _, s := range skills {
		if s.frontMatter.Name != name {
			continue
		}
		skill := adkskill.Skill{
			FrontMatter:   s.frontMatter,
			Content:       s.content,
			BaseDirectory: s.baseDir,
		}
		if b.skillsDir == "" {
			return skill, nil
		}
		// The archive loader already persists every skill it discovers, so this
		// only has work to do when that has not happened — a cache loaded
		// before a skills directory was configured, or a checkout that was
		// cleaned up underneath us. Rewriting the files on every retrieval
		// would also churn the mtimes of files an agent may be reading.
		if localDir := b.localSkillDir(s); dirExists(localDir) {
			skill.BaseDirectory = localDir
			return skill, nil
		}
		localDir, saveErr := b.saveSkillToDir(ctx, s)
		if saveErr != nil {
			// Serving the skill from the archive is still useful, so a failed
			// persist is reported and not fatal.
			slog.WarnContext(ctx, b.logPrefix+" failed to persist skill to disk", "skill", name, "error", saveErr)
		} else {
			skill.BaseDirectory = localDir
		}
		return skill, nil
	}
	return adkskill.Skill{}, fmt.Errorf("skill not found: %s", name)
}

// saveSkillToDir writes every file belonging to the skill's archive directory
// under skillsDir and returns the resulting local base directory:
//
//	<skillsDir>/<agentKey>/<repoKey>/<branch>/<repoRelativePath>/
func (b *archiveBackend) saveSkillToDir(ctx context.Context, entry archiveSkillEntry) (string, error) {
	files, err := b.loadFiles(ctx)
	if err != nil {
		return "", fmt.Errorf("load files: %w", err)
	}
	return b.saveSkillToDirWithFiles(entry, files)
}

// saveSkillToDirWithFiles is saveSkillToDir against an already-loaded file map,
// so a caller that just fetched the archive does not reload it per skill.
// localSkillDir is where a skill is checked out: the layout
// saveSkillToDirWithFiles writes and get reads back.
func (b *archiveBackend) localSkillDir(entry archiveSkillEntry) string {
	return filepath.Join(b.skillsDir, AgentDirSegment(b.agentKey), b.repoKey, b.branch, stripArchiveRoot(entry.baseDir))
}

// dirExists reports whether a directory is present.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (b *archiveBackend) saveSkillToDirWithFiles(entry archiveSkillEntry, files map[string]string) (string, error) {
	b.diskMu.Lock()
	defer b.diskMu.Unlock()

	relSkillDir := stripArchiveRoot(entry.baseDir)
	localBaseDir := b.localSkillDir(entry)

	for rawPath, content := range files {
		rel := stripArchiveRoot(rawPath)
		if rel != relSkillDir && !strings.HasPrefix(rel, relSkillDir+"/") {
			continue
		}
		fileRel := filepath.Base(rel)
		if rel != relSkillDir {
			fileRel = rel[len(relSkillDir)+1:]
		}
		// The archive is untrusted input. Entry names are filtered by the
		// prefix check above, but a name like "skills/foo/../../etc/cron.d/x"
		// passes that check and then escapes through the join — so the
		// containment check has to happen here, on the relative name, before
		// anything is written.
		if !isContainedRelPath(fileRel) {
			return "", fmt.Errorf("unsafe path in archive: %q", rawPath)
		}
		destPath := filepath.Join(localBaseDir, fileRel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}
		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", destPath, err)
		}
	}
	return localBaseDir, nil
}

// isContainedRelPath reports whether a relative path stays inside the
// directory it will be joined onto.
//
// Absolute paths and any ".." segment are refused outright rather than
// sanitised: a published package has no legitimate reason to contain one, and
// rewriting the path would hide the fact that someone tried.
func isContainedRelPath(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) {
		return false
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// stripArchiveRoot removes the leading "<repo>-<branch>/" prefix that GitHub
// and GitLab archives insert before every path.
func stripArchiveRoot(path string) string {
	if idx := strings.Index(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// isLocalAbsPath reports whether p is an absolute path that exists on disk.
// Such a path is served by the real filesystem; anything else is a virtual
// archive path served from the in-memory zip.
func isLocalAbsPath(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// Filesystem operations. Each routes to the local backend for a path that
// exists on disk, and to the cached archive otherwise. Write, Edit and Execute
// only ever touch disk: a virtual archive path is read-only by construction.

func (b *archiveBackend) lsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	if isLocalAbsPath(req.Path) {
		return b.local.LsInfo(ctx, req)
	}
	files, err := b.loadFiles(ctx)
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(req.Path, "/")
	seen := make(map[string]bool)
	var result []filesystem.FileInfo
	for rawPath := range files {
		rel := stripArchiveRoot(rawPath)
		if base != "" && base != "." && base != "/" {
			if !strings.HasPrefix(rel, base+"/") {
				continue
			}
			rel = rel[len(base)+1:]
		}
		parts := strings.SplitN(rel, "/", 2)
		if name := parts[0]; name != "" && !seen[name] {
			seen[name] = true
			result = append(result, filesystem.FileInfo{Path: name, IsDir: len(parts) > 1})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (b *archiveBackend) read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	if isLocalAbsPath(req.FilePath) {
		return b.local.Read(ctx, req)
	}
	files, err := b.loadFiles(ctx)
	if err != nil {
		return nil, err
	}
	target := strings.TrimPrefix(req.FilePath, "/")
	for rawPath, content := range files {
		if stripArchiveRoot(rawPath) == target {
			return sliceLines(content, req.Offset, req.Limit), nil
		}
	}
	return nil, fmt.Errorf("file not found: %s", req.FilePath)
}

func (b *archiveBackend) grepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if isLocalAbsPath(req.Path) {
		return b.local.GrepRaw(ctx, req)
	}
	files, err := b.loadFiles(ctx)
	if err != nil {
		return nil, err
	}

	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", req.Pattern, err)
	}

	base := strings.TrimSuffix(req.Path, "/")
	var matches []filesystem.GrepMatch
	for rawPath, content := range files {
		rel := stripArchiveRoot(rawPath)
		if base != "" && base != "/" && base != "." && !strings.HasPrefix(rel, base+"/") && rel != base {
			continue
		}
		if req.Glob != "" {
			if matched, _ := filepath.Match(req.Glob, filepath.Base(rel)); !matched {
				continue
			}
		}
		for i, line := range strings.Split(content, "\n") {
			if re.MatchString(line) {
				matches = append(matches, filesystem.GrepMatch{Path: rel, Line: i + 1, Content: line})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, nil
}

func (b *archiveBackend) globInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	if isLocalAbsPath(req.Path) {
		return b.local.GlobInfo(ctx, req)
	}
	files, err := b.loadFiles(ctx)
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(req.Path, "/")
	var result []filesystem.FileInfo
	for rawPath := range files {
		rel := stripArchiveRoot(rawPath)
		if base != "" && base != "/" && base != "." {
			if !strings.HasPrefix(rel, base+"/") {
				continue
			}
			rel = rel[len(base)+1:]
		}
		matched, _ := filepath.Match(req.Pattern, rel)
		if !matched {
			matched, _ = filepath.Match(req.Pattern, filepath.Base(rel))
		}
		if matched {
			result = append(result, filesystem.FileInfo{Path: rel})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (b *archiveBackend) write(ctx context.Context, req *filesystem.WriteRequest) error {
	return b.local.Write(ctx, req)
}

func (b *archiveBackend) edit(ctx context.Context, req *filesystem.EditRequest) error {
	return b.local.Edit(ctx, req)
}

func (b *archiveBackend) execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return b.local.Execute(ctx, req)
}

func (b *archiveBackend) executeStreaming(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return b.local.ExecuteStreaming(ctx, req)
}

// sliceLines returns lines [offset, offset+limit) of content, 1-based.
func sliceLines(content string, offset, limit int) *filesystem.FileContent {
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	lines := strings.Split(content, "\n")
	start := offset - 1
	if start >= len(lines) {
		return &filesystem.FileContent{Content: ""}
	}
	end := min(start+limit, len(lines))
	return &filesystem.FileContent{Content: strings.Join(lines[start:end], "\n")}
}

// parseFrontmatter splits a markdown file into its YAML frontmatter and body.
func parseFrontmatter(data string) (frontmatter, content string, err error) {
	const delimiter = "---"

	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, delimiter) {
		return "", "", fmt.Errorf("file does not start with frontmatter delimiter %q", delimiter)
	}
	rest := data[len(delimiter):]
	endIdx := strings.Index(rest, "\n"+delimiter)
	if endIdx == -1 {
		return "", "", fmt.Errorf("frontmatter closing delimiter %q not found", delimiter)
	}
	frontmatter = strings.TrimSpace(rest[:endIdx])
	content = strings.TrimPrefix(rest[endIdx+len("\n"+delimiter):], "\n")
	return frontmatter, content, nil
}
