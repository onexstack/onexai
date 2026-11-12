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

// Package sandbox runs owner-scoped command/file environments in one-shot
// containers. The container is the isolation boundary: no network by default,
// read-only root, writable tmpfs workspace, non-root user, dropped capabilities.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/sandboxstore"
)

// Defaults.
const (
	DefaultTTL     = 30 * time.Minute
	DefaultMaxTTL  = 4 * time.Hour
	DefaultExec    = 60 * time.Second
	MaxExec        = 10 * time.Minute
	MaxOutputBytes = 64 << 10
	workspace      = "/workspace"
)

// ErrNotFound reports a sandbox that does not exist or belongs to another owner.
var ErrNotFound = errors.New("sandbox not found")

// Config is the deployment's sandbox policy.
type Config struct {
	// Images maps a template name to a digest-pinned image.
	Images map[string]string
	// DefaultImage is used when no template is named.
	DefaultImage string
	// DefaultTTL and MaxTTL bound a sandbox's life.
	DefaultTTL time.Duration
	MaxTTL     time.Duration
	// CPUs and Memory are the container limits (e.g. "0.5", "512m").
	CPUs      string
	Memory    string
	PidsLimit int
	// Network is the Docker network the sandbox joins. Empty means "none", which
	// is the isolation this package promises: a sandbox with no route out cannot
	// exfiltrate what it reads.
	//
	// A deployment that needs egress points this at a network whose only
	// reachable endpoint is an allowlisting proxy, and sets EgressProxy so the
	// commands inside use it. Two settings rather than one because neither is
	// sufficient alone: a proxy the container cannot reach does nothing, and a
	// network with a route out is not an allow-list.
	Network string
	// EgressProxy is exported into the container as HTTP_PROXY/HTTPS_PROXY (and
	// the lowercase spellings, which is what most tools read). Empty means the
	// sandbox is told nothing about a proxy.
	EgressProxy string
	// NoProxy lists the hosts that bypass the proxy, exported as NO_PROXY.
	NoProxy    string
	Command    []string
	Executable string
}

// Validate refuses a configuration that would quietly undo the isolation.
//
// It is a check rather than a filter because a deployment that asks for host
// networking has said what it wants; silently substituting "none" would leave an
// operator believing a policy is in force that is not.
func (c Config) Validate() error {
	switch {
	case c.Network == "host":
		return fmt.Errorf("sandbox: the host network would share this machine's interfaces with the sandbox")
	case strings.HasPrefix(c.Network, "container:"):
		return fmt.Errorf("sandbox: sharing another container's network namespace defeats the isolation")
	case strings.HasPrefix(c.Network, "service:"), strings.HasPrefix(c.Network, "pod:"),
		strings.HasPrefix(c.Network, "ns:"):
		return fmt.Errorf("sandbox: network mode %q joins another workload's namespace", c.Network)
	}
	if c.EgressProxy != "" {
		if _, err := url.Parse(c.EgressProxy); err != nil {
			return fmt.Errorf("sandbox: egress proxy %q is not a URL: %w", c.EgressProxy, err)
		}
	}
	return nil
}

// ExecResult is one command's outcome.
type ExecResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exitCode"`
	Truncated bool   `json:"truncated"`
}

// Template is what a declarative template contributes to one container.
//
// It is resolved per sandbox rather than held in Config because a template can
// change under a running deployment: the image a new sandbox gets is whatever
// the template says now, not whatever it said at start-up.
type Template struct {
	Image   string
	Command []string
	// Env is KEY=VALUE pairs.
	Env []string
	// TTL is the template's default lifetime; zero means the deployment's.
	TTL time.Duration
}

// TemplateSource resolves a declared template by name. A nil source means this
// deployment configures images statically, which is what it did before
// templates existed.
type TemplateSource interface {
	Template(ctx context.Context, name string) (*Template, error)
}

// Service is the sandbox subsystem.
type Service struct {
	Store  sandboxstore.Store
	Config Config
	// Templates resolves declarative templates. Nil falls back to Config.Images.
	Templates TemplateSource
	Now       func() time.Time
}

