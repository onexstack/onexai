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

	"github.com/spf13/viper"
)

// TestAgentOptionsYAMLDecode pins the YAML spelling of the context-budget
// knobs. They are the only thing standing between a long conversation and an
// over-window 400, and a misspelled key decodes to a zero value rather than
// erroring — which reads as "context management is off" and leaves the server
// failing exactly as it did before they existed. So this checks the decode path
// the server actually uses, through viper.Unmarshal.
//
// The decode target is a wrapper carrying an "agent" key, mirroring how
// ServerOptions holds this struct. Decoding straight into an *AgentOptions
// would look for these keys at the config root and quietly decode nothing.
func TestAgentOptionsYAMLDecode(t *testing.T) {
	const yamlConfig = `
agent:
  engine: eino
  max-context-tokens: 250000
  context-preserve-tokens: 80000
  max-tool-output-chars: 12000
  tool-search:
    enabled: true
    min-tools: 12
    always-visible:
      - mcp_heat
  agents-md:
    enabled: true
    files:
      - AGENTS.md
    max-bytes: 8192
    per-file-max-bytes: 4096
`

	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yamlConfig)); err != nil {
		t.Fatalf("ReadConfig returned error: %v", err)
	}

	wrapper := struct {
		Agent *AgentOptions `mapstructure:"agent"`
	}{Agent: NewAgentOptions()}
	if err := v.Unmarshal(&wrapper); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	o := wrapper.Agent

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"agent.engine", o.Engine, "eino"},
		{"agent.max-context-tokens", o.MaxContextTokens, 250000},
		{"agent.context-preserve-tokens", o.ContextPreserveTokens, 80000},
		{"agent.max-tool-output-chars", o.MaxToolOutputChars, 12000},
		{"agent.tool-search.enabled", o.ToolSearch.Enabled, true},
		{"agent.tool-search.min-tools", o.ToolSearch.MinTools, 12},
		{"agent.agents-md.enabled", o.AgentsMD.Enabled, true},
		{"agent.agents-md.max-bytes", o.AgentsMD.MaxBytes, 8192},
		{"agent.agents-md.per-file-max-bytes", o.AgentsMD.PerFileMaxBytes, 4096},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(o.ToolSearch.AlwaysVisible) != 1 || o.ToolSearch.AlwaysVisible[0] != "mcp_heat" {
		t.Errorf("agent.tool-search.always-visible = %v, want [mcp_heat]", o.ToolSearch.AlwaysVisible)
	}
	if len(o.AgentsMD.Files) != 1 || o.AgentsMD.Files[0] != "AGENTS.md" {
		t.Errorf("agent.agents-md.files = %v, want [AGENTS.md]", o.AgentsMD.Files)
	}

	if errs := o.Validate(); len(errs) != 0 {
		t.Errorf("decoded configuration does not validate: %v", errs)
	}
}

// TestAgentOptionsContextDefaults pins the budget defaults. A YAML file with no
// "agent:" block must still bound the context, otherwise every deployment that
// never sets these gets the unbounded behaviour that caused the 400.
func TestAgentOptionsContextDefaults(t *testing.T) {
	o := NewAgentOptions()

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"max-context-tokens", o.MaxContextTokens, 500000},
		// Zero means "a third of the budget"; it must not be a literal zero
		// preserve window, which would compress away all history.
		{"context-preserve-tokens", o.ContextPreserveTokens, 0},
		{"max-tool-output-chars", o.MaxToolOutputChars, 50000},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestAgentOptionsContextValidation covers the combinations that would make
// compaction either useless or self-defeating.
func TestAgentOptionsContextValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AgentOptions)
		wantErr bool
	}{
		{"defaults", func(*AgentOptions) {}, false},
		{"context management disabled", func(o *AgentOptions) { o.MaxContextTokens = 0 }, false},
		{"tool truncation disabled", func(o *AgentOptions) { o.MaxToolOutputChars = 0 }, false},
		{"negative budget", func(o *AgentOptions) { o.MaxContextTokens = -1 }, true},
		{"negative preserve", func(o *AgentOptions) { o.ContextPreserveTokens = -1 }, true},
		{"negative tool cap", func(o *AgentOptions) { o.MaxToolOutputChars = -1 }, true},
		// Preserving as much as the budget leaves nothing to compact, so the
		// history would be re-summarized every turn for no reduction.
		{"preserve equals budget", func(o *AgentOptions) {
			o.MaxContextTokens, o.ContextPreserveTokens = 1000, 1000
		}, true},
		{"preserve over budget", func(o *AgentOptions) {
			o.MaxContextTokens, o.ContextPreserveTokens = 1000, 2000
		}, true},
		{"preserve just under budget", func(o *AgentOptions) {
			o.MaxContextTokens, o.ContextPreserveTokens = 1000, 999
		}, false},
		// With context management off there is no budget for preserve to
		// exceed, so the pairing is meaningless rather than invalid.
		{"preserve without a budget", func(o *AgentOptions) {
			o.MaxContextTokens, o.ContextPreserveTokens = 0, 1000
		}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := NewAgentOptions()
			c.mutate(o)
			errs := o.Validate()
			if c.wantErr && len(errs) == 0 {
				t.Fatal("Validate accepted an invalid configuration")
			}
			if !c.wantErr && len(errs) != 0 {
				t.Fatalf("Validate rejected a valid configuration: %v", errs)
			}
		})
	}
}

// TestAgentOptionsEngineRejectsHTTP pins the removal of the http engine.
//
// It is rejected rather than silently mapped to eino: the two engines differ in
// whether tools run at all, so a deployment that asked for http and got eino
// would start calling tools its operator believed were inert.
func TestAgentOptionsEngineRejectsHTTP(t *testing.T) {
	o := NewAgentOptions()
	if o.Engine != "eino" {
		t.Fatalf("default engine = %q, want eino", o.Engine)
	}
	if errs := o.Validate(); len(errs) != 0 {
		t.Fatalf("the default configuration is invalid: %v", errs)
	}

	o.Engine = "http"
	errs := o.Validate()
	if len(errs) == 0 {
		t.Fatal("Validate accepted the removed http engine")
	}
	if !strings.Contains(errs[0].Error(), "was removed") {
		t.Errorf("error does not explain the removal: %v", errs[0])
	}

	o.Engine = "nonsense"
	if errs := o.Validate(); len(errs) == 0 {
		t.Fatal("Validate accepted an unknown engine")
	}
}
