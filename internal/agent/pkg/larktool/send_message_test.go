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

package larktool

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// TestRequireSRE pins the tool's only access control. It is the whole reason
// this tool is safe to hand a model, so it is tested directly rather than only
// through a send that would need a live client.
func TestRequireTrustedCaller(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		wantErr string
	}{
		{
			name: "a service account is allowed",
			ctx:  authz.ContextWithSubject(context.Background(), authz.SubjectFromService("cron")),
		},
		{
			name:    "a user caller is refused",
			ctx:     authz.ContextWithSubject(context.Background(), authz.SubjectFromUser("u1", "Go Kong")),
			wantErr: "service account",
		},
		{
			name:    "a caller with no identity is refused",
			ctx:     context.Background(),
			wantErr: "caller identity",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireTrustedCaller(tc.ctx)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the call to be allowed, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolveTarget_RejectsWhatItCannotRoute(t *testing.T) {
	tool := &sendMessageTool{}

	cases := []string{"", "   ", "Toka Wu", "toka.wu", "chat123", "部门群"}
	for _, target := range cases {
		if _, _, err := tool.resolveTarget(context.Background(), target); err == nil {
			t.Errorf("resolveTarget(%q) was accepted; a display name or free text is not routable", target)
		}
	}
}

func TestResolveTarget_AcceptsExplicitIDs(t *testing.T) {
	tool := &sendMessageTool{}
	cases := []struct {
		target   string
		wantID   string
		wantType string
	}{
		{target: "oc_abc123", wantID: "oc_abc123", wantType: receiveIDTypeChatID},
		{target: "ou_def456", wantID: "ou_def456", wantType: receiveIDTypeOpenID},
		{target: "  oc_padded  ", wantID: "oc_padded", wantType: receiveIDTypeChatID},
	}
	for _, tc := range cases {
		id, typ, err := tool.resolveTarget(context.Background(), tc.target)
		if err != nil {
			t.Errorf("resolveTarget(%q): %v", tc.target, err)
			continue
		}
		if id != tc.wantID || typ != tc.wantType {
			t.Errorf("resolveTarget(%q) = (%q, %q), want (%q, %q)", tc.target, id, typ, tc.wantID, tc.wantType)
		}
	}
}

func TestInfo_AdvertisesTheCopyInSemantics(t *testing.T) {
	tool := &sendMessageTool{}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolName {
		t.Errorf("tool name = %q, want %q", info.Name, ToolName)
	}
	// The description has to say it is a copy rather than a redirect: a model
	// that reads it as redirection stops answering the original conversation.
	if !strings.Contains(info.Desc, "抄送") {
		t.Error("the description must say the message is an additional copy")
	}
	if info.ParamsOneOf == nil {
		t.Fatal("the tool must advertise parameters")
	}
}

func TestNewSendMessage_NoClientIsNil(t *testing.T) {
	if tool := NewSendMessage(nil); tool != nil {
		t.Error("a tool with no client cannot send, so it must not be offered")
	}
}
