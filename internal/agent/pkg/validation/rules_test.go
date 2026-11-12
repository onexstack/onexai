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
	"strings"
	"testing"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestResourceRules(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	digest := "example/runtime@sha256:" + strings.Repeat("a", 64)

	tests := []struct {
		name    string
		run     func() error
		wantErr bool
	}{
		{
			name: "agent binding with neither selector nor agents",
			run: func() error {
				return v.ValidateCreateAgentBindingRequest(ctx, &v1.CreateAgentBindingRequest{
					AgentBinding: &v1.AgentBinding{
						Name: "binding",
						Spec: &v1.AgentBindingSpec{},
					},
				})
			},
			wantErr: true,
		},
		{
			name: "agent binding with a named agent",
			run: func() error {
				return v.ValidateCreateAgentBindingRequest(ctx, &v1.CreateAgentBindingRequest{
					AgentBinding: &v1.AgentBinding{
						Name: "binding",
						Spec: &v1.AgentBindingSpec{Agents: []string{"helper"}},
					},
				})
			},
		},
		{
			name: "docker hosting with a tag instead of a digest",
			run: func() error {
				return v.ValidateCreateRuntimeHostingRequest(ctx, &v1.CreateRuntimeHostingRequest{
					RuntimeHosting: &v1.RuntimeHosting{
						Name: "hosting", ProfileRef: "profile", Mode: "docker", Image: "example/runtime:latest",
					},
				})
			},
			wantErr: true,
		},
		{
			name: "docker hosting pinned by digest",
			run: func() error {
				return v.ValidateCreateRuntimeHostingRequest(ctx, &v1.CreateRuntimeHostingRequest{
					RuntimeHosting: &v1.RuntimeHosting{
						Name: "hosting", ProfileRef: "profile", Mode: "docker", Image: digest,
					},
				})
			},
		},
		{
			name: "process hosting without a command",
			run: func() error {
				return v.ValidateCreateRuntimeHostingRequest(ctx, &v1.CreateRuntimeHostingRequest{
					RuntimeHosting: &v1.RuntimeHosting{Name: "hosting", ProfileRef: "profile", Mode: "process"},
				})
			},
			wantErr: true,
		},
		{
			name: "a2a profile with both endpoint and hostingRef",
			run: func() error {
				return v.ValidateCreateRuntimeProfileRequest(ctx, &v1.CreateRuntimeProfileRequest{
					RuntimeProfile: &v1.RuntimeProfile{
						Name: "profile", Kind: "a2a", Endpoint: "https://example.com", HostingRef: "hosting",
					},
				})
			},
			wantErr: true,
		},
		{
			name: "a2a profile with an explicit endpoint",
			run: func() error {
				return v.ValidateCreateRuntimeProfileRequest(ctx, &v1.CreateRuntimeProfileRequest{
					RuntimeProfile: &v1.RuntimeProfile{
						Name: "profile", Kind: "a2a", Endpoint: "https://example.com",
					},
				})
			},
		},
		{
			name: "sandbox template image without a digest",
			run: func() error {
				return v.ValidateCreateSandboxTemplateRequest(ctx, &v1.CreateSandboxTemplateRequest{
					SandboxTemplate: &v1.SandboxTemplate{Name: "sandbox", Image: "example/sandbox:latest"},
				})
			},
			wantErr: true,
		},
		{
			name: "sandbox template pinned by digest",
			run: func() error {
				return v.ValidateCreateSandboxTemplateRequest(ctx, &v1.CreateSandboxTemplateRequest{
					SandboxTemplate: &v1.SandboxTemplate{Name: "sandbox", Image: digest},
				})
			},
		},
		{
			name: "scheduled run with a malformed cron expression",
			run: func() error {
				return v.ValidateCreateScheduledRunRequest(ctx, &v1.CreateScheduledRunRequest{
					ScheduledRun: &v1.ScheduledRun{Name: "schedule", Agent: "helper", Prompt: "hi", Schedule: "every minute"},
				})
			},
			wantErr: true,
		},
		{
			name: "scheduled run with a five-field cron expression",
			run: func() error {
				return v.ValidateCreateScheduledRunRequest(ctx, &v1.CreateScheduledRunRequest{
					ScheduledRun: &v1.ScheduledRun{Name: "schedule", Agent: "helper", Prompt: "hi", Schedule: "*/5 * * * *"},
				})
			},
		},
		{
			name: "agent template with malformed spec JSON",
			run: func() error {
				return v.ValidateCreateAgentTemplateRequest(ctx, &v1.CreateAgentTemplateRequest{
					AgentTemplate: &v1.AgentTemplate{Name: "template", Spec: "{"},
				})
			},
			wantErr: true,
		},
		{
			name: "list request with a negative limit",
			run: func() error {
				return v.ValidateListAgentRequest(ctx, &v1.ListAgentRequest{Limit: -1})
			},
			wantErr: true,
		},
		{
			name: "get request with a blank name",
			run: func() error {
				return v.ValidateGetAgentRequest(ctx, &v1.GetAgentRequest{Name: " "})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidationNormalizesIdentifiers pins that normalization happens in the
// validator, on the same request the handler later uses. The handler therefore
// never needs a second TrimSpace, and the store never sees two spellings of one
// identifier.
func TestValidationNormalizesIdentifiers(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	completion := &v1.ChatCompletionRequest{
		Agent:    "  helper  ",
		Model:    "  gpt-4o  ",
		Messages: []*v1.ChatCompletionMessage{{Role: agent.RoleUser, Content: "hi"}},
	}
	if err := v.ValidateChatCompletionRequest(ctx, completion); err != nil {
		t.Fatalf("ValidateChatCompletionRequest() error = %v", err)
	}
	if completion.Agent != "helper" || completion.Model != "gpt-4o" {
		t.Errorf("identifiers = %q/%q, want helper/gpt-4o", completion.Agent, completion.Model)
	}

	payload := &v1.Agent{Name: "  helper  "}
	if err := v.ValidateCreateAgentRequest(ctx, &v1.CreateAgentRequest{Agent: payload}); err != nil {
		t.Fatalf("ValidateCreateAgentRequest() error = %v", err)
	}
	if payload.Name != "helper" {
		t.Errorf("agent name = %q, want helper", payload.Name)
	}
}

func TestRuntimeRequestRules(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		run     func() error
		wantErr bool
	}{
		{
			name: "completion without an agent",
			run: func() error {
				return v.ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{})
			},
			wantErr: true,
		},
		{
			name: "completion with no user message",
			run: func() error {
				return v.ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{Agent: "helper"})
			},
			wantErr: true,
		},
		{
			name: "completion with a user message",
			run: func() error {
				return v.ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{
					Agent:    "helper",
					Messages: []*v1.ChatCompletionMessage{{Role: agent.RoleUser, Content: "hi"}},
				})
			},
		},
		{
			name: "admin ask without a question",
			run: func() error {
				return v.ValidateAdminAskRequest(ctx, &v1.AgentAskRequest{
					Selection: &v1.Selection{Agents: []string{"helper"}},
				})
			},
			wantErr: true,
		},
		{
			name: "image generation without a prompt",
			run: func() error {
				return v.ValidateImageGenerationRequest(ctx, &v1.ImageGenerationRequest{})
			},
			wantErr: true,
		},
		{
			name: "memory list with a negative offset",
			run: func() error {
				return v.ValidateListMemoryRequest(ctx, &v1.ListMemoryRequest{Offset: -1})
			},
			wantErr: true,
		},
		{
			name: "memory delete without a name",
			run: func() error {
				return v.ValidateDeleteMemoryRequest(ctx, &v1.DeleteMemoryRequest{})
			},
			wantErr: true,
		},
		{
			name: "share with an explicit zero ttl",
			run: func() error {
				zero := 0
				return v.ValidateCreateSessionShareRequest(ctx, &CreateSessionShareRequest{ID: "s", TTLSeconds: &zero})
			},
			wantErr: true,
		},
		{
			name: "observability request missing a field",
			run: func() error {
				return v.ValidateGetLogsRequest(ctx, &GetLogsRequest{AccountID: "a", ClusterName: "c"})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
