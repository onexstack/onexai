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

package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/sandbox"
)

// SandboxService is the container execution subsystem as the gateway needs it.
type SandboxService interface {
	Create(ctx context.Context, owner, template string, ttl time.Duration) (*model.SandboxM, error)
	Exec(ctx context.Context, id, owner, command string, timeout time.Duration) (*sandbox.ExecResult, error)
	ReadFile(ctx context.Context, id, owner, path string, maxBytes int) (string, error)
	WriteFile(ctx context.Context, id, owner, path, content string) error
	List(ctx context.Context, owner string, limit int) ([]*model.SandboxM, error)
	Delete(ctx context.Context, id, owner string) error
}

// SandboxProvider exposes container execution as MCP tools. The owner is the
// run's subject, so one run cannot address another user's sandbox even with an
// id it guessed.
type SandboxProvider struct {
	Service SandboxService
}

// Namespace implements Provider.
func (p *SandboxProvider) Namespace() string { return "sandbox" }

// Tools implements Provider.
func (p *SandboxProvider) Tools() []Tool {
	return []Tool{
		{
			Namespace:   "sandbox",
			Name:        "create",
			Description: "Create an isolated container workspace. Returns the sandbox id used by the other sandbox tools.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"template":   map[string]any{"type": "string", "description": "Configured template name"},
					"ttlSeconds": map[string]any{"type": "integer", "description": "Lifetime; the deployment caps it"},
				},
			},
			Handler: p.create,
		},
		{
			Namespace:   "sandbox",
			Name:        "exec",
			Description: "Run a shell command in a sandbox and return its combined output and exit code.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"sandbox_id", "command"},
				"properties": map[string]any{
					"sandbox_id":     map[string]any{"type": "string"},
					"command":        map[string]any{"type": "string"},
					"timeoutSeconds": map[string]any{"type": "integer"},
				},
			},
			Handler: p.exec,
		},
		{
			Namespace:   "sandbox",
			Name:        "read_file",
			Description: "Read a text file from the sandbox workspace.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"sandbox_id", "path"},
				"properties": map[string]any{
					"sandbox_id": map[string]any{"type": "string"},
					"path":       map[string]any{"type": "string"},
					"maxBytes":   map[string]any{"type": "integer"},
				},
			},
			Handler: p.readFile,
		},
		{
			Namespace:   "sandbox",
			Name:        "write_file",
			Description: "Write a text file into the sandbox workspace.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"sandbox_id", "path", "content"},
				"properties": map[string]any{
					"sandbox_id": map[string]any{"type": "string"},
					"path":       map[string]any{"type": "string"},
					"content":    map[string]any{"type": "string"},
				},
			},
			Handler: p.writeFile,
		},
		{
			Namespace:   "sandbox",
			Name:        "list",
			Description: "List this caller's sandboxes.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			Handler:     p.list,
		},
	}
}

func (p *SandboxProvider) create(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Template   string `json:"template"`
		TTLSeconds int    `json:"ttlSeconds"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("invalid sandbox_create arguments")
	}
	sandbox, err := p.Service.Create(ctx, claims.Subject, input.Template, time.Duration(input.TTLSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sandbox_id": sandbox.ID, "expires_at": sandbox.ExpiresAt}, nil
}

func (p *SandboxProvider) exec(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		SandboxID     string `json:"sandbox_id"`
		Command       string `json:"command"`
		TimeoutSecond int    `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.SandboxID == "" || input.Command == "" {
		return nil, fmt.Errorf("sandbox_exec needs sandbox_id and command")
	}
	result, err := p.Service.Exec(ctx, input.SandboxID, claims.Subject, input.Command,
		time.Duration(input.TimeoutSecond)*time.Second)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (p *SandboxProvider) readFile(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		SandboxID string `json:"sandbox_id"`
		Path      string `json:"path"`
		MaxBytes  int    `json:"maxBytes"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.SandboxID == "" || input.Path == "" {
		return nil, fmt.Errorf("sandbox_read_file needs sandbox_id and path")
	}
	content, err := p.Service.ReadFile(ctx, input.SandboxID, claims.Subject, input.Path, input.MaxBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": content}, nil
}

func (p *SandboxProvider) writeFile(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		SandboxID string `json:"sandbox_id"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.SandboxID == "" || input.Path == "" {
		return nil, fmt.Errorf("sandbox_write_file needs sandbox_id, path and content")
	}
	if err := p.Service.WriteFile(ctx, input.SandboxID, claims.Subject, input.Path, input.Content); err != nil {
		return nil, err
	}
	return map[string]any{"status": "written"}, nil
}

func (p *SandboxProvider) list(ctx context.Context, claims Claims, _ json.RawMessage) (any, error) {
	sandboxes, err := p.Service.List(ctx, claims.Subject, 50)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(sandboxes))
	for _, sandbox := range sandboxes {
		out = append(out, map[string]any{
			"id":         sandbox.ID,
			"template":   sandbox.Template,
			"state":      sandbox.State,
			"expires_at": sandbox.ExpiresAt,
			"created_at": sandbox.CreatedAt,
		})
	}
	return map[string]any{"sandboxes": out}, nil
}
