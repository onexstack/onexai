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
		Name: ProviderGitHub,
		Build: func(_ context.Context, s *model.SkillM, spec BuildSpec) (FullBackend, error) {
			var params model.GitHubParams
			if err := decodeParams(s.Params, &params); err != nil {
				return nil, err
			}
			if params.RepoURL == "" {
				return nil, fmt.Errorf("github: repo_url is required")
			}
			return NewGitHubBackend(spec.SkillsDir, spec.AgentKey, GitHubConfig{
				RepoURL:  params.RepoURL,
				Branch:   params.Branch,
				SubDir:   params.SubDir,
				CacheTTL: spec.CacheTTL,
			})
		},
	})
}

// GitHubConfig configures a GitHub-backed skill repository.
type GitHubConfig struct {
	// RepoURL is the repository address, e.g. "https://github.com/owner/repo".
	RepoURL string
	// Branch defaults to "master".
	Branch string
	// SubDir, when set, limits discovery to one directory of the repository.
	SubDir string
	// Token authenticates against a private repository. Left empty for public
	// ones, so the credential never reaches the codeload CDN.
	Token string
	// CacheTTL is how long a downloaded archive is served before re-fetching.
	CacheTTL time.Duration
	// HTTPClient overrides the default client (tests, proxies).
	HTTPClient *http.Client
}

// githubBackend loads skills from a downloaded GitHub repository archive.
type githubBackend struct {
	*archiveBackend
	cfg        GitHubConfig
	httpClient *skillclient.Client
	owner      string
	repo       string
}

// NewGitHubBackend builds a backend for one GitHub repository.
//
// The archive is fetched from the plain
// https://github.com/<owner>/<repo>/archive/refs/heads/<branch>.zip URL rather
// than through the API: it consumes no API rate limit, involves no pre-signed
// redirect chain, and never forwards an Authorization header to the codeload
// host (which is what makes an authenticated API-based download fail).
func NewGitHubBackend(skillsDir, agentKey string, cfg GitHubConfig) (FullBackend, error) {
	if cfg.RepoURL == "" {
		return nil, fmt.Errorf("github: repo_url is required")
	}
	owner, repo, err := parseGitHubRepoURL(cfg.RepoURL)
	if err != nil {
		return nil, err
	}
	if cfg.Branch == "" {
		cfg.Branch = "master"
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	var client *skillclient.Client
	if cfg.HTTPClient != nil {
		client = skillclient.NewWithHTTPClient(cfg.HTTPClient)
	} else {
		// Not http.DefaultClient: it has no timeout, so a repository that
		// accepts a connection and then stalls would hold the build forever.
		client = skillclient.New(archiveDownloadTimeout)
	}

	localFS, err := localbk.NewBackend(context.Background(), &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("github: local backend: %w", err)
	}

	b := &githubBackend{
		archiveBackend: &archiveBackend{
			local:     localFS,
			repoKey:   repoURLToKey(cfg.RepoURL),
			branch:    cfg.Branch,
			skillsDir: skillsDir,
			agentKey:  agentKey,
			logPrefix: "[skill/github]",
		},
		cfg:        cfg,
		httpClient: client,
		owner:      owner,
		repo:       repo,
	}
	b.cache = astore.New(astore.Options{Name: "skill.github", TTL: cfg.CacheTTL}, func(ctx context.Context) (*archiveCacheData, error) {
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

// List returns every skill the repository declares.
func (b *githubBackend) List(ctx context.Context) ([]adkskill.FrontMatter, error) {
	return b.list(ctx)
}

// Get returns one skill, persisted to disk when a skills directory is set.
func (b *githubBackend) Get(ctx context.Context, name string) (adkskill.Skill, error) {
	return b.get(ctx, name)
}

// fetch downloads and unpacks the repository archive.
func (b *githubBackend) fetch(ctx context.Context) ([]archiveSkillEntry, map[string]string, error) {
	archiveURL := fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.zip",
		b.owner, b.repo, b.cfg.Branch)
	data, err := b.download(ctx, archiveURL)
	if err != nil {
		return nil, nil, fmt.Errorf("github: download %s: %w", archiveURL, err)
	}
	return extractSkillsFromZip(data, b.cfg.SubDir)
}

// download fetches an archive, adding the bearer token only when one is
// configured.
func (b *githubBackend) download(ctx context.Context, rawURL string) ([]byte, error) {
	headers := map[string]string{}
	if b.cfg.Token != "" {
		headers["Authorization"] = "Bearer " + b.cfg.Token
	}
	status, data, err := b.httpClient.Get(ctx, rawURL, headers, maxArchiveBytes)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d", status)
	}
	return data, nil
}

// archiveDownloadTimeout bounds one archive transfer.
const archiveDownloadTimeout = 2 * time.Minute

// maxArchiveBytes caps what is read off the network and what is held in memory
// after decompression. It is generous for a repository of skills and small
// enough that a hostile or misconfigured endpoint cannot exhaust the process.
const maxArchiveBytes = 64 << 20

// parseGitHubRepoURL extracts the owner and repository from a GitHub URL,
// tolerating a trailing ".git".
func parseGitHubRepoURL(repoURL string) (owner, repo string, err error) {
	u, err := url.Parse(strings.TrimSuffix(repoURL, ".git"))
	if err != nil {
		return "", "", fmt.Errorf("github: parse repo URL %q: %w", repoURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github: expected https://github.com/<owner>/<repo>, got %q", repoURL)
	}
	return parts[0], parts[1], nil
}

// repoURLToKey turns a repository URL into the local directory segment it is
// checked out under, e.g. "github.com/owner/repo".
func repoURLToKey(rawURL string) string {
	u, err := url.Parse(strings.TrimSuffix(rawURL, ".git"))
	if err != nil {
		return rawURL
	}
	return u.Host + strings.TrimSuffix(u.Path, ".git")
}

// Filesystem and shell operations delegate to the shared archive backend.

func (b *githubBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	return b.lsInfo(ctx, req)
}

func (b *githubBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	return b.read(ctx, req)
}

func (b *githubBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return b.grepRaw(ctx, req)
}

func (b *githubBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	return b.globInfo(ctx, req)
}

func (b *githubBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	return b.write(ctx, req)
}

func (b *githubBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	return b.edit(ctx, req)
}

func (b *githubBackend) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return b.execute(ctx, req)
}

func (b *githubBackend) ExecuteStreaming(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return b.executeStreaming(ctx, req)
}
