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

package v1

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRequestDefaultsFillGeneratedValues(t *testing.T) {
	completion := &ChatCompletionRequest{}
	completion.Default()
	if !strings.HasPrefix(completion.GetSessionID(), "thread_") {
		t.Errorf("sessionID = %q, want a thread_ prefix", completion.GetSessionID())
	}

	explicit := &ChatCompletionRequest{SessionID: "thread-keep"}
	explicit.Default()
	if explicit.GetSessionID() != "thread-keep" {
		t.Errorf("explicit sessionID was overwritten: %q", explicit.GetSessionID())
	}

	ask := &AgentAskRequest{}
	ask.Default()
	if _, err := uuid.Parse(ask.GetRunId()); err != nil {
		t.Errorf("runId = %q is not a UUID: %v", ask.GetRunId(), err)
	}

	explicitRun := &AgentAskRequest{RunId: "run-keep"}
	explicitRun.Default()
	if explicitRun.GetRunId() != "run-keep" {
		t.Errorf("explicit runId was overwritten: %q", explicitRun.GetRunId())
	}

	invoke := &InvokeCustomCommandRequest{}
	invoke.Default()
	if _, err := uuid.Parse(invoke.GetUid()); err != nil {
		t.Errorf("uid = %q is not a UUID: %v", invoke.GetUid(), err)
	}

	test := &TestModelRequest{Model: &Model{Name: "inline-model"}}
	test.Default()
	if test.GetName() != "inline-model" {
		t.Errorf("name = %q, want inline-model", test.GetName())
	}
	if test.GetModel() == nil {
		t.Fatal("inline model was not installed")
	}
}

func TestGeneratedStaticDefaults(t *testing.T) {
	asset := &CreateAssetRequest{}
	asset.Default()
	if asset.GetKind() != "image" || asset.GetSource() != "upload" {
		t.Errorf("asset defaults = %q/%q, want image/upload", asset.GetKind(), asset.GetSource())
	}

	sandbox := &CreateSandboxRequest{}
	sandbox.Default()
	if sandbox.GetOwner() != "service" {
		t.Errorf("sandbox owner = %q, want service", sandbox.GetOwner())
	}
}