// Create starts a sandbox for owner.
func (s *Service) Create(ctx context.Context, owner, template string, ttl time.Duration) (*model.SandboxM, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("sandbox is not configured")
	}
	resolved, err := s.templateFor(ctx, template)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = resolved.ttl()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	max := s.Config.MaxTTL
	if max <= 0 {
		max = DefaultMaxTTL
	}
	if ttl > max {
		return nil, fmt.Errorf("ttl exceeds the maximum %s", max)
	}
	id := uuid.NewString()
	name := containerName(id)
	args := s.runArgs(name, resolved)
	output, err := exec.CommandContext(ctx, s.executable(), args...).CombinedOutput() //nolint:gosec // configured
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker run: %w: %s", err, strings.TrimSpace(string(output)))
	}
	containerID := strings.TrimSpace(string(output))
	now := s.now()
	sandbox := &model.SandboxM{
		ID:          id,
		Owner:       owner,
		Template:    template,
		Image:       resolved.Image,
		ContainerID: containerID,
		State:       model.SandboxStateRunning,
		ExpiresAt:   now.Add(ttl).UnixMilli(),
		CreatedAt:   now.UnixMilli(),
	}
	if err := s.Store.Create(ctx, sandbox); err != nil {
		_ = exec.Command(s.executable(), "rm", "-f", name).Run() //nolint:gosec // best effort rollback
		return nil, err
	}
	return sandbox, nil
}

// Get returns a sandbox owned by owner.
func (s *Service) Get(ctx context.Context, id, owner string) (*model.SandboxM, error) {
	sandbox, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if sandbox.Owner != owner {
		return nil, ErrNotFound
	}
	return sandbox, nil
}

// List returns the owner's sandboxes.
func (s *Service) List(ctx context.Context, owner string, limit int) ([]*model.SandboxM, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("sandbox is not configured")
	}
	return s.Store.List(ctx, owner, limit)
}

// Delete stops and removes a sandbox.
func (s *Service) Delete(ctx context.Context, id, owner string) error {
	sandbox, err := s.Get(ctx, id, owner)
	if err != nil {
		return err
	}
	_ = exec.Command(s.executable(), "rm", "-f", sandbox.ContainerID).Run() //nolint:gosec // best effort
	sandbox.State = model.SandboxStateStopped
	return s.Store.Update(ctx, sandbox)
}

// Exec runs one command inside the sandbox.
func (s *Service) Exec(
	ctx context.Context, id, owner, command string, timeout time.Duration,
) (*ExecResult, error) {
	sandbox, err := s.Get(ctx, id, owner)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is empty")
	}
	if timeout <= 0 {
		timeout = DefaultExec
	}
	if timeout > MaxExec {
		timeout = MaxExec
	}
	seconds := int(timeout.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	script := "timeout -s KILL " + strconv.Itoa(seconds) + " sh -lc " + shellQuote(command)
	cmd := exec.CommandContext(ctx, s.executable(), "exec", sandbox.ContainerID, "sh", "-lc", script) //nolint:gosec // user command by design
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("sandbox exec: %w", runErr)
		}
	}
	return &ExecResult{Output: output.String(), ExitCode: exitCode, Truncated: output.truncated}, nil
}

