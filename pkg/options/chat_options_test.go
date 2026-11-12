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

package options

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestChatOptionsYAMLDecode pins the YAML spelling of every field,
// including that durations decode from their human-readable form ("15m", not a
// nanosecond count). The server reads its config through viper.Unmarshal, whose
// mapstructure hook does the duration parsing; if that ever stops applying, the
// knobs would silently decode to zero and the defaults would be replaced by
// broken values, so this checks the decode path the server actually uses rather
// than the struct in isolation.
//
// The decode target is a wrapper carrying a "chat" key, mirroring how
// ServerOptions holds this struct. Decoding straight into a *ChatOptions
// would look for session/streaming/... at the config root and quietly decode
// nothing, leaving every default in place — silence that looks exactly like a
// working config file.
func TestChatOptionsYAMLDecode(t *testing.T) {
	const yamlConfig = `
chat:
  session:
    idle-timeout: 15m
    event-idle-timeout: 2h
    close-timeout: 150s
    busy-stale-lock-max-held: 2m
    max-queued-messages: 5
    max-concurrent-turns: 2
    turn-timeout: 5m
    data-dir: /var/lib/onexai
  streaming:
    enabled: true
    interval: 1500ms
    min-delta-chars: 30
    max-chars: 2000
    print-frequency-ms: 50
    print-step: 2
    print-strategy: fast
  progress:
    style: card
    max-entries: 10
    interval: 250ms
  dedup:
    enabled: true
    ttl-sec: 300
    max-entries: 4096
  agent-command:
    rules-ttl: 5s
    agents-ttl: 30s
    skills-ttl: 90s
  roles:
    admin-user-ids: "ou_a,ou_b"
  agents:
    default-backend: cli
    cli:
      enabled: true
      max-sessions: 4
      workspace-root: /var/lib/onexai/workspaces
      allowed-dirs:
        - /srv/repos
      idle-timeout: 15m
      turn-timeout: 5m
      permission-mode: stdio
      allowed-tools:
        - Read
        - Grep
      disallowed-tools:
        - Bash
      model: claude-sonnet-5
      max-line-bytes: 8388608
`

	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yamlConfig)); err != nil {
		t.Fatalf("ReadConfig returned error: %v", err)
	}

	wrapper := struct {
		Chat *ChatOptions `mapstructure:"chat"`
	}{Chat: NewChatOptions()}
	if err := v.Unmarshal(&wrapper); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	o := wrapper.Chat

	session := []struct {
		name string
		got  any
		want any
	}{
		{"session.idle-timeout", o.Session.IdleTimeout, 15 * time.Minute},
		{"session.event-idle-timeout", o.Session.EventIdleTimeout, 2 * time.Hour},
		{"session.close-timeout", o.Session.CloseTimeout, 150 * time.Second},
		{"session.busy-stale-lock-max-held", o.Session.BusyStaleLockMaxHeld, 2 * time.Minute},
		{"session.max-queued-messages", o.Session.MaxQueuedMessages, 5},
		{"session.max-concurrent-turns", o.Session.MaxConcurrentTurns, 2},
		{"session.turn-timeout", o.Session.TurnTimeout, 5 * time.Minute},
		{"session.data-dir", o.Session.DataDir, "/var/lib/onexai"},
		{"streaming.interval", o.Streaming.Interval, 1500 * time.Millisecond},
		{"streaming.min-delta-chars", o.Streaming.MinDeltaChars, 30},
		{"streaming.max-chars", o.Streaming.MaxChars, 2000},
		{"streaming.print-frequency-ms", o.Streaming.PrintFrequencyMs, 50},
		{"streaming.print-step", o.Streaming.PrintStep, 2},
		{"streaming.print-strategy", o.Streaming.PrintStrategy, "fast"},
		{"progress.style", o.Progress.Style, "card"},
		{"progress.max-entries", o.Progress.MaxEntries, 10},
		{"progress.interval", o.Progress.Interval, 250 * time.Millisecond},
		{"dedup.ttl-sec", o.Dedup.TTLSec, 300},
		{"dedup.max-entries", o.Dedup.MaxEntries, 4096},
		{"agent-command.rules-ttl", o.AgentCommand.RulesTTL, 5 * time.Second},
		{"agent-command.skills-ttl", o.AgentCommand.SkillsTTL, 90 * time.Second},
		{"agents.default-backend", o.Agents.DefaultBackend, AgentBackendCLI},
		{"agents.cli.max-sessions", o.Agents.CLI.MaxSessions, 4},
		{"agents.cli.workspace-root", o.Agents.CLI.WorkspaceRoot, "/var/lib/onexai/workspaces"},
		{"agents.cli.idle-timeout", o.Agents.CLI.IdleTimeout, 15 * time.Minute},
		{"agents.cli.turn-timeout", o.Agents.CLI.TurnTimeout, 5 * time.Minute},
		{"agents.cli.permission-mode", o.Agents.CLI.PermissionMode, CLIPermissionModeStdio},
		{"agents.cli.model", o.Agents.CLI.Model, "claude-sonnet-5"},
		{"agents.cli.max-line-bytes", o.Agents.CLI.MaxLineBytes, 8388608},
	}
	for _, c := range session {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if !o.Agents.CLI.Enabled {
		t.Error("agents.cli.enabled = false, want true")
	}
	if got, want := strings.Join(o.Agents.CLI.AllowedTools, ","), "Read,Grep"; got != want {
		t.Errorf("agents.cli.allowed-tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(o.Agents.CLI.DisallowedTools, ","), "Bash"; got != want {
		t.Errorf("agents.cli.disallowed-tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(o.Agents.CLI.AllowedDirs, ","), "/srv/repos"; got != want {
		t.Errorf("agents.cli.allowed-dirs = %q, want %q", got, want)
	}

	if errs := o.Validate(); len(errs) != 0 {
		t.Errorf("decoded configuration does not validate: %v", errs)
	}
}

// TestChatOptionsDefaults pins every default to the behaviour the server
// had before these knobs existed: the hardcoded aiTurnTimeout, the cardkit
// print hints baked into streaming_card.go, the dedup TTL and cache size, and
// the progress panel's entry cap. A YAML file with no "chat:" block must
// reproduce the old server exactly, so a drift in any of these is a silent
// behaviour change and this test is where it becomes visible.
func TestChatOptionsDefaults(t *testing.T) {
	o := NewChatOptions()

	session := []struct {
		name string
		got  any
		want any
	}{
		{"session.idle-timeout", o.Session.IdleTimeout, 15 * time.Minute},
		{"session.event-idle-timeout", o.Session.EventIdleTimeout, 2 * time.Hour},
		{"session.close-timeout", o.Session.CloseTimeout, 150 * time.Second},
		{"session.busy-stale-lock-max-held", o.Session.BusyStaleLockMaxHeld, 2 * time.Minute},
		{"session.max-queued-messages", o.Session.MaxQueuedMessages, 5},
		// A deliberate break from "default == the old behaviour": the bot now
		// answers up to three questions per conversation at once unless a config
		// file says otherwise. 1 restores the serial-and-queue behaviour.
		{"session.max-concurrent-turns", o.Session.MaxConcurrentTurns, 3},
		// aiTurnTimeout in biz/v1/bot/ai.go.
		{"session.turn-timeout", o.Session.TurnTimeout, 5 * time.Minute},
		// Empty means "use the server's home directory", which is what the
		// server does today via app.WithDirInHome.
		{"session.data-dir", o.Session.DataDir, ""},
	}
	for _, c := range session {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	streaming := []struct {
		name string
		got  any
		want any
	}{
		{"streaming.enabled", o.Streaming.Enabled, true},
		{"streaming.interval", o.Streaming.Interval, 1500 * time.Millisecond},
		{"streaming.min-delta-chars", o.Streaming.MinDeltaChars, 30},
		{"streaming.max-chars", o.Streaming.MaxChars, 2000},
		// These three are currently hardcoded in streaming_card.go's
		// streaming_config block; changing them changes what Feishu renders.
		{"streaming.print-frequency-ms", o.Streaming.PrintFrequencyMs, 50},
		{"streaming.print-step", o.Streaming.PrintStep, 2},
		{"streaming.print-strategy", o.Streaming.PrintStrategy, "fast"},
	}
	for _, c := range streaming {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	progress := []struct {
		name string
		got  any
		want any
	}{
		{"progress.style", o.Progress.Style, "card"},
		{"progress.max-entries", o.Progress.MaxEntries, 10},
		{"progress.interval", o.Progress.Interval, 250 * time.Millisecond},
	}
	for _, c := range progress {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	dedup := []struct {
		name string
		got  any
		want any
	}{
		{"dedup.enabled", o.Dedup.Enabled, true},
		{"dedup.ttl-sec", o.Dedup.TTLSec, 300},
		{"dedup.max-entries", o.Dedup.MaxEntries, 4096},
	}
	for _, c := range dedup {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// The rule snapshot is the one that changed on purpose: 15s was long enough
	// that an operator editing a rule watched it not take effect. The other two
	// are the values the resolver compiled in before they were configurable.
	agentCommand := []struct {
		name string
		got  any
		want any
	}{
		{"agent-command.rules-ttl", o.AgentCommand.RulesTTL, 5 * time.Second},
		{"agent-command.skills-ttl", o.AgentCommand.SkillsTTL, 2 * time.Minute},
	}
	for _, c := range agentCommand {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if o.Agents.DefaultBackend != AgentBackendLLM {
		t.Errorf("agents.default-backend = %q, want %q", o.Agents.DefaultBackend, AgentBackendLLM)
	}
}

// TestChatOptionsCLIDefaultsAreSafe pins the CLI backend's out-of-the-box
// posture. It is the one backend that runs local processes, so these defaults
// are a security boundary, not a preference: off, prompting for permission, and
// with a line buffer large enough for a real tool result.
func TestChatOptionsCLIDefaultsAreSafe(t *testing.T) {
	cli := NewChatOptions().Agents.CLI

	if cli.Enabled {
		t.Error("agents.cli.enabled defaults to true; the local-process backend must be opt-in")
	}
	if cli.PermissionMode != CLIPermissionModeStdio {
		t.Errorf("agents.cli.permission-mode = %q, want %q (prompt the user, do not bypass)",
			cli.PermissionMode, CLIPermissionModeStdio)
	}
	if cli.MaxLineBytes < 8<<20 {
		t.Errorf("agents.cli.max-line-bytes = %d, want at least %d; a single large tool result "+
			"would otherwise kill the session", cli.MaxLineBytes, 8<<20)
	}
	if cli.Model != "" {
		t.Errorf("agents.cli.model = %q, want empty (defer to the CLI's own default)", cli.Model)
	}
	if len(cli.AllowedTools) != 0 || len(cli.DisallowedTools) != 0 {
		t.Errorf("agents.cli tool lists default to %v/%v, want empty", cli.AllowedTools, cli.DisallowedTools)
	}
}

// TestChatOptionsDefaultsValidate ensures the shipped defaults actually
// pass validation. A default set that fails its own Validate is unreachable
// otherwise, since nothing constructs the options without also validating them.
func TestChatOptionsDefaultsValidate(t *testing.T) {
	if errs := NewChatOptions().Validate(); len(errs) != 0 {
		t.Errorf("NewChatOptions().Validate() = %v, want no errors", errs)
	}
}

func TestChatOptionsValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatOptions)
	}{
		{"zero turn timeout", func(o *ChatOptions) { o.Session.TurnTimeout = 0 }},
		{"negative queue depth", func(o *ChatOptions) { o.Session.MaxQueuedMessages = -1 }},
		{"zero concurrent turns", func(o *ChatOptions) { o.Session.MaxConcurrentTurns = 0 }},
		{"zero streaming interval", func(o *ChatOptions) { o.Streaming.Interval = 0 }},
		{"zero min delta", func(o *ChatOptions) { o.Streaming.MinDeltaChars = 0 }},
		{"max below min delta", func(o *ChatOptions) {
			o.Streaming.MinDeltaChars = 100
			o.Streaming.MaxChars = 50
		}},
		{"empty default backend", func(o *ChatOptions) { o.Agents.DefaultBackend = "" }},
		{"unknown default backend", func(o *ChatOptions) { o.Agents.DefaultBackend = "sandbox" }},
		{"unknown permission mode", func(o *ChatOptions) { o.Agents.CLI.PermissionMode = "yolo" }},
		{"zero agent-command rules ttl", func(o *ChatOptions) { o.AgentCommand.RulesTTL = 0 }},
		{"negative agent-command skills ttl", func(o *ChatOptions) { o.AgentCommand.SkillsTTL = -time.Second }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewChatOptions()
			tt.mutate(o)
			if errs := o.Validate(); len(errs) == 0 {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// TestChatOptionsCLIEnabledRequiresWorkspaceRoot covers the guard that
// keeps a half-configured local-process backend from starting.
func TestChatOptionsCLIEnabledRequiresWorkspaceRoot(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ChatOptions)
		wantErr bool
	}{
		{
			name:    "enabled without a root",
			mutate:  func(o *ChatOptions) { o.Agents.CLI.Enabled = true },
			wantErr: true,
		},
		{
			name: "enabled with a relative root",
			mutate: func(o *ChatOptions) {
				o.Agents.CLI.Enabled = true
				o.Agents.CLI.WorkspaceRoot = "workspaces"
			},
			wantErr: true,
		},
		{
			name: "enabled with a tiny line buffer",
			mutate: func(o *ChatOptions) {
				o.Agents.CLI.Enabled = true
				o.Agents.CLI.WorkspaceRoot = "/var/lib/onexai/workspaces"
				o.Agents.CLI.MaxLineBytes = 4096
			},
			wantErr: true,
		},
		{
			name: "enabled with an absolute root",
			mutate: func(o *ChatOptions) {
				o.Agents.CLI.Enabled = true
				o.Agents.CLI.WorkspaceRoot = "/var/lib/onexai/workspaces"
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewChatOptions()
			tt.mutate(o)
			errs := o.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Error("Validate() = nil, want an error")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Errorf("Validate() = %v, want no errors", errs)
			}
		})
	}
}
