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

package agentcmd

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// fakeCustoms is a custom command source over a fixed per-agent map.
type fakeCustoms struct {
	commands map[string][]runtime.CustomCommand
	err      error
}

func (f fakeCustoms) CustomCommandsOf(_ context.Context, agent string) ([]runtime.CustomCommand, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.commands[agent], nil
}

func TestResolveReturnsBoundCustomCommandsWithoutRules(t *testing.T) {
	resolver := New(Options{
		Rules:  &fakeRules{},
		Agents: &fakeAgents{},
		Customs: fakeCustoms{commands: map[string][]runtime.CustomCommand{
			"helper": {{Name: "hello", Type: "prompt", Aliases: []string{"hi"}}},
		}},
	})

	selection := resolver.Resolve(context.Background(), []string{"helper"})
	if len(selection.Customs) != 1 {
		t.Fatalf("customs = %v, want one command", selection.Customs)
	}
	if selection.Customs[0].Name != "hello" {
		t.Errorf("name = %q, want hello", selection.Customs[0].Name)
	}
	if selection.Customs[0].Agent != "helper" {
		t.Errorf("agent = %q, want helper", selection.Customs[0].Agent)
	}
	if !selection.AllowsCustom("hello") || !selection.AllowsCustom("hi") {
		t.Errorf("the command or its alias was not allowed: %+v", selection.Customs)
	}
	if !selection.AllowsAny() {
		t.Errorf("a selection holding only custom commands is not empty")
	}
}

func TestResolveDeduplicatesACustomCommandAcrossAgents(t *testing.T) {
	resolver := New(Options{
		Rules:  &fakeRules{},
		Agents: &fakeAgents{},
		Customs: fakeCustoms{commands: map[string][]runtime.CustomCommand{
			"a": {{Name: "shared"}},
			"b": {{Name: "shared"}},
		}},
	})

	selection := resolver.Resolve(context.Background(), []string{"a", "b"})
	if len(selection.Customs) != 1 {
		t.Fatalf("customs = %v, want one deduplicated command", selection.Customs)
	}
	if selection.Customs[0].Agent != "a" {
		t.Errorf("agent = %q, want the first agent", selection.Customs[0].Agent)
	}
}

func TestResolveSurvivesACustomSourceFailure(t *testing.T) {
	resolver := New(Options{
		Rules:   &fakeRules{},
		Agents:  &fakeAgents{},
		Customs: fakeCustoms{err: errors.New("agent service is down")},
	})

	selection := resolver.Resolve(context.Background(), []string{"helper"})
	if len(selection.Customs) != 0 {
		t.Fatalf("customs = %v, want none", selection.Customs)
	}
}

func TestResolveWithoutAgentsReturnsNoCustomCommands(t *testing.T) {
	resolver := New(Options{
		Rules:   &fakeRules{},
		Agents:  &fakeAgents{},
		Customs: fakeCustoms{commands: map[string][]runtime.CustomCommand{"helper": {{Name: "hello"}}}},
	})

	selection := resolver.Resolve(context.Background(), nil)
	if len(selection.Customs) != 0 {
		t.Fatalf("customs = %v, want none without an agent", selection.Customs)
	}
}
