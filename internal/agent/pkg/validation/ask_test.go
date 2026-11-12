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
	"testing"

	"github.com/onexstack/onexstack/pkg/ptr"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// consoleAsk builds the shared request the way a browser sends it: content and
// selection only, no identity or lifecycle fields.
func consoleAsk(text string, agents []string) *v1.AgentAskRequest {
	return &v1.AgentAskRequest{
		Input:     &v1.Input{Text: text},
		Selection: &v1.Selection{Agents: agents},
	}
}

// TestUserAskAllowsHostlessComposition pins the one protocol extension the
// playground needs: on the self-service surface an empty selection is legal only
// when a composition is present. The management surface keeps requiring an
// agent because its contract is "execute as the agent owner".
func TestUserAskAllowsHostlessComposition(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	hostless := consoleAsk("hello", nil)
	hostless.Composition = &v1.AgentComposition{Prompts: []string{"persona"}}
	hostless.SessionNonce = "n1"
	if err := v.ValidateUserAskRequest(ctx, hostless); err != nil {
		t.Fatalf("hostless composition refused: %v", err)
	}

	noComposition := consoleAsk("hello", nil)
	if err := v.ValidateUserAskRequest(ctx, noComposition); err == nil {
		t.Fatal("empty selection without composition must be refused")
	}

	if err := v.ValidateAdminAskRequest(ctx, hostless); err == nil {
		t.Fatal("the management surface must still require an agent")
	}
}

// TestUserAskHostlessRequiresNonce keeps the session bucket well defined: two
// conversations with the same composition must not silently share one.
func TestUserAskHostlessRequiresNonce(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk("hello", nil)
	rq.Composition = &v1.AgentComposition{Prompts: []string{"persona"}}
	if err := v.ValidateUserAskRequest(context.Background(), rq); err == nil {
		t.Fatal("composition without sessionNonce must be refused")
	}
}

// TestUserAskHostlessRejectsNarrowingAndBuiltin preserves the two neighbouring
// meanings: a composition replaces capabilities rather than narrowing them, and
// "default" is the builtin assistant, which has no host to compose against.
func TestUserAskHostlessRejectsNarrowingAndBuiltin(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	both := consoleAsk("hello", nil)
	both.Capabilities = &v1.CapabilitySelection{Skills: []string{"sre"}}
	both.Composition = &v1.AgentComposition{Skills: []string{"sre"}}
	both.SessionNonce = "n1"
	if err := v.ValidateUserAskRequest(ctx, both); err == nil {
		t.Fatal("composition and capabilities together must be refused")
	}

	builtin := consoleAsk("hello", []string{"default"})
	builtin.Composition = &v1.AgentComposition{Prompts: []string{"persona"}}
	builtin.SessionNonce = "n1"
	if err := v.ValidateUserAskRequest(ctx, builtin); err == nil {
		t.Fatal("the builtin assistant must not accept a composition")
	}
}

// TestUserAskTrimsAndNormalizes keeps the values the handler reads identical to
// the ones the run uses.
func TestUserAskTrimsAndNormalizes(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk(" hello ", []string{" helper "})
	rq.Composition = &v1.AgentComposition{Prompts: []string{" persona ", "persona"}}
	rq.SessionNonce = " n1 "
	if err := v.ValidateUserAskRequest(context.Background(), rq); err != nil {
		t.Fatalf("valid request refused: %v", err)
	}

	ask, err := ReadConsoleAsk(rq)
	if err != nil {
		t.Fatalf("ReadConsoleAsk: %v", err)
	}
	if len(ask.Agents) != 1 || ask.Agents[0] != "helper" {
		t.Fatalf("agents = %v, want [helper]", ask.Agents)
	}
	if ask.SessionNonce != "n1" {
		t.Fatalf("nonce = %q, want n1", ask.SessionNonce)
	}
	if got := rq.GetComposition().GetPrompts(); len(got) != 1 || got[0] != "persona" {
		t.Fatalf("prompts = %v, want [persona]", got)
	}
}

// TestUserAskHostlessRequiresQuestion keeps the question required on every
// branch, including the new one.
func TestUserAskHostlessRequiresQuestion(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk("   ", nil)
	rq.Composition = &v1.AgentComposition{Prompts: []string{"persona"}}
	rq.SessionNonce = "n1"
	if err := v.ValidateUserAskRequest(context.Background(), rq); err == nil {
		t.Fatal("empty question must be refused")
	}
}

