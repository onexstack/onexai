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

	"github.com/onexstack/onexstack/pkg/ptr"
)

// cliGuardError reports whether Validate rejected the combination of an enabled
// CLI backend with auth that admits anonymous or unauthorised requests. The
// aggregate carries unrelated validation errors too, so the assertion is on the
// specific message rather than on the presence of any error.
func cliGuardError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "agents.cli.enabled")
}

// TestCLIBackendRefusedUnderPermissiveAuth is the startup guard for the local
// agent-CLI backend. That backend runs a process on the server host on behalf
// of whoever can reach the API; paired with anonymous access or AlwaysAllow it
// is remote code execution reachable from the network. The server must refuse
// to start, because once it is running the misconfiguration is indistinguishable
// from a working deployment.
func TestCLIBackendRefusedUnderPermissiveAuth(t *testing.T) {
	tests := []struct {
		name       string
		cliEnabled bool
		anonymous  bool
		authMode   string
		wantRefuse bool
	}{
		{
			name:       "cli on, anonymous auth on",
			cliEnabled: true,
			anonymous:  true,
			wantRefuse: true,
		},
		{
			name:       "cli on, AlwaysAllow",
			cliEnabled: true,
			authMode:   "AlwaysAllow",
			wantRefuse: true,
		},
		{
			name:       "cli on, anonymous auth on and AlwaysAllow",
			cliEnabled: true,
			anonymous:  true,
			authMode:   "AlwaysAllow",
			wantRefuse: true,
		},
		{
			// The shipped posture: backend off, so permissive auth is a
			// separate decision and this guard stays out of the way.
			name:       "cli off, anonymous auth on",
			anonymous:  true,
			wantRefuse: false,
		},
		{
			name:       "cli off, AlwaysAllow",
			authMode:   "AlwaysAllow",
			wantRefuse: false,
		},
		{
			// The supported configuration: backend on, real auth.
			name:       "cli on, normal auth",
			cliEnabled: true,
			wantRefuse: false,
		},
		{
			name:       "cli on, AlwaysDeny",
			cliEnabled: true,
			authMode:   "AlwaysDeny",
			wantRefuse: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewServerOptions()
			o.AuthOptions.AnonymousAuth = tt.anonymous
			o.AuthOptions.AuthorizationMode = tt.authMode
			o.ChatOptions.Agents.CLI.Enabled = tt.cliEnabled
			if tt.cliEnabled {
				// Satisfy the backend's own requirements so the only error this
				// test can trip on is the auth guard.
				o.ChatOptions.Agents.CLI.WorkspaceRoot = "/var/lib/onexai/workspaces"
			}

			got := cliGuardError(o.Validate())
			if got != tt.wantRefuse {
				t.Errorf("cliGuardError(Validate()) = %v, want %v", got, tt.wantRefuse)
			}
		})
	}
}

// TestDefaultServerOptionsHaveNoChatErrors ensures the default chat
// block contributes no validation errors of its own, so the guard above is the
// only thing it can ever trip.
//
// A freshly constructed ServerOptions does not validate cleanly overall — the
// Resty-backed clients require an endpoint that only the config file supplies —
// so the assertion is that no error mentions the chat block or the CLI
// backend, rather than that the aggregate is nil.
func TestDefaultServerOptionsHaveNoChatErrors(t *testing.T) {
	err := NewServerOptions().Validate()
	if err == nil {
		return
	}

	for _, marker := range []string{"chat.", "agents.cli", "agents.default-backend"} {
		if strings.Contains(err.Error(), marker) {
			t.Errorf("default configuration produces a %q error: %v", marker, err)
		}
	}
}

// TestConfigCarriesChatOptions ensures the options survive the hop into
// the server Config. A field dropped in Config() would leave every knob
// silently inert while still being settable, which is worse than absent.
func TestConfigCarriesChatOptions(t *testing.T) {
	o := NewServerOptions()
	o.ChatOptions.Agents.CLI.MaxSessions = 7
	o.ChatOptions.Session.TurnTimeout = 42

	cfg, err := o.Config()
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	if cfg.ChatOptions == nil {
		t.Fatal("Config().ChatOptions is nil; the chat block would be ignored")
	}
	if cfg.ChatOptions.Agents.CLI.MaxSessions != 7 {
		t.Errorf("Config().ChatOptions.Agents.CLI.MaxSessions = %d, want 7",
			cfg.ChatOptions.Agents.CLI.MaxSessions)
	}
	if cfg.ChatOptions.Session.TurnTimeout != 42 {
		t.Errorf("Config().ChatOptions.Session.TurnTimeout = %v, want 42",
			cfg.ChatOptions.Session.TurnTimeout)
	}
}

// TestFeatureOptionsDefaultOff pins the contract the alignment work depends on:
// a configuration that predates the features block must leave every switch off,
// so the services behave exactly as they did before the switches existed.
func TestFeatureOptionsDefaultOff(t *testing.T) {
	o := NewServerOptions()
	if o.FeatureOptions == nil {
		t.Fatal("NewServerOptions left FeatureOptions nil")
	}

	// Complete() and Config() must both tolerate a nil block, which is what a
	// pre-existing config file unmarshals into.
	nilBlock := NewServerOptions()
	nilBlock.FeatureOptions = nil
	if err := nilBlock.Complete(); err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if nilBlock.FeatureOptions == nil {
		t.Fatal("Complete() did not allocate the feature block")
	}

	cfg, err := nilBlock.Config()
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	if cfg.FeatureOptions == nil {
		t.Fatal("Config().FeatureOptions is nil; the switches would be ignored")
	}

	f := ptr.From(cfg.FeatureOptions)
	if f.Pagination || f.ObservabilitySkip || f.UnifiedPanic || f.AlignedMetricNames {
		t.Errorf("feature switches default to on: %+v", f)
	}
}
