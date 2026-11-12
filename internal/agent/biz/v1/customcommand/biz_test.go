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

package customcommand

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/customcmd"
	"github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestInvokeRendersAPromptCommand(t *testing.T) {
	command := promptCommand("hello", "你好，{{ .Params.name }}！")
	agent := &model.AgentM{Name: "helper", CustomCommands: model.StringList{"hello"}}
	audit := &captureAudit{}

	biz := New(Deps{Resources: fakeStore{command: command, agent: agent}, Audit: audit})
	resp, err := biz.Invoke(context.Background(), "hello", invokeRequest("helper", map[string]any{"name": "世界"}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.GetStatus().GetPhase() != "Succeeded" {
		t.Fatalf("phase = %q (%s)", resp.GetStatus().GetPhase(), resp.GetStatus().GetMessage())
	}
	if got := resp.GetStatus().GetPrompt().GetPrompt(); got != "你好，世界！" {
		t.Errorf("prompt = %q", got)
	}
	if len(audit.rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(audit.rows))
	}
	row := audit.rows[0]
	if row.Command != "hello" || row.Agent != "helper" || row.Phase != "Succeeded" || row.Kind != "prompt" {
		t.Errorf("audit row = %+v", row)
	}
}

// captureAudit records the rows an invocation writes.
type captureAudit struct {
	rows []*model.CustomCommandAuditM
}

func (a *captureAudit) Write(_ context.Context, row *model.CustomCommandAuditM) error {
	a.rows = append(a.rows, row)
	return nil
}

func (a *captureAudit) List(
	_ context.Context, _ AuditQuery,
) (int64, []*model.CustomCommandAuditM, error) {
	return int64(len(a.rows)), a.rows, nil
}

// captureApprovals is an in-memory ApprovalStore.
type captureApprovals struct {
	rows []*model.CustomCommandApprovalM
	// updateErr makes every Update fail, so a test can prove the decision is
	// reported rather than silently dropped.
	updateErr error
}

func (a *captureApprovals) Create(_ context.Context, row *model.CustomCommandApprovalM) error {
	a.rows = append(a.rows, row)
	return nil
}

func (a *captureApprovals) Get(
	_ context.Context, approvalID string,
) (*model.CustomCommandApprovalM, error) {
	for _, row := range a.rows {
		if row.ApprovalID == approvalID {
			return row, nil
		}
	}
	return nil, ErrApprovalNotFound
}

func (a *captureApprovals) Update(context.Context, *model.CustomCommandApprovalM) error {
	return a.updateErr
}

