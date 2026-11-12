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

	"github.com/cloudwego/eino/adk/filesystem"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/schema"
)

// FullBackend is what every backend in this package returns: the skill
// middleware's view of it, plus the filesystem and shell views the deep agent
// needs. Naming it lets a constructor return one value usable in all four
// places instead of forcing a type assertion at each call site.
type FullBackend interface {
	adkskill.Backend
	filesystem.Backend
	filesystem.Shell
	filesystem.StreamingShell
}

// CombinedBackend merges the skills of several backends. Earlier backends win a
// name collision, so the order the caller resolved the skills in decides the
// outcome and stays stable across runs.
type CombinedBackend struct {
	backends []adkskill.Backend
}

// NewCombinedBackend aggregates the given backends, dropping nils.
func NewCombinedBackend(backends ...adkskill.Backend) FullBackend {
	kept := make([]adkskill.Backend, 0, len(backends))
	for _, b := range backends {
		if b != nil {
			kept = append(kept, b)
		}
	}
	return &CombinedBackend{backends: kept}
}

// List returns the union of the backends' skills, first occurrence winning.
// A backend that fails contributes nothing rather than failing the whole list:
// one broken repository must not hide every other skill.
func (c *CombinedBackend) List(ctx context.Context) ([]adkskill.FrontMatter, error) {
	seen := make(map[string]bool)
	var result []adkskill.FrontMatter
	for _, b := range c.backends {
		matters, err := b.List(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "[skill/combined] backend list failed", "error", err)
			continue
		}
		for _, fm := range matters {
			if seen[fm.Name] {
				continue
			}
			seen[fm.Name] = true
			result = append(result, fm)
		}
	}
	return result, nil
}

// Get returns the first backend's skill with the given name.
func (c *CombinedBackend) Get(ctx context.Context, name string) (adkskill.Skill, error) {
	for _, b := range c.backends {
		if skill, err := b.Get(ctx, name); err == nil {
			return skill, nil
		}
	}
	return adkskill.Skill{}, fmt.Errorf("skill not found in any backend: %s", name)
}

// Filesystem and shell operations delegate to the first backend that supports
// them. A combined set of pure-archive backends has none, which is why each
// method reports the absence rather than panicking.

func (c *CombinedBackend) firstFS() filesystem.Backend {
	for _, b := range c.backends {
		if fb, ok := b.(filesystem.Backend); ok {
			return fb
		}
	}
	return nil
}

func (c *CombinedBackend) firstShell() filesystem.Shell {
	for _, b := range c.backends {
		if sb, ok := b.(filesystem.Shell); ok {
			return sb
		}
	}
	return nil
}

func (c *CombinedBackend) firstStreamingShell() filesystem.StreamingShell {
	for _, b := range c.backends {
		if ssb, ok := b.(filesystem.StreamingShell); ok {
			return ssb
		}
	}
	return nil
}

// LsInfo delegates to the first filesystem-capable backend.
func (c *CombinedBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	if fb := c.firstFS(); fb != nil {
		return fb.LsInfo(ctx, req)
	}
	return nil, errNoFilesystemBackend
}

// Read delegates to the first filesystem-capable backend.
func (c *CombinedBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	if fb := c.firstFS(); fb != nil {
		return fb.Read(ctx, req)
	}
	return nil, errNoFilesystemBackend
}

// GrepRaw delegates to the first filesystem-capable backend.
func (c *CombinedBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	if fb := c.firstFS(); fb != nil {
		return fb.GrepRaw(ctx, req)
	}
	return nil, errNoFilesystemBackend
}

// GlobInfo delegates to the first filesystem-capable backend.
func (c *CombinedBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	if fb := c.firstFS(); fb != nil {
		return fb.GlobInfo(ctx, req)
	}
	return nil, errNoFilesystemBackend
}

// Write delegates to the first filesystem-capable backend.
func (c *CombinedBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	if fb := c.firstFS(); fb != nil {
		return fb.Write(ctx, req)
	}
	return errNoFilesystemBackend
}

// Edit delegates to the first filesystem-capable backend.
func (c *CombinedBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	if fb := c.firstFS(); fb != nil {
		return fb.Edit(ctx, req)
	}
	return errNoFilesystemBackend
}

// Execute delegates to the first shell-capable backend.
func (c *CombinedBackend) Execute(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*filesystem.ExecuteResponse, error) {
	if sb := c.firstShell(); sb != nil {
		return sb.Execute(ctx, req)
	}
	return nil, errNoShellBackend
}

// ExecuteStreaming delegates to the first shell-capable backend.
func (c *CombinedBackend) ExecuteStreaming(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	if ssb := c.firstStreamingShell(); ssb != nil {
		return ssb.ExecuteStreaming(ctx, req)
	}
	return nil, errNoShellBackend
}

var (
	errNoFilesystemBackend = fmt.Errorf("no filesystem backend available")
	errNoShellBackend      = fmt.Errorf("no shell backend available")
)
