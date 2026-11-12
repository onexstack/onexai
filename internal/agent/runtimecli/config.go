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

// Package runtimecli wraps a command-line agent (Claude Code, Codex, or any
// tool with a text/stream-json interface) as an A2A runtime.
package runtimecli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config describes the CLI to run.
type Config struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	// Command is the executable, e.g. "claude".
	Command string `json:"command"`
	// Args are passed verbatim except for {{prompt}}, which is replaced with
	// the user's message. With no placeholder the prompt is appended.
	Args []string `json:"args"`
	// Format is "text" (each stdout line is answer text) or "claude" (Claude
	// Code stream-json).
	Format string `json:"format"`
	// Workdir is the process working directory.
	Workdir string `json:"workdir"`
	// Env adds to the process environment.
	Env map[string]string `json:"env"`
	// SessionDir keeps the CLI's own conversation id per A2A context, so a
	// second turn resumes the conversation instead of starting over. Empty
	// disables continuation, which is what a CLI without a resume flag wants.
	SessionDir string `json:"sessionDir"`
	// Approval routes a tool call that would otherwise prompt to this process,
	// where a human answers it. It is the only way a CLI agent can consult
	// someone: the CLI has no pause of its own, so the pause is a tool call
	// that blocks.
	Approval ApprovalConfig `json:"approval"`
}

// ApprovalConfig turns on human approval for CLI tool calls.
type ApprovalConfig struct {
	// Enabled installs the permission-prompt tool.
	Enabled bool `json:"enabled"`
	// Tools lists the tool names or patterns the CLI must ask about, in the
	// CLI's own syntax ("Bash", "Write", "mcp__github__*"). Empty asks about
	// every tool that would otherwise be auto-approved, which is the strictest
	// setting and the one a deployment that turned this on meant.
	Tools []string `json:"tools,omitempty"`
}

// Load reads a config file and validates it.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runtime config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decode runtime config: %w", err)
	}
	if config.Command == "" {
		return nil, fmt.Errorf("runtime config: command is required")
	}
	if config.Name == "" {
		config.Name = "cli-agent"
	}
	if config.Version == "" {
		config.Version = "v1"
	}
	switch config.Format {
	case "", "text":
		config.Format = "text"
	case "claude":
	default:
		return nil, fmt.Errorf("runtime config: unsupported format %q", config.Format)
	}
	return &config, nil
}

// Arguments builds the process arguments for one prompt.
//
// resumeID continues a conversation the CLI already knows about; empty starts a
// new one. Only a runtime with a resume flag uses it.
func (c *Config) Arguments(prompt, resumeID string) []string {
	args := make([]string, 0, len(c.Args)+1)
	replaced := false
	for _, arg := range c.Args {
		if strings.Contains(arg, "{{prompt}}") {
			args = append(args, strings.ReplaceAll(arg, "{{prompt}}", prompt))
			replaced = true
			continue
		}
		args = append(args, arg)
	}
	if !replaced {
		args = append(args, prompt)
	}
	if resumeID != "" && c.Format == "claude" {
		// --resume, not --continue: --continue picks up whichever session ran
		// last, which a subagent or an interrupted attempt can redirect.
		args = append(args, "--resume", resumeID)
	}
	return args
}

// Environment returns the process environment.
func (c *Config) Environment() []string {
	env := os.Environ()
	for key, value := range c.Env {
		env = append(env, key+"="+value)
	}
	return env
}
