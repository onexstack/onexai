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

package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/i18n"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// doctorReply is a Responder that keeps what /doctor reported, so a test can read
// the report as the user would.
type doctorReply struct {
	mu   sync.Mutex
	text string
}

func (r *doctorReply) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text += string(p)
	return len(p), nil
}

func (r *doctorReply) SendText(_ context.Context, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text += content
	return nil
}

func (r *doctorReply) SendCard(context.Context, string, map[string]any) error { return nil }
func (r *doctorReply) SendMarkdownCard(context.Context, string, string, string) error {
	return nil
}
func (r *doctorReply) SendInteractiveCard(context.Context, any) error { return nil }

func (r *doctorReply) SendResult(_ context.Context, res *runtime.CommandResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text = res.PlainText
	return nil
}

func (r *doctorReply) report() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text
}

// privateCaller is the caller /doctor answers.
func privateCaller() *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		IsAdmin:  true,
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: "ou_me"},
	}
}

// doctorFixture is a deployment with the CLI backend on, which is the
// configuration /doctor has the most to say about.
func doctorFixture(t *testing.T, admins []string) *runtime.StaticRuntime {
	t.Helper()

	llm := &stubAgent{name: agent.BackendLLM}
	cli := &stubAgent{name: agent.BackendCLI}
	admin := make(map[string]struct{}, len(admins))
	for _, id := range admins {
		admin[id] = struct{}{}
	}
	return &runtime.StaticRuntime{
		Sessions: session.NewManager(context.Background(), session.Options{DataDir: t.TempDir()}),
		Agents:   agent.NewRegistry(agent.BackendLLM, llm, cli),
		Roles:    roleset.NewStatic(map[string]struct{}{"ou_sre_secret": {}}, admin),
		Streaming: stream.Config{
			Interval:      1500 * time.Millisecond,
			MinDeltaChars: 30,
			MaxChars:      2000,
		},
		StreamingEnabled: true,
	}
}

// stubAgent satisfies agent.Agent: /doctor only asks which backends exist.
type stubAgent struct{ name string }

func (s *stubAgent) Name() string { return s.name }
func (s *stubAgent) Session(context.Context, string) (agent.Session, error) {
	return nil, agent.ErrUnknownBackend
}
func (s *stubAgent) CloseSession(context.Context, string) error { return nil }
func (s *stubAgent) Close() error                               { return nil }

// TestDoctorRefusesOutsideAPrivateChat pins the surface restriction: the report
// describes the host and the deployment, so a group chat is refused rather than
// served a shortened version.
func TestDoctorRefusesOutsideAPrivateChat(t *testing.T) {
	sr := doctorFixture(t, []string{"ou_admin"})
	reply := &doctorReply{}

	group := &runtime.DynamicRuntime{
		IsAdmin:  true,
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_group"},
	}
	err := runDoctor(context.Background(), sr, group, reply)
	if err == nil {
		t.Fatal("runDoctor answered a group chat")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("runDoctor() = %v, want a user-facing error", err)
	}
	if reply.report() != "" {
		t.Errorf("reply = %q, want nothing sent", reply.report())
	}
}

// TestDoctorReportsWhatIsConfigured pins the body of the report: the things an
// operator runs this command to find out are all present, and the two findings
// that matter for a deployment with the CLI backend on are raised — nothing
// limits which chats may reach it, and agent is not configured.
func TestDoctorReportsWhatIsConfigured(t *testing.T) {
	sr := doctorFixture(t, []string{"ou_admin"})
	reply := &doctorReply{}

	if err := runDoctor(context.Background(), sr, privateCaller(), reply); err != nil {
		t.Fatalf("runDoctor() = %v", err)
	}
	report := reply.report()

	for _, want := range []string{
		agent.BackendCLI,          // the enabled backend is named
		agent.BackendLLM,          // and so is the default
		sr.Sessions.Directory(),   // where snapshots live
		"oai-agent-apiserver：未配置", // the missing dependency
		"来源不限",                    // the inbound allow-list posture
		"cli 后端已启用",               // the host-affecting backend is called out
		"需要注意",                    // and there are findings to flag
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not mention %q:\n%s", want, report)
		}
	}
}