// TestConsoleAskAcceptsImageRefs pins the wired image path: the console sends
// the same ImageRef list the signed face accepts, and a well-formed reference
// passes validation.
func TestConsoleAskAcceptsImageRefs(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk("what is in this screenshot?", []string{"helper"})
	rq.Input.Images = []*v1.ImageRef{{AssetId: "img_abc"}}
	if err := v.ValidateUserAskRequest(context.Background(), rq); err != nil {
		t.Fatalf("image reference refused: %v", err)
	}
}

// TestConsoleAskRejectsMalformedImageRef refuses the whole request rather than
// answering about fewer images than the caller sent.
func TestConsoleAskRejectsMalformedImageRef(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk("what is in this screenshot?", []string{"helper"})
	rq.Input.Images = []*v1.ImageRef{{}}
	if err := v.ValidateUserAskRequest(context.Background(), rq); err == nil {
		t.Fatal("an image reference naming nothing must be refused")
	}
}

// TestUserAskBuiltinRejectsImages: the builtin assistant is a stateless text
// generation with no instruction or tools; an image has nothing to reach.
func TestUserAskBuiltinRejectsImages(t *testing.T) {
	v := &Validator{}
	rq := consoleAsk("hi", []string{"default"})
	rq.Input.Images = []*v1.ImageRef{{AssetId: "img_abc"}}
	if err := v.ValidateUserAskRequest(context.Background(), rq); err == nil {
		t.Fatal("the builtin assistant must not accept images")
	}
}

// TestConsoleAskRejectsServerOwnedFields is the boundary that replaces the old
// separate request message: a browser that sends a field the server owns is
// refused, not silently ignored.
func TestConsoleAskRejectsServerOwnedFields(t *testing.T) {
	v := &Validator{}
	tests := []struct {
		name   string
		mutate func(*v1.AgentAskRequest)
	}{
		{name: "subject", mutate: func(rq *v1.AgentAskRequest) {
			rq.Subject = &v1.Principal{Id: "someone-else"}
		}},
		{name: "actor", mutate: func(rq *v1.AgentAskRequest) {
			rq.Actor = &v1.Principal{Id: "someone-else"}
		}},
		{name: "sessionId", mutate: func(rq *v1.AgentAskRequest) { rq.SessionId = "shared" }},
		{name: "metering", mutate: func(rq *v1.AgentAskRequest) {
			rq.Metering = &v1.Metering{Mode: v1.Metering_MODE_CALLER}
		}},
		{name: "metadata", mutate: func(rq *v1.AgentAskRequest) {
			rq.Metadata = map[string]string{"tenant": "x"}
		}},
		{name: "options", mutate: func(rq *v1.AgentAskRequest) {
			rq.Options = &v1.ExecutionOptions{}
		}},
		{name: "model", mutate: func(rq *v1.AgentAskRequest) { rq.Model = "gpt-4o" }},
		{name: "invokedSkills", mutate: func(rq *v1.AgentAskRequest) {
			rq.Selection.InvokedSkills = []string{"sre"}
		}},
		{name: "two agents", mutate: func(rq *v1.AgentAskRequest) {
			rq.Selection.Agents = []string{"a", "b"}
		}},
		{name: "temperature", mutate: func(rq *v1.AgentAskRequest) {
			rq.Generation = &v1.GenerationConfig{Temperature: ptr.To(0.5)}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rq := consoleAsk("hi", []string{"helper"})
			tt.mutate(rq)
			if err := v.ValidateUserAskRequest(context.Background(), rq); err == nil {
				t.Fatalf("%s was accepted on the browser surface", tt.name)
			}
		})
	}
}

// TestSignedFaceRejectsConsoleOnlyFields keeps the shared message from silently
// growing behaviour on the signed face: capabilities, composition and
// sessionNonce belong to the browser surfaces only.
func TestSignedFaceRejectsConsoleOnlyFields(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	withCaps := &v1.AgentAskRequest{
		Input:        &v1.Input{Text: "hi"},
		Capabilities: &v1.CapabilitySelection{Skills: []string{"sre"}},
	}
	if err := v.ValidateAgentAskRequest(ctx, withCaps); err == nil {
		t.Fatal("the signed face must refuse capabilities")
	}

	withComposition := &v1.AgentAskRequest{
		Input:       &v1.Input{Text: "hi"},
		Composition: &v1.AgentComposition{Prompts: []string{"persona"}},
	}
	if err := v.ValidateAgentAskRequest(ctx, withComposition); err == nil {
		t.Fatal("the signed face must refuse composition")
	}

	withNonce := &v1.AgentAskRequest{
		Input:        &v1.Input{Text: "hi"},
		SessionNonce: "n1",
	}
	if err := v.ValidateAgentAskRequest(ctx, withNonce); err == nil {
		t.Fatal("the signed face must refuse sessionNonce")
	}
}
