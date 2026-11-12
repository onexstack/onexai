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
	"fmt"
	"io"
	"path/filepath"
	"strings"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"gopkg.in/yaml.v3"
)

// extractSkillsFromZip parses a repository zip archive and returns the skills
// it contains, together with every file's content keyed by its raw archive path.
//
// The full file map is kept because a skill's filesystem surface is not only
// SKILL.md: a skill typically ships a references/ directory the agent must be
// able to read, and serving those from the in-memory archive is what makes a
// not-yet-persisted skill usable.
//
// Both GitHub and GitLab archives wrap everything in a single
// "<repo>-<branch>/" directory; that prefix is stripped on use, not here, so
// the stored paths stay faithful to the archive.
func extractSkillsFromZip(data []byte, subDir string) ([]archiveSkillEntry, map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("open zip archive: %w", err)
	}

	files := readZipIntoMapFrom(zr)
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("zip archive contains no readable files")
	}

	var skills []archiveSkillEntry
	for path, content := range files {
		if !strings.HasSuffix(path, "/"+skillFileName) && path != skillFileName {
			continue
		}
		if subDir != "" && !underSubDir(path, subDir) {
			continue
		}
		entry, err := parseSkillMD(content, filepath.Dir(path))
		if err != nil {
			continue // a malformed SKILL.md is skipped, not fatal
		}
		skills = append(skills, entry)
	}
	return skills, files, nil
}

// readZipIntoMapFrom decompresses every file of an already-opened archive into
// a path → content map. One unreadable entry is skipped rather than failing the
// whole download: a repository is not worth discarding over a single file the
// server stored oddly.
func readZipIntoMapFrom(zr *zip.Reader) map[string]string {
	files := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		content, err := readZipFile(f)
		if err != nil {
			continue
		}
		files[f.Name] = content
	}
	return files
}

// readZipFile reads one archive entry into a string.
func readZipFile(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// underSubDir reports whether an archive path lives under subDir once the
// archive's root prefix has been removed.
func underSubDir(path, subDir string) bool {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		return false
	}
	rel := parts[1]
	return strings.HasPrefix(rel, subDir+"/") || rel == subDir+"/"+skillFileName
}

// parseSkillMD parses one SKILL.md into an archive entry, requiring a name in
// the frontmatter because that name is how the model asks for the skill.
func parseSkillMD(content, baseDir string) (archiveSkillEntry, error) {
	frontmatter, body, err := parseFrontmatter(content)
	if err != nil {
		return archiveSkillEntry{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	var fm adkskill.FrontMatter
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return archiveSkillEntry{}, fmt.Errorf("unmarshal frontmatter YAML: %w", err)
	}
	if fm.Name == "" {
		return archiveSkillEntry{}, fmt.Errorf("skill is missing 'name' in its frontmatter")
	}
	return archiveSkillEntry{
		frontMatter: fm,
		content:     strings.TrimSpace(body),
		baseDir:     baseDir,
	}, nil
}
