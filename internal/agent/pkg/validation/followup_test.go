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

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

type fakeModelStore struct {
	store.ModelStore
	row *model.ModelM
}

func (f fakeModelStore) Get(context.Context, *where.Options) (*model.ModelM, error) {
	return f.row, nil
}

type fakeSecretStore struct {
	store.SecretStore
	row *model.SecretM
}

func (f fakeSecretStore) Get(context.Context, *where.Options) (*model.SecretM, error) {
	return f.row, nil
}

type fakeSkillStore struct {
	store.SkillStore
	row *model.SkillM
}

func (f fakeSkillStore) Get(context.Context, *where.Options) (*model.SkillM, error) {
	return f.row, nil
}

type fakeResources struct {
	store.AIStore
	models  store.ModelStore
	secrets store.SecretStore
	skills  store.SkillStore
}

func (f fakeResources) Models() store.ModelStore   { return f.models }
func (f fakeResources) Secrets() store.SecretStore { return f.secrets }
func (f fakeResources) Skills() store.SkillStore   { return f.skills }

func TestChatCompletionChecksAgentExistence(t *testing.T) {
	ctx := context.Background()
	messages := []*v1.ChatCompletionMessage{{Role: agent.RoleUser, Content: "hi"}}

	v := New(store.NewEmptyStore())
	err := v.ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{Agent: "missing", Messages: messages})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown agent error = %v, want a not-found", err)
	}

	if err := v.ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{Agent: v1.NoAgent, Messages: messages}); err != nil {
		t.Fatalf("the sentinel was checked against the store: %v", err)
	}

	if err := New(nil).ValidateChatCompletionRequest(ctx, &v1.ChatCompletionRequest{Agent: "missing", Messages: messages}); err != nil {
		t.Fatalf("a nil store should skip the lookup: %v", err)
	}
}

func TestUpdateValidatorsLeaveStoredFieldsToBiz(t *testing.T) {
	ctx := context.Background()
	v := New(fakeResources{
		models: fakeModelStore{row: &model.ModelM{
			APIKey: "sealed", Owner: "u1", Capabilities: []string{"vision"}, CreatedAt: 42,
		}},
		secrets: fakeSecretStore{row: &model.SecretM{SecretKey: "k1"}},
		skills: fakeSkillStore{row: &model.SkillM{
			Mode: "tool", Params: "p", Contract: "c", Entrypoint: "e",
		}},
	})

	modelReq := &v1.UpdateModelRequest{Model: &v1.Model{Name: "m"}}
	if err := v.ValidateUpdateModelRequest(ctx, modelReq); err != nil {
		t.Fatalf("ValidateUpdateModelRequest() error = %v", err)
	}

	if modelReq.Model.ApiKey != "" || modelReq.Model.Owner != "" ||
		len(modelReq.Model.Capabilities) != 0 || modelReq.Model.CreatedAt != 0 {
		t.Errorf("validator hydrated the model request: %+v", modelReq.Model)
	}

	secretReq := &v1.UpdateSecretRequest{Secret: &v1.Secret{SecretId: "s"}}
	if err := v.ValidateUpdateSecretRequest(ctx, secretReq); err != nil {
		t.Fatalf("ValidateUpdateSecretRequest() error = %v", err)
	}

	if secretReq.Secret.SecretKey != "" {
		t.Errorf("validator hydrated the secret key: %q", secretReq.Secret.SecretKey)
	}

	skillReq := &v1.UpdateSkillRequest{Skill: &v1.Skill{Name: "sk"}}
	if err := v.ValidateUpdateSkillRequest(ctx, skillReq); err != nil {
		t.Fatalf("ValidateUpdateSkillRequest() error = %v", err)
	}

	if skillReq.Skill.Mode != "" || skillReq.Skill.Entrypoint != "" ||
		skillReq.Skill.Params != "" || skillReq.Skill.Contract != "" {
		t.Errorf("validator hydrated the skill request: %+v", skillReq.Skill)
	}

	if err := v.ValidateUpdateSkillRequest(ctx, &v1.UpdateSkillRequest{
		Skill: &v1.Skill{Name: "sk", Mode: "tool"},
	}); err == nil {
		t.Fatal("an explicit tool mode without an entrypoint was accepted")
	}
}

