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

package mcpclient

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/contextx"
	"github.com/onexstack/onexai/pkg/known"
)

// TestCallerHeaderCarriesTheIdentity pins the client half of the ownership
// contract. A tool on the other side scopes its queries by whatever this header
// says, so it has to carry the identity of the human whose turn is running and
// nothing at all when there is none — an empty identity is what makes the tool
// refuse, and a wrong one silently acts as the wrong user.
func TestCallerHeaderCarriesTheIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		userID string
		want   string
	}{
		{name: "identity is forwarded", userID: "ou_abc123", want: "ou_abc123"},
		{name: "no identity means no header", userID: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.userID != "" {
				ctx = contextx.WithUserID(ctx, tc.userID)
			}

			headers := callerHeader(ctx)
			if tc.want == "" {
				if headers != nil {
					t.Fatalf("headers = %v, want nil so no identity header is sent", headers)
				}
				return
			}
			if got := headers[string(known.XUserID)]; got != tc.want {
				t.Errorf("header %q = %q, want %q", known.XUserID, got, tc.want)
			}
		})
	}
}

// TestCallerHeaderIsPerRequest is the reason the header is a function rather
// than an option value: connections are cached by server name, so a header
// baked in when the client was built would pin every later conversation to
// whichever identity happened to open that connection.
func TestCallerHeaderIsPerRequest(t *testing.T) {
	first := callerHeader(contextx.WithUserID(context.Background(), "ou_first"))
	second := callerHeader(contextx.WithUserID(context.Background(), "ou_second"))

	if first[string(known.XUserID)] == second[string(known.XUserID)] {
		t.Fatalf("both requests carried %q; the header is not read per request",
			first[string(known.XUserID)])
	}
}

// TestCallerHeaderCarriesTheAgents pins the other half of the header contract:
// the agents a run selected travel with it, so a server that scopes its tools per
// agent can answer with the right set. It rides alongside the identity on a call,
// and alone on a discovery, which carries no human.
func TestCallerHeaderCarriesTheAgents(t *testing.T) {
	ctx := contextx.WithUserID(context.Background(), "ou_abc")
	ctx = contextx.WithAgentNames(ctx, []string{"duty-agent", "ops-agent"})

	headers := callerHeader(ctx)
	if got := headers[string(known.XUserID)]; got != "ou_abc" {
		t.Errorf("x-user-id = %q, want ou_abc", got)
	}
	if got := headers[string(known.XAgentNames)]; got != "duty-agent,ops-agent" {
		t.Errorf("x-agent-names = %q, want duty-agent,ops-agent", got)
	}

	// Agents alone still produce a header: a discovery carries them and no identity.
	agentsOnly := callerHeader(contextx.WithAgentNames(context.Background(), []string{"duty-agent"}))
	if got := agentsOnly[string(known.XAgentNames)]; got != "duty-agent" {
		t.Errorf("x-agent-names = %q, want duty-agent", got)
	}
	if _, ok := agentsOnly[string(known.XUserID)]; ok {
		t.Error("an agent-only context must not invent a user id")
	}
}
