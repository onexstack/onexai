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

package authz

import (
	"context"
	"errors"
	"testing"
)

func TestAuthorizeAgentNamesReturnsTheFirstDeny(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionDeny),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	err := AuthorizeAgentNames(ctx, c, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"some-agent"})
	var denied *ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected *ErrAgentAccessDenied, got %v", err)
	}
	if denied.AgentName != "some-agent" {
		t.Fatalf("got AgentName=%q, want %q", denied.AgentName, "some-agent")
	}
}

func TestAuthorizeAgentNamesSkipsEmptyNamesAndAllows(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionAllow),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	if err := AuthorizeAgentNames(ctx, c, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"", "some-agent"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthorizeAgentNamesAllowsWhenAuthorizationIsDisabled(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionDeny),
		staticAuthorizer("resource", DecisionDeny),
		false, // enabled=false
	)

	if err := AuthorizeAgentNames(ctx, c, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"some-agent"}); err != nil {
		t.Fatalf("unexpected error when authz is disabled: %v", err)
	}
}
