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

package validation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestCreateAgentRequiresName pins the rule the switch turns on: a request with
// no name must be refused. The store keys agents by name, so accepting it would
// insert a record nobody can address — the state this rule exists to prevent.
func TestCreateAgentRequiresName(t *testing.T) {
	v := New(nil)

	tests := []struct {
		name        string
		agent       *v1.Agent
		wantMessage string
	}{
		{name: "named agent", agent: &v1.Agent{Name: "helper"}},
		{name: "empty payload", agent: nil, wantMessage: "agent is required"},
		{name: "blank name", agent: &v1.Agent{Name: "   "}, wantMessage: "name is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateCreateAgentRequest(context.Background(), &v1.CreateAgentRequest{Agent: tt.agent})
			if tt.wantMessage == "" {
				if err != nil {
					t.Fatalf("ValidateCreateAgentRequest() error = %v, want none", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateCreateAgentRequest() accepted the request")
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error %q does not mention %q", err, tt.wantMessage)
			}
		})
	}
}

// TestCreateSecretRequiresSecretID covers the one resource addressed by
// secretId rather than name: the shared rule would not fire on it, so it needs
// its own guard.
func TestCreateSecretRequiresSecretID(t *testing.T) {
	v := New(nil)

	if err := v.ValidateCreateSecretRequest(context.Background(),
		&v1.CreateSecretRequest{Secret: &v1.Secret{SecretId: "sk-1"}}); err != nil {
		t.Fatalf("named secret rejected: %v", err)
	}
	if err := v.ValidateCreateSecretRequest(context.Background(),
		&v1.CreateSecretRequest{Secret: &v1.Secret{}}); err == nil {
		t.Fatal("secret with no secretId accepted")
	}
}

// TestUpdateAgentRequiresName is the same contract on the update path, which
// reaches the same store method.
func TestUpdateAgentRequiresName(t *testing.T) {
	v := New(nil)

	if err := v.ValidateUpdateAgentRequest(context.Background(),
		&v1.UpdateAgentRequest{Agent: &v1.Agent{Name: "helper"}}); err != nil {
		t.Fatalf("named agent rejected: %v", err)
	}
	if err := v.ValidateUpdateAgentRequest(context.Background(),
		&v1.UpdateAgentRequest{Agent: &v1.Agent{}}); err == nil {
		t.Fatal("agent with no name accepted")
	}
}

// TestAgentPromptInput pins the prompt contract: prompts holds Prompt names,
// and the sentinel is not a valid entry. Free text must be refused rather than
// silently materialized or dropped.
func TestAgentPromptInput(t *testing.T) {
	v := New(nil)
	tooMany := make([]*v1.ObjectReference, maxAgentPromptRefs+1)
	for i := range tooMany {
		tooMany[i] = &v1.ObjectReference{Name: fmt.Sprintf("prompt-%d", i)}
	}

	tests := []struct {
		name        string
		agent       *v1.Agent
		wantMessage string
	}{
		{name: "names", agent: &v1.Agent{Name: "helper", Prompts: objectRefs("one", "two")}},
		{
			name:        "sentinel is not a name",
			agent:       &v1.Agent{Name: "helper", Prompts: objectRefs("__builtin__")},
			wantMessage: "must name a Prompt",
		},
		{
			name:        "invalid name",
			agent:       &v1.Agent{Name: "helper", Prompts: objectRefs("Not A Name")},
			wantMessage: "is not a Prompt name",
		},
		{
			name:        "cross-namespace is refused",
			agent:       &v1.Agent{Name: "helper", Prompts: []*v1.ObjectReference{{Name: "one", Namespace: ptrString("other")}}},
			wantMessage: "must be local",
		},
		{
			name:        "too many",
			agent:       &v1.Agent{Name: "helper", Prompts: tooMany},
			wantMessage: "at most",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateCreateAgentRequest(context.Background(), &v1.CreateAgentRequest{Agent: tt.agent})
			if tt.wantMessage == "" {
				if err != nil {
					t.Fatalf("ValidateCreateAgentRequest() error = %v, want none", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateCreateAgentRequest() accepted the request")
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error %q does not mention %q", err, tt.wantMessage)
			}
		})
	}
}

// TestAgentTemplateInput pins the management-plane template contract: names
// must be resolvable resource names and the list is capped like the CRD.
func TestAgentTemplateInput(t *testing.T) {
	v := New(nil)
	tooMany := make([]*v1.ObjectReference, maxAgentTemplateRefs+1)
	for i := range tooMany {
		tooMany[i] = &v1.ObjectReference{Name: fmt.Sprintf("template-%d", i)}
	}

	tests := []struct {
		name        string
		agent       *v1.Agent
		wantMessage string
	}{
		{name: "names", agent: &v1.Agent{Name: "helper", TemplateRefs: objectRefs("base", "ops")}},
		{name: "empty list", agent: &v1.Agent{Name: "helper", TemplateRefs: []*v1.ObjectReference{}}},
		{
			name:        "invalid name",
			agent:       &v1.Agent{Name: "helper", TemplateRefs: objectRefs("Not A Name")},
			wantMessage: "is not an AgentTemplate name",
		},
		{
			name:        "cross-namespace is refused",
			agent:       &v1.Agent{Name: "helper", TemplateRefs: []*v1.ObjectReference{{Name: "base", Namespace: ptrString("other")}}},
			wantMessage: "must be local",
		},
		{
			name:        "too many",
			agent:       &v1.Agent{Name: "helper", TemplateRefs: tooMany},
			wantMessage: "at most",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateCreateAgentRequest(context.Background(), &v1.CreateAgentRequest{Agent: tt.agent})
			if tt.wantMessage == "" {
				if err != nil {
					t.Fatalf("ValidateCreateAgentRequest() error = %v, want none", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateCreateAgentRequest() accepted the request")
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error %q does not mention %q", err, tt.wantMessage)
			}
		})
	}
}

func objectRefs(names ...string) []*v1.ObjectReference {
	out := make([]*v1.ObjectReference, 0, len(names))
	for _, name := range names {
		out = append(out, &v1.ObjectReference{Name: name})
	}
	return out
}

func ptrString(value string) *string { return &value }
