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
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/pkg/agentlabel"
)

// TestPreview covers what an operator is shown before a rule is stored.
//
// The two questions are "who does this cover" and "what do they get", and both
// have a silent wrong answer: a selector read as narrower than it was written
// covers nobody, and one read as wider covers everybody.
func TestPreview(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{
		// The second tag is the shape that makes the dropped-tag report worth
		// having: its key is a legal label name and its value is not, so a
		// selector can name the key — "team" — and never match. A tag whose
		// key is illegal is unnameable in the first place, because the selector
		// grammar rejects it too.
		{Name: "bke-agent", Labels: map[string]string{"sre": ""}},
		{Name: "plain-agent"},
	}}

	tests := []struct {
		name         string
		rule         *model.AgentCommandM
		wantAgents   []string
		wantCommands []string
		wantNamed    []string
	}{
		{
			name:         "naming an agent covers it and lists what it gets",
			rule:         rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
			wantAgents:   []string{"bke-agent"},
			wantCommands: []string{"bind-onex", "doctor"},
		},
		{
			name:         "a star covers the whole list",
			rule:         rule("r", "", `["*"]`, "", `["pod"]`),
			wantAgents:   []string{"bke-agent", "plain-agent"},
			wantCommands: []string{"pod"},
		},
		{
			name:         "a tag selector covers only the agent that has it",
			rule:         rule("r", "sre", "", groupSelector(command.GroupAI), ""),
			wantAgents:   []string{"bke-agent"},
			wantCommands: []string{"agent", "aiassets", "ailab", "askai", "clear", "dir", "new", "sessions", "switch"},
		},
		{
			name:       "a skill named outright is reported as such",
			rule:       rule("r", "", `["*"]`, "", `["skill:k8s-troubleshoot"]`),
			wantAgents: []string{"bke-agent", "plain-agent"},
			wantNamed:  []string{"k8s-troubleshoot"},
		},
		{
			name:       "a group the tree does not have grants nothing, visibly",
			rule:       rule("r", "", `["*"]`, command.LabelGroup+"=no-such-group", ""),
			wantAgents: []string{"bke-agent", "plain-agent"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(&fakeRules{}, agents, nil)

			got, err := r.Preview(context.Background(), tc.rule)
			if err != nil {
				t.Fatalf("Preview = %v", err)
			}

			if len(got.Agents) != len(tc.wantAgents) {
				t.Fatalf("Agents = %v, want %v", got.Agents, tc.wantAgents)
			}
			for i, name := range tc.wantAgents {
				if got.Agents[i] != name {
					t.Errorf("Agents[%d] = %q, want %q", i, got.Agents[i], name)
				}
			}
			if len(got.Commands) != len(tc.wantCommands) {
				t.Fatalf("Commands = %v, want %v", got.Commands, tc.wantCommands)
			}
			for i, name := range tc.wantCommands {
				if got.Commands[i] != name {
					t.Errorf("Commands[%d] = %q, want %q", i, got.Commands[i], name)
				}
			}
			if len(got.NamedSkills) != len(tc.wantNamed) {
				t.Fatalf("NamedSkills = %v, want %v", got.NamedSkills, tc.wantNamed)
			}

			// The two questions the admin surface refuses a rule over.
			if covers := got.Covers(); covers != (len(tc.wantAgents) > 0) {
				t.Errorf("Covers = %v, want %v", covers, len(tc.wantAgents) > 0)
			}
			wantGrants := len(tc.wantCommands) > 0 || len(tc.wantNamed) > 0
			if grants := got.Grants(); grants != wantGrants {
				t.Errorf("Grants = %v, want %v", grants, wantGrants)
			}
		})
	}
}

// TestPreview_ReportsTheLabelsItMatchedOn pins that the report says what the
// selector was reading. Without it an operator looking at "covers nobody" has
// only the selector to go on, and the label set is the other half of that
// question.
func TestPreview_ReportsTheLabelsItMatchedOn(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent", Namespace: "bke", Labels: map[string]string{"sre": ""}}}}
	r := newResolver(&fakeRules{}, agents, nil)

	got, err := r.Preview(context.Background(), rule("r", "sre", "", groupSelector(command.GroupCore), ""))
	if err != nil {
		t.Fatalf("Preview = %v", err)
	}

	ls, ok := got.AgentLabels["bke-agent"]
	if !ok {
		t.Fatal("the report does not say which labels the agent was matched on")
	}
	if ls["sre"] != "" {
		t.Errorf("labels = %v, want the sre tag among them", ls)
	}

	// The record's own columns are in the other space: the label set has no
	// namespace in it at all, and the field set has nothing but the record.
	if _, ok := ls["namespace"]; ok {
		t.Errorf("labels = %v, want no record columns among them", ls)
	}
	fs, ok := got.AgentFields["bke-agent"]
	if !ok {
		t.Fatal("the report does not say which fields the agent was matched on")
	}
	if fs[agentlabel.FieldNamespace] != "bke" {
		t.Errorf("fields = %v, want the namespace among them", fs)
	}
}