func TestApprovalGatedToolIsParkedAndDenied(t *testing.T) {
	command := &model.CustomCommandM{Name: "delete"}
	if err := command.SetSpec(&model.CustomCommandSpecM{
		Type: "tool",
		Tool: &model.ToolCommandM{
			MCPServer:         model.ReferenceM{Name: "k8s"},
			Tool:              "delete_pod",
			ArgumentsTemplate: `{"pod":"{{ .Params.pod }}"}`,
		},
		Parameters: []model.CommandParameterM{{Name: "pod", Type: "string", Required: true}},
	}); err != nil {
		t.Fatalf("SetSpec: %v", err)
	}
	agent := &model.AgentM{
		Name:           "helper",
		CustomCommands: model.StringList{"delete"},
		MCPs:           model.StringList{"k8s"},
	}
	approvals := &captureApprovals{}
	biz := New(Deps{
		Resources:       fakeStore{command: command, agent: agent, mcp: &model.MCPM{Name: "k8s"}},
		RequireApproval: []string{"delete_*"},
		Approvals:       approvals,
	})

	req := invokeRequest("helper", map[string]any{"pod": "p1"})
	req.User = &v1.UserInfo{Username: "go.kong"}
	resp, err := biz.Invoke(context.Background(), "delete", req)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	status := resp.GetStatus()
	if status.GetPhase() != "PendingApproval" {
		t.Fatalf("phase = %q (%s)", status.GetPhase(), status.GetMessage())
	}
	if status.GetApproval().GetApprovalId() == "" {
		t.Fatalf("no approval id in %+v", status)
	}
	if len(approvals.rows) != 1 {
		t.Fatalf("approval rows = %d, want 1", len(approvals.rows))
	}
	if approvals.rows[0].Arguments != `{"pod":"p1"}` {
		t.Errorf("stored arguments = %q", approvals.rows[0].Arguments)
	}

	deny, err := biz.Approve(context.Background(), "delete", &v1.ApproveCustomCommandRequest{
		ApprovalId: status.GetApproval().GetApprovalId(),
		Decision:   "deny",
		Agent:      "helper",
		User:       &v1.UserInfo{Username: "go.kong"},
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if deny.GetStatus().GetReason() != "ApprovalDenied" {
		t.Errorf("reason = %q, want ApprovalDenied", deny.GetStatus().GetReason())
	}
	if approvals.rows[0].Phase != "denied" {
		t.Errorf("row phase = %q, want denied", approvals.rows[0].Phase)
	}
}

// A decision the store refused must surface as an error. Reporting "denied"
// while the row stays pending lets the next click run the tool.
func TestApproveReportsADecisionItCannotStore(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		row      *model.CustomCommandApprovalM
		decision string
	}{
		{
			name: "deny",
			row: &model.CustomCommandApprovalM{
				ApprovalID: "a1", Command: "delete", Agent: "helper", UserID: "go.kong",
				Phase: approvalStatusPending, ExpiresAt: now.Add(time.Minute).UnixMilli(),
			},
			decision: "deny",
		},
		{
			name: "expired",
			row: &model.CustomCommandApprovalM{
				ApprovalID: "a2", Command: "delete", Agent: "helper", UserID: "go.kong",
				Phase: approvalStatusPending, ExpiresAt: now.Add(-time.Minute).UnixMilli(),
			},
			decision: "allow",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approvals := &captureApprovals{
				rows:      []*model.CustomCommandApprovalM{tc.row},
				updateErr: errors.New("store is down"),
			}
			biz := New(Deps{Resources: fakeStore{}, Approvals: approvals})

			_, err := biz.Approve(context.Background(), "delete", &v1.ApproveCustomCommandRequest{
				ApprovalId: tc.row.ApprovalID,
				Decision:   tc.decision,
				Agent:      "helper",
				User:       &v1.UserInfo{Username: "go.kong"},
			})
			if err == nil {
				t.Fatal("an approval decision that could not be stored must be reported")
			}
		})
	}
}