// TestDoctorReportsRolesAsCounts pins the privacy rule: the report says how many
// operators are configured, never who they are. A list of ids in a chat log
// outlives the question that produced it, and this command is a health report.
func TestDoctorReportsRolesAsCounts(t *testing.T) {
	sr := doctorFixture(t, []string{"ou_admin"})
	reply := &doctorReply{}

	if err := runDoctor(context.Background(), sr, privateCaller(), reply); err != nil {
		t.Fatalf("runDoctor() = %v", err)
	}
	report := reply.report()

	if !strings.Contains(report, "管理员 2 人") {
		t.Errorf("report does not count the admins:\n%s", report)
	}
	for _, id := range []string{"ou_sre_secret", "ou_admin"} {
		if strings.Contains(report, id) {
			t.Errorf("report leaks the configured id %s:\n%s", id, report)
		}
	}
}

// TestDoctorFlagsAnEmptyAdminList pins the finding that is easiest to miss: with
// nobody in the admin list, the commands that switch backends are unreachable —
// including the ones that would turn the CLI backend back off from inside a
// conversation.
func TestDoctorFlagsAnEmptyAdminList(t *testing.T) {
	sr := doctorFixture(t, nil)
	sr.Roles = roleset.NewStatic(nil, nil)

	reply := &doctorReply{}
	if err := runDoctor(context.Background(), sr, privateCaller(), reply); err != nil {
		t.Fatalf("runDoctor() = %v", err)
	}

	if !strings.Contains(reply.report(), "管理员名单为空") {
		t.Errorf("report does not flag the empty admin list:\n%s", reply.report())
	}
}

// TestDoctorAnswersInTheConfiguredLanguage is the end-to-end half of the i18n
// work: the catalog existing is worth nothing if the runtime's language never
// reaches a reply. It also pins the two things the language line is for — a
// deployment can see which language is in effect, and an untranslated string is
// sent as written rather than dropped.
func TestDoctorAnswersInTheConfiguredLanguage(t *testing.T) {
	sr := doctorFixture(t, []string{"ou_admin"})
	sr.Language = i18n.EN
	reply := &doctorReply{}

	if err := runDoctor(context.Background(), sr, privateCaller(), reply); err != nil {
		t.Fatalf("runDoctor() = %v", err)
	}
	report := reply.report()

	for _, want := range []string{
		"**Service self-check**",
		"- Language: en",
		"oai-agent-apiserver: not configured", // a translated dependency line
		"- Authorisation:",                    // and a translated count
		"Needs attention",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("English report does not contain %q:\n%s", want, report)
		}
	}
	// The source strings must be gone, not merely accompanied: a report that
	// carried both would mean the callers were formatting around Translate.
	for _, unwanted := range []string{"**服务自检**", "oai-agent-apiserver：未配置", "需要注意"} {
		if strings.Contains(report, unwanted) {
			t.Errorf("English report still contains the source string %q:\n%s", unwanted, report)
		}
	}
}

// TestDoctorNamesTheSourceLanguage pins the other half of the language line: a
// deployment answering in Chinese — which is every deployment that has not
// configured this — is told that untranslated strings are sent as written, so a
// string still in Chinese after switching to English reads as missing coverage
// rather than as a setting that did not take.
func TestDoctorNamesTheSourceLanguage(t *testing.T) {
	sr := doctorFixture(t, []string{"ou_admin"})
	reply := &doctorReply{}

	if err := runDoctor(context.Background(), sr, privateCaller(), reply); err != nil {
		t.Fatalf("runDoctor() = %v", err)
	}

	if !strings.Contains(reply.report(), "- 语言：zh") {
		t.Errorf("report does not name the language in effect:\n%s", reply.report())
	}
}
