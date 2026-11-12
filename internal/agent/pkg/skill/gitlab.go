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
	"net/http"
	"net/url"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexstack/pkg/astore"

	"github.com/onexstack/onexai/internal/agent/model"
	skillclient "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/skill"
)

func init() {
	Register(ProviderSpec{
		Name: ProviderGitLab,
		Build: func(ctx context.Context, s *model.SkillM, spec BuildSpec) (FullBackend, error) {
			var params model.GitLabParams
			if err := decodeParams(s.Params, &params); err != nil {
				return nil, err
			}
			if params.ProjectPath == "" {
				return nil, fmt.Errorf("gitlab: project_path is required")
			}
			token, err := resolveToken(ctx, spec.Deps.Secrets, params.TokenSecretID, params.Token)
			if err != nil {
				return nil, err
			}
			return NewGitLabBackend(spec.SkillsDir, spec.AgentKey, GitLabConfig{
				BaseURL:     params.BaseURL,
				ProjectPath: params.ProjectPath,
				Branch:      params.Branch,
				SubDir:      params.SubDir,
				Token:       token,
				CacheTTL:    spec.CacheTTL,
			})
		},
	})
}

// GitLabConfig configures a GitLab-backed skill repository.
type GitLabConfig struct {
	// BaseURL is the instance address; defaults to https://gitlab.com.
	BaseURL string
	// ProjectPath is the namespace/project path, e.g. "group/project".
	ProjectPath string
	// Branch defaults to "master".
	Branch string
	// SubDir limits discovery to one directory of the repository.
	SubDir string
	// Token authenticates against a private project. Optional for public ones.
	Token string
	// CacheTTL is how long a downloaded archive is served before re-fetching.
	CacheTTL time.Duration
	// HTTPClient overrides the default client (tests, proxies).
	HTTPClient *http.Client
}

// gitlabBackend loads skills from a downloaded GitLab repository archive.
type gitlabBackend struct {
	*archiveBackend
	cfg    GitLabConfig
	client *gitlab.Client
}

// NewGitLabBackend builds a backend for one GitLab project.
//
// The archive is fetched through the official GitLab client, with its own retry
// policy disabled: this package caches aggressively and a retrying transport
// underneath a cache turns one slow repository into a queue of identical
// requests.
func NewGitLabBackend(skillsDir, agentKey string, cfg GitLabConfig) (FullBackend, error) {
	if cfg.ProjectPath == "" {
		return nil, fmt.Errorf("gitlab: project_path is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://gitlab.com"
	}
	if cfg.Branch == "" {
		cfg.Branch = "master"
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}

	var transport *skillclient.Client
	if cfg.HTTPClient != nil {
		transport = skillclient.NewWithHTTPClient(cfg.HTTPClient)
	} else {
		// The SDK's own default client has no timeout.
		transport = skillclient.New(archiveDownloadTimeout)
	}
	opts := []gitlab.ClientOptionFunc{
		gitlab.WithBaseURL(strings.TrimRight(cfg.BaseURL, "/")),
		gitlab.WithoutRetries(),
		gitlab.WithHTTPClient(transport.HTTPClient()),
	}
	// An empty token is accepted: GitLab serves public projects anonymously.
	client, err := gitlab.NewClient(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: create client: %w", err)
	}

	localFS, err := localbk.NewBackend(context.Background(), &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("gitlab: local backend: %w", err)
	}

	b := &gitlabBackend{
		archiveBackend: &archiveBackend{
			local:     localFS,
			repoKey:   gitlabRepoKey(cfg.BaseURL, cfg.ProjectPath),
			branch:    cfg.Branch,
			skillsDir: skillsDir,
			agentKey:  agentKey,
			logPrefix: "[skill/gitlab]",
		},
		cfg:    cfg,
		client: client,
	}
	b.cache = astore.New(astore.Options{Name: "skill.gitlab", TTL: cfg.CacheTTL}, func(ctx context.Context) (*archiveCacheData, error) {
		skills, files, err := b.fetch(ctx)
		if err != nil {
			return nil, err
		}
		if b.skillsDir != "" {
			for _, entry := range skills {
				if _, saveErr := b.saveSkillToDirWithFiles(entry, files); saveErr != nil {
					slog.WarnContext(ctx, b.logPrefix+" failed to persist skill", "skill", entry.frontMatter.Name, "error", saveErr)
				}
			}
		}
		return &archiveCacheData{skills: skills, files: files}, nil
	})
	return b, nil
}

// List returns every skill the project declares.
func (b *gitlabBackend) List(ctx context.Context) ([]adkskill.FrontMatter, error) {
	return b.list(ctx)
}

// Get returns one skill, persisted to disk when a skills directory is set.
func (b *gitlabBackend) Get(ctx context.Context, name string) (adkskill.Skill, error) {
	return b.get(ctx, name)
}

// fetch downloads and unpacks the project archive.
func (b *gitlabBackend) fetch(ctx context.Context) ([]archiveSkillEntry, map[string]string, error) {
	format := "zip"
	sha := b.cfg.Branch

	// GitLab archives share the "<root>/…/SKILL.md" layout and frontmatter
	// format with GitHub's, so extraction is shared with that provider.
	data, resp, err := b.client.Repositories.Archive(b.cfg.ProjectPath,
		&gitlab.ArchiveOptions{Format: &format, SHA: &sha},
		gitlab.WithContext(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: fetch archive of %s@%s: %w", b.cfg.ProjectPath, b.cfg.Branch, err)
	}
	if resp != nil && len(data) > maxArchiveBytes {
		return nil, nil, fmt.Errorf("gitlab: archive of %s exceeds the %d byte limit", b.cfg.ProjectPath, maxArchiveBytes)
	}
	return extractSkillsFromZip(data, b.cfg.SubDir)
}

// gitlabRepoKey builds the local directory segment for a project, e.g.
// ("https://gitlab.com", "group/project") → "gitlab.com/group/project".
func gitlabRepoKey(baseURL, projectPath string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return projectPath
	}
	return u.Host + "/" + strings.TrimPrefix(projectPath, "/")
}

// Filesystem and shell operations delegate to the shared archive backend.

func (b *gitlabBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	return b.lsInfo(ctx, req)
}

func (b *gitlabBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	return b.read(ctx, req)
}

func (b *gitlabBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return b.grepRaw(ctx, req)
}

func (b *gitlabBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	return b.globInfo(ctx, req)
}

func (b *gitlabBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	return b.write(ctx, req)
}

func (b *gitlabBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	return b.edit(ctx, req)
}

func (b *gitlabBackend) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return b.execute(ctx, req)
}

func (b *gitlabBackend) ExecuteStreaming(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return b.executeStreaming(ctx, req)
}