func TestInvokeDeniesAnUnboundCommand(t *testing.T) {
	command := promptCommand("hello", "x")
	agent := &model.AgentM{Name: "helper"}

	biz := New(Deps{Resources: fakeStore{command: command, agent: agent}})
	resp, err := biz.Invoke(context.Background(), "hello", invokeRequest("helper", map[string]any{"name": "世界"}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.GetStatus().GetPhase() != "Denied" {
		t.Fatalf("phase = %q, want Denied", resp.GetStatus().GetPhase())
	}
	if resp.GetStatus().GetReason() != "NotBound" {
		t.Errorf("reason = %q, want NotBound", resp.GetStatus().GetReason())
	}
}

func TestInvokeDeniesADisabledAgent(t *testing.T) {
	command := promptCommand("hello", "x")
	agent := &model.AgentM{
		Name:           "helper",
		Status:         model.StatusDisabled,
		CustomCommands: model.StringList{"hello"},
	}

	biz := New(Deps{Resources: fakeStore{command: command, agent: agent}})
	resp, err := biz.Invoke(context.Background(), "hello", invokeRequest("helper", map[string]any{"name": "世界"}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.GetStatus().GetReason() != "AgentDisabled" {
		t.Errorf("reason = %q, want AgentDisabled", resp.GetStatus().GetReason())
	}
}

func TestInvokeRejectsMissingParametersWithUsage(t *testing.T) {
	command := promptCommand("hello", "x")
	agent := &model.AgentM{Name: "helper", CustomCommands: model.StringList{"hello"}}

	biz := New(Deps{Resources: fakeStore{command: command, agent: agent}})
	_, err := biz.Invoke(context.Background(), "hello", invokeRequest("helper", map[string]any{}))
	if err == nil {
		t.Fatalf("Invoke accepted a missing parameter")
	}
	if !strings.Contains(err.Error(), "用法:") {
		t.Errorf("error = %q, want a usage line", err)
	}
}

func TestInvokeProxiesAnHTTPCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":` + string(body) + `}`))
	}))
	defer srv.Close()

	command := &model.CustomCommandM{Name: "echo", Type: "http"}
	if err := command.SetSpec(&model.CustomCommandSpecM{
		Type: "http",
		HTTP: &model.HTTPCommandM{
			Method: "POST",
			URL:    srv.URL + "/echo",
			Body:   `{"name":"{{ .Params.name }}"}`,
		},
		Parameters: []model.CommandParameterM{{Name: "name", Type: "string", Required: true}},
	}); err != nil {
		t.Fatalf("SetSpec: %v", err)
	}
	agent := &model.AgentM{Name: "helper", CustomCommands: model.StringList{"echo"}}

	biz := New(Deps{
		Resources: fakeStore{command: command, agent: agent},
		HTTP:      customcmd.NewHTTPExecutor(customcmd.HTTPConfig{AllowPrivateHosts: true, AllowPlainHTTP: true}),
	})
	resp, err := biz.Invoke(context.Background(), "echo", invokeRequest("helper", map[string]any{"name": "世界"}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	status := resp.GetStatus()
	if status.GetPhase() != "Succeeded" {
		t.Fatalf("phase = %q (%s)", status.GetPhase(), status.GetMessage())
	}
	if status.GetHttp().GetStatus() != http.StatusOK {
		t.Errorf("status = %d, want 200", status.GetHttp().GetStatus())
	}
	if !strings.Contains(status.GetHttp().GetRendered(), "世界") {
		t.Errorf("rendered = %q, want the echoed name", status.GetHttp().GetRendered())
	}
}

func promptCommand(name, template string) *model.CustomCommandM {
	row := &model.CustomCommandM{Name: name, Type: "prompt"}
	_ = row.SetSpec(&model.CustomCommandSpecM{
		Type:       "prompt",
		Prompt:     &model.PromptCommandM{Template: template},
		Parameters: []model.CommandParameterM{{Name: "name", Type: "string", Required: true}},
	})
	return row
}

func invokeRequest(agent string, params map[string]any) *v1.InvokeCustomCommandRequest {
	value, _ := structpb.NewStruct(params)
	return &v1.InvokeCustomCommandRequest{Agent: agent, Params: value}
}

// ──────────────────────────────────────────────────────────────────────────────
// Store fakes
// ──────────────────────────────────────────────────────────────────────────────

type fakeStore struct {
	store.AIStore
	command *model.CustomCommandM
	agent   *model.AgentM
	mcp     *model.MCPM
}

func (f fakeStore) CustomCommands() store.CustomCommandStore { return fakeCommands{row: f.command} }
func (f fakeStore) Agents() store.AgentStore                 { return fakeAgents{row: f.agent} }
func (f fakeStore) MCPs() store.MCPStore                     { return fakeMCPs{row: f.mcp} }
func (f fakeStore) Secrets() store.SecretStore               { return fakeSecrets{} }

type fakeCommands struct{ row *model.CustomCommandM }

func (f fakeCommands) Create(context.Context, *model.CustomCommandM) error { return nil }
func (f fakeCommands) Update(context.Context, *model.CustomCommandM) error { return nil }
func (f fakeCommands) Delete(context.Context, *where.Options) error        { return nil }
func (f fakeCommands) Get(context.Context, *where.Options) (*model.CustomCommandM, error) {
	return f.row, nil
}
func (f fakeCommands) List(context.Context, *where.Options) (int64, []*model.CustomCommandM, error) {
	return 1, []*model.CustomCommandM{f.row}, nil
}

type fakeAgents struct{ row *model.AgentM }

func (f fakeAgents) Create(context.Context, *model.AgentM) error  { return nil }
func (f fakeAgents) Update(context.Context, *model.AgentM) error  { return nil }
func (f fakeAgents) Delete(context.Context, *where.Options) error { return nil }
func (f fakeAgents) Get(context.Context, *where.Options) (*model.AgentM, error) {
	return f.row, nil
}
func (f fakeAgents) List(context.Context, *where.Options) (int64, []*model.AgentM, error) {
	return 1, []*model.AgentM{f.row}, nil
}
func (fakeAgents) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}

type fakeMCPs struct{ row *model.MCPM }

func (fakeMCPs) Create(context.Context, *model.MCPM) error    { return nil }
func (fakeMCPs) Update(context.Context, *model.MCPM) error    { return nil }
func (fakeMCPs) Delete(context.Context, *where.Options) error { return nil }
func (f fakeMCPs) Get(context.Context, *where.Options) (*model.MCPM, error) {
	return f.row, nil
}
func (fakeMCPs) List(context.Context, *where.Options) (int64, []*model.MCPM, error) {
	return 0, nil, nil
}

type fakeSecrets struct{}

func (fakeSecrets) Create(context.Context, *model.SecretM) error                { return nil }
func (fakeSecrets) Update(context.Context, *model.SecretM) error                { return nil }
func (fakeSecrets) Delete(context.Context, *where.Options) error                { return nil }
func (fakeSecrets) Get(context.Context, *where.Options) (*model.SecretM, error) { return nil, nil }
func (fakeSecrets) List(context.Context, *where.Options) (int64, []*model.SecretM, error) {
	return 0, nil, nil
}

// A cross-namespace entry in the agent's resolved set is a reference this store
// cannot resolve, so it grants nothing here. Matching its last segment would
// let a "other-ns/delete" grant be redeemed by a local command of the same name,
// which the menu (ContextCommands) never offered.
func TestInvokeDoesNotRedeemACrossNamespaceEntry(t *testing.T) {
	command := promptCommand("delete", "x")
	agent := &model.AgentM{Name: "helper", CustomCommands: model.StringList{"other/delete"}}
	biz := New(Deps{Resources: fakeStore{command: command, agent: agent}})

	resp, err := biz.Invoke(context.Background(), "delete", invokeRequest("helper", nil))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	status := resp.GetStatus()
	if status.GetPhase() != "Denied" {
		t.Fatalf("phase = %q, want Denied", status.GetPhase())
	}
	if status.GetReason() != "NotBound" {
		t.Errorf("reason = %q, want NotBound", status.GetReason())
	}
}

// A large response body is bounded before it reaches the proto message. Carried
// whole it risks the gRPC message limit, which fails the entire invocation
// rather than shortening one field.
func TestBoundedValueTruncatesALargeBody(t *testing.T) {
	small := boundedValue([]byte(`{"ok":true}`))
	if small.GetStructValue() == nil {
		t.Errorf("a small JSON body should stay structured, got %v", small)
	}

	large := boundedValue([]byte(strings.Repeat("x", customcmd.ResultLimit+1)))
	text := large.GetStringValue()
	if text == "" {
		t.Fatalf("a large body should become a string, got %v", large)
	}
	if !strings.HasSuffix(text, "… (truncated)") {
		t.Errorf("a large body should carry the truncation marker, got %q", text[len(text)-20:])
	}
}