func TestTestModelValidatorLeavesHydrationToBiz(t *testing.T) {
	v := New(fakeResources{models: fakeModelStore{row: &model.ModelM{
		Provider: "openai", BaseURL: "https://api.example", Model: "gpt-x",
	}}})

	rq := &v1.TestModelRequest{Name: "m", Model: &v1.Model{}}
	if err := v.ValidateTestModelRequest(context.Background(), rq); err != nil {
		t.Fatalf("ValidateTestModelRequest() error = %v", err)
	}

	if rq.Model.Provider != "" || rq.Model.BaseUrl != "" || rq.Model.Model != "" {
		t.Errorf("validator hydrated the test request: %+v", rq.Model)
	}
}

func TestNewPathAndRuntimeValidators(t *testing.T) {
	ctx := context.Background()
	v := New(nil)

	shareDefault := &CreateSessionShareRequest{ID: "s"}
	shareDefault.Default()
	if shareDefault.TTLSeconds == nil || *shareDefault.TTLSeconds != defaultShareTTLSeconds {
		t.Fatalf("share default = %v, want %d", shareDefault.TTLSeconds, defaultShareTTLSeconds)
	}

	zero := 0

	tests := []struct {
		name    string
		run     func() error
		wantErr bool
	}{
		{"fork requires id", func() error {
			return v.ValidateForkSessionRequest(ctx, &v1.ForkSessionRequest{})
		}, true},
		{"fork rejects negative sequence", func() error {
			return v.ValidateForkSessionRequest(ctx, &v1.ForkSessionRequest{Id: "s", UpToSeq: -1})
		}, true},
		{"fork trims name", func() error {
			rq := &v1.ForkSessionRequest{Id: "s", Name: "  n  "}
			if err := v.ValidateForkSessionRequest(ctx, rq); err != nil {
				return err
			}
			if rq.Name != "n" {
				t.Errorf("name = %q, want n", rq.Name)
			}
			return nil
		}, false},
		{"get run requires id", func() error {
			return v.ValidateGetRunRequest(ctx, &v1.GetRunRequest{})
		}, true},
		{"run events rejects negative after", func() error {
			return v.ValidateListRunEventRequest(ctx, &ListRunEventRequest{ID: "r", After: -1})
		}, true},
		{"share rejects explicit zero", func() error {
			return v.ValidateCreateSessionShareRequest(ctx, &CreateSessionShareRequest{ID: "s", TTLSeconds: &zero})
		}, true},
		{"share with default is accepted", func() error {
			return v.ValidateCreateSessionShareRequest(ctx, shareDefault)
		}, false},
		{"sandbox get requires id", func() error {
			return v.ValidateGetSandboxRequest(ctx, &v1.GetSandboxRequest{})
		}, true},
		{"sandbox exec requires command", func() error {
			return v.ValidateExecSandboxRequest(ctx, &v1.ExecSandboxRequest{Id: "s"})
		}, true},
		{"sandbox read requires path", func() error {
			return v.ValidateReadSandboxFileRequest(ctx, &v1.ReadSandboxFileRequest{Id: "s"})
		}, true},
		{"approve command requires approval id", func() error {
			return v.ValidateApproveCustomCommandRequest(ctx, &v1.ApproveCustomCommandRequest{})
		}, true},
		{"audit rejects negative offset", func() error {
			return v.ValidateListCustomCommandAuditRequest(ctx, &v1.ListCustomCommandAuditRequest{Offset: -1})
		}, true},
		{"trigger requires name", func() error {
			return v.ValidateTriggerScheduledRunRequest(ctx, &v1.TriggerScheduledRunRequest{})
		}, true},
		{"cancel requires execution id", func() error {
			return v.ValidateCancelScheduledExecutionRequest(ctx, &v1.CancelScheduledExecutionRequest{Name: "s"})
		}, true},
		{"preview requires a selection", func() error {
			return v.ValidatePreviewAgentBindingRequest(ctx, &v1.PreviewAgentBindingRequest{})
		}, true},
		{"preview accepts a named agent", func() error {
			return v.ValidatePreviewAgentBindingRequest(ctx, &v1.PreviewAgentBindingRequest{
				Spec: agentv1alpha1.AgentBindingSpec{Agents: []string{"helper"}},
			})
		}, false},
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