// ReadFile returns a text file from the workspace.
func (s *Service) ReadFile(ctx context.Context, id, owner, filePath string, maxBytes int) (string, error) {
	sandbox, err := s.Get(ctx, id, owner)
	if err != nil {
		return "", err
	}
	clean, err := workspacePath(filePath)
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 || maxBytes > MaxOutputBytes {
		maxBytes = MaxOutputBytes
	}
	cmd := exec.CommandContext(ctx, s.executable(), "exec", sandbox.ContainerID, "sh", "-lc",
		"head -c "+strconv.Itoa(maxBytes)+" -- "+shellQuote(clean)) //nolint:gosec // validated path
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sandbox read: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// WriteFile writes text into the workspace.
func (s *Service) WriteFile(ctx context.Context, id, owner, filePath, content string) error {
	sandbox, err := s.Get(ctx, id, owner)
	if err != nil {
		return err
	}
	clean, err := workspacePath(filePath)
	if err != nil {
		return err
	}
	if len(content) > MaxOutputBytes {
		return fmt.Errorf("content exceeds %d bytes", MaxOutputBytes)
	}
	cmd := exec.CommandContext(ctx, s.executable(), "exec", "-i", sandbox.ContainerID, "sh", "-lc",
		"cat > "+shellQuote(clean)) //nolint:gosec // validated path
	cmd.Stdin = strings.NewReader(content)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox write: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// RunReaper removes expired sandboxes until ctx is done.
func (s *Service) RunReaper(ctx context.Context) error {
	if s == nil || s.Store == nil {
		return nil
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := s.Reap(ctx); err != nil {
				slog.WarnContext(ctx, "sandbox reaper failed", "error", err)
			}
		}
	}
}

// Reap removes expired sandboxes.
func (s *Service) Reap(ctx context.Context) (int, error) {
	if s.Store == nil {
		return 0, nil
	}
	expired, err := s.Store.ListExpired(ctx, s.now().UnixMilli(), 100)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, sandbox := range expired {
		_ = exec.Command(s.executable(), "rm", "-f", sandbox.ContainerID).Run() //nolint:gosec // best effort
		sandbox.State = model.SandboxStateExpired
		if err := s.Store.Update(ctx, sandbox); err != nil {
			continue
		}
		removed++
	}
	return removed, nil
}

// templateFor resolves what a sandbox is made of.
//
// The declarative template wins; the deployment's image map is the fallback, so
// a deployment that has not adopted templates keeps working unchanged, and one
// that has can retire an entry from its config without a code change.
func (s *Service) templateFor(ctx context.Context, name string) (Template, error) {
	if s.Templates != nil {
		if resolved, err := s.Templates.Template(ctx, name); err != nil {
			return Template{}, err
		} else if resolved != nil && resolved.Image != "" {
			return *resolved, nil
		}
	}
	if name == "" {
		name = "default"
	}
	if image := s.Config.Images[name]; image != "" {
		return Template{Image: image}, nil
	}
	if s.Config.DefaultImage != "" {
		return Template{Image: s.Config.DefaultImage}, nil
	}
	return Template{}, fmt.Errorf("sandbox template %q is not configured", name)
}

// ttl is the template's default lifetime, falling back to the deployment's.
func (t Template) ttl() time.Duration { return t.TTL }

func (s *Service) runArgs(name string, template Template) []string {
	image := template.Image
	network := s.Config.Network
	if network == "" {
		network = "none"
	}
	args := []string{
		"run", "-d", "--rm", "--name", name,
		"--network", network,
		"--read-only",
		// mode=1777 is not decoration: the container runs as an unprivileged
		// user, and a tmpfs mounts root-owned 0755 by default — the only
		// writable place in the sandbox would then be unwritable, which is what
		// "can't create /workspace/notes.txt: Permission denied" meant.
		"--tmpfs", "/tmp:rw,size=64m,mode=1777",
		"--tmpfs", workspace + ":rw,size=256m,mode=1777",
		"--user", "65532:65532",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--workdir", workspace,
	}
	// Both spellings: curl and Go read the uppercase ones, most language
	// runtimes read the lowercase ones, and a proxy that half the tools ignore
	// is worse than none — the failure looks like a flaky network.
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if s.Config.EgressProxy != "" {
			args = append(args, "--env", name+"="+s.Config.EgressProxy)
		}
	}
	if s.Config.NoProxy != "" {
		args = append(args, "--env", "NO_PROXY="+s.Config.NoProxy, "--env", "no_proxy="+s.Config.NoProxy)
	}
	if s.Config.CPUs != "" {
		args = append(args, "--cpus", s.Config.CPUs)
	}
	if s.Config.Memory != "" {
		args = append(args, "-m", s.Config.Memory)
	}
	pids := s.Config.PidsLimit
	if pids <= 0 {
		pids = 256
	}
	args = append(args, "--pids-limit", strconv.Itoa(pids))
	// Template environment is literal by construction: a template cannot name a
	// Secret, because the sandbox is given no credential to resolve one with.
	for _, entry := range template.Env {
		if entry == "" {
			continue
		}
		args = append(args, "--env", entry)
	}
	args = append(args, image)
	if len(template.Command) > 0 {
		return append(args, template.Command...)
	}
	if len(s.Config.Command) > 0 {
		return append(args, s.Config.Command...)
	}
	return append(args, "sleep", "infinity")
}

func (s *Service) executable() string {
	if s.Config.Executable != "" {
		return s.Config.Executable
	}
	return "docker"
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func containerName(id string) string {
	return "onexai-sbx-" + strings.ReplaceAll(id, "-", "")[:12]
}

func workspacePath(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("path is empty")
	}
	if !path.IsAbs(filePath) {
		filePath = path.Join(workspace, filePath)
	}
	clean := path.Clean(filePath)
	if clean != workspace && !strings.HasPrefix(clean, workspace+"/") &&
		!strings.HasPrefix(clean, "/tmp/") && clean != "/tmp" {
		return "", fmt.Errorf("path %q is outside the sandbox workspace", filePath)
	}
	return clean, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type cappedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len() >= MaxOutputBytes {
		b.truncated = true
		return len(p), nil
	}
	remaining := MaxOutputBytes - b.Len()
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	return b.Buffer.Write(p)
}

func (b *cappedBuffer) String() string {
	out := b.Buffer.String()
	if b.truncated {
		out += "\n[output truncated]"
	}
	return out
}
