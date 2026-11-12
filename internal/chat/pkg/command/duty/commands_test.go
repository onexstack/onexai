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

package duty

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// withInactiveGroup registers a module the store knows about but that has no
// rotation right now: the state every duty command has to report without
// calling the module "missing".
func (e *testEnv) withInactiveGroup(code, name string) {
	e.dutyStore.RegisterGroup(&v1.OnDutyGroup{GroupId: code, GroupName: name})
}

// unreadableRosterSource loads module metadata while reporting that the rosters
// behind it could not be read. It is the state a duty backend outage leaves in
// the store, and the one that made /oncall answer "当前没有可用的值班模块" (no duty module is currently available) for a
// deployment whose module table was in fact fully populated.
type unreadableRosterSource struct {
	groups []dutystore.Group
	err    error
}

func (s *unreadableRosterSource) Load(context.Context) ([]dutystore.Group, error) {
	return s.groups, nil
}

func (s *unreadableRosterSource) RosterError() error { return s.err }

// withUnreadableRosters replaces the environment's store with one that holds
// the given modules and no roster at all.
func (e *testEnv) withUnreadableRosters(t *testing.T, groups ...dutystore.Group) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.setDutyStore(dutystore.NewWithSource(ctx, &unreadableRosterSource{
		groups: groups,
		err:    errors.New("connection refused"),
	}, time.Hour))
}

func ticketGroup() (*model.TicketM, *model.SessionM) {
	ticket := &model.TicketM{
		TicketID: "tkt-1",
		Title:    "标题",
		Status:   model.TicketStatusProcessing,
		Level:    4,
		GroupID:  "eks",
	}
	session := &model.SessionM{
		SessionID:          "oc_1",
		ChatID:             "oc_1",
		SessionType:        model.SessionTypeTicketGroup,
		AssociatedTicketID: "tkt-1",
	}
	return ticket, session
}

func TestOncallListRendersTable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "--list")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.Markdown == nil || !strings.Contains(res.Markdown.Title, "共找到 1 个值班模块") {
		t.Errorf("Markdown.Title = %+v, want the module count", res.Markdown)
	}
	body := res.Markdown.Content
	for _, want := range []string{"模块ID", "模块名称", "模块描述", "eks", "EKS", "容器平台"} {
		if !strings.Contains(body, want) {
			t.Errorf("list body is missing %q:\n%s", want, body)
		}
	}
}

func TestOncallListEmptyAndBadRegex(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }

	res, err := env.exec(t, build, "--list", "nomatch")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "未找到匹配 'nomatch' 的值班模块") {
		t.Errorf("PlainText = %q", res.PlainText)
	}

	_, err = env.exec(t, build, "--list", "(")
	if err == nil || !strings.Contains(err.Error(), "无效的正则表达式") {
		t.Errorf("bad regex error = %v", err)
	}
}

func TestOncallValidationErrors(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }

	cases := []struct {
		name, want string
		args       []string
	}{
		{"missing title", "请输入工单标题", []string{"eks"}},
		{"unknown module", "值班组 [nope] 不存在", []string{"nope", "x"}},
		{"level out of range", "工单级别必须在 1-4 之间", []string{"-l", "5", "eks", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.exec(t, build, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOncallWithoutModuleSendsPickerCard(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.LarkTemplateID != chat.OncallFlatSelectCardID {
		t.Fatalf("LarkTemplateID = %q, want the flat-select card", res.LarkTemplateID)
	}
	modules, _ := res.LarkVariables["modules"].([]map[string]string)
	if len(modules) != 2 || modules[0]["value"] != "eks" {
		t.Errorf("modules = %+v, want the real module followed by smart-routing", modules)
	}
	// smart-routing is a synthetic option and must always sit last.
	if last := modules[len(modules)-1]; last["value"] != SmartRoutingModuleID {
		t.Errorf("last option = %+v, want %s", last, SmartRoutingModuleID)
	}
	if res.LarkVariables["session_key"] != "oc_1" {
		t.Errorf("session_key = %v, want the receiver id", res.LarkVariables["session_key"])
	}
}

func TestOncallSmartRoutingCreatesTicketForMatchedModule(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "eks"

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) },
		SmartRoutingModuleID, "Pod 一直 Pending")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if len(env.lark.created) != 1 || env.lark.created[0] != "[处理中][P4]Pod 一直 Pending" {
		t.Errorf("created chat titles = %v", env.lark.created)
	}
	if ticket.GroupID != "eks" {
		t.Errorf("ticket group = %q, want the matched module", ticket.GroupID)
	}
	if !strings.Contains(res.Markdown.Content, "工单创建成功") {
		t.Errorf("Markdown = %q", res.Markdown.Content)
	}
	if len(env.llm.reqs) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(env.llm.reqs))
	}
	// One user message carries the whole instruction; a SessionID would leak the
	// routing question into a session.
	if len(env.llm.reqs[0].Messages) != 1 || env.llm.reqs[0].SessionID != "" {
		t.Errorf("request = %+v, want one stateless user message", env.llm.reqs[0])
	}
	// The far side requires an agent, and this call wants the builtin assistant:
	// the sentinel keeps it a routing question with no agent's instruction or
	// tools in the way.
	if got := env.llm.reqs[0].GetAgent(); got != llmv1.NoAgent {
		t.Errorf("agent = %q, want %q", got, llmv1.NoAgent)
	}
}

func TestOncallSmartRoutingRejectsUnknownModule(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "none"

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) },
		SmartRoutingModuleID, "Pod 一直 Pending")
	if err == nil || !strings.Contains(err.Error(), "无法自动匹配值班模块") {
		t.Errorf("error = %v, want the no-match message", err)
	}
	if len(env.lark.created) != 0 {
		t.Errorf("created chat titles = %v, want no ticket", env.lark.created)
	}
}

func TestOncallSmartRoutingCallFailureIsUserError(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.err = errors.New("llm down")

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) },
		SmartRoutingModuleID, "Pod 一直 Pending")
	if err == nil || !strings.Contains(err.Error(), "自动匹配值班模块失败") {
		t.Errorf("error = %v, want the routing-failure message", err)
	}
	if len(env.lark.created) != 0 {
		t.Errorf("created chat titles = %v, want no ticket", env.lark.created)
	}
}

func TestOncallSmartRoutingRequiresTitle(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) },
		SmartRoutingModuleID)
	if err == nil || !strings.Contains(err.Error(), "请输入工单标题") {
		t.Errorf("error = %v, want the title-required message", err)
	}
	if len(env.llm.reqs) != 0 {
		t.Errorf("LLM calls = %d, want none before the title is given", len(env.llm.reqs))
	}
}

// A lone token that names no module is a title, so the backend picks the group.
// "/oncall 咨询下Go开发" (consult on Go development) is the case this exists for: the user wrote a
// description, not a module id.
func TestOncallSingleUnknownTokenSmartRoutes(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.llm.answer = "eks"

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) },
		"咨询下Go开发")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if len(env.llm.reqs) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(env.llm.reqs))
	}
	if len(env.lark.created) != 1 || env.lark.created[0] != "[处理中][P4]咨询下Go开发" {
		t.Errorf("created chat titles = %v", env.lark.created)
	}
	if ticket.GroupID != "eks" {
		t.Errorf("ticket group = %q, want the routed module", ticket.GroupID)
	}
	if !strings.Contains(res.Markdown.Content, "工单创建成功") {
		t.Errorf("Markdown = %q", res.Markdown.Content)
	}
}

// A known module still needs its title: the single-token fallback must not
// swallow "/oncall eks" and invent one.
func TestOncallSingleKnownModuleStillRequiresTitle(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "eks")
	if err == nil || !strings.Contains(err.Error(), "请输入工单标题") {
		t.Errorf("error = %v, want the title-required message", err)
	}
	if len(env.llm.reqs) != 0 {
		t.Errorf("LLM calls = %d, want none for a known module", len(env.llm.reqs))
	}
}

// A registered module with no rotation is still known, so the single-token
// fallback (which keys off GetGroupInfo) must leave it to the no-rotation
// message rather than routing it as a title.
func TestOncallSingleInactiveModuleStaysStrict(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withInactiveGroup("go", "Go 语言")

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "go")
	if err == nil || !strings.Contains(err.Error(), "值班组 [go] 当前没有值班人") {
		t.Errorf("error = %v, want the no-rotation message", err)
	}
	if len(env.llm.reqs) != 0 {
		t.Errorf("LLM calls = %d, want none", len(env.llm.reqs))
	}
}

// Nothing loaded: no name can be judged, so the command says the service is
// unavailable instead of blaming the module the user typed.
func TestOncallEmptyStoreReportsUnavailable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.setDutyStore(dutystore.New())
	build := func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }

	_, err := env.exec(t, build, "咨询下Go开发")
	if err == nil || !strings.Contains(err.Error(), "值班服务暂时不可用") {
		t.Errorf("error = %v, want the unavailable message", err)
	}
	// The picker would offer only the synthetic option, so it is not shown.
	_, err = env.exec(t, build)
	if err == nil || !strings.Contains(err.Error(), "值班服务暂时不可用") {
		t.Errorf("no-arg error = %v, want the unavailable message", err)
	}
	if len(env.llm.reqs) != 0 {
		t.Errorf("LLM calls = %d, want none", len(env.llm.reqs))
	}
}

func TestOncallListEmptyStoreReportsUnavailable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.setDutyStore(dutystore.New())

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "--list")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "值班服务暂时不可用") {
		t.Errorf("PlainText = %q, want the unavailable message", res.PlainText)
	}
}

// An unreadable roster backend is the failure the live deployment hit: the
// store lists its modules, nobody is on call as far as it can tell, and the
// module list is perfectly fine. Every surface that has nothing to act on must
// say the service could not be read rather than blame the module list, which is
// exactly what sent a user looking for modules that were all there.
func TestDutyCommandsWithUnreadableRostersReportUnavailable(t *testing.T) {
	build := func(env *testEnv) func(runtime.Responder) *cobra.Command {
		return func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }
	}

	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, env *testEnv, res *runtime.CommandResult, err error)
	}{
		{
			// The reported symptom: a bare title no module owns, which has to
			// be routed and cannot be.
			name: "smart routing",
			args: []string{"咨询下Go开发"},
			check: func(t *testing.T, env *testEnv, _ *runtime.CommandResult, err error) {
				if err == nil || !strings.Contains(err.Error(), "值班服务暂时不可用") {
					t.Errorf("error = %v, want the unavailable message, not the empty-module one", err)
				}
				if len(env.llm.reqs) != 0 {
					t.Errorf("LLM calls = %d, want none while no module is known to be staffed", len(env.llm.reqs))
				}
			},
		},
		{
			name: "list",
			args: []string{"--list"},
			check: func(t *testing.T, _ *testEnv, res *runtime.CommandResult, err error) {
				if err != nil {
					t.Fatalf("execute = %v", err)
				}
				if !strings.Contains(res.PlainText, "值班服务暂时不可用") {
					t.Errorf("PlainText = %q, want the unavailable message", res.PlainText)
				}
			},
		},
		{
			// The picker would only offer the modules and then refuse them.
			name: "picker",
			args: nil,
			check: func(t *testing.T, _ *testEnv, _ *runtime.CommandResult, err error) {
				if err == nil || !strings.Contains(err.Error(), "值班服务暂时不可用") {
					t.Errorf("error = %v, want the unavailable message", err)
				}
			},
		},
		{
			// The module is registered and has no rotation: not a typo.
			name: "named module",
			args: []string{"go"},
			check: func(t *testing.T, _ *testEnv, _ *runtime.CommandResult, err error) {
				if err == nil || !strings.Contains(err.Error(), "值班服务暂时不可用") {
					t.Errorf("error = %v, want the unavailable message, not the no-rotation one", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticket, session := ticketGroup()
			env := newTestEnv(t, ticket, session)
			env.withUnreadableRosters(t, dutystore.Group{ID: "go", Name: "Go 开发"})

			res, err := env.exec(t, build(env), tt.args...)
			tt.check(t, env, res, err)
		})
	}
}

// The /duty listing answers with the same taxonomy: a store that cannot read
// its rosters has no active group to show, and saying so is not the same as
// saying no group exists.
func TestDutyBareWithUnreadableRostersReportsUnavailable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withUnreadableRosters(t, dutystore.Group{ID: "go", Name: "Go 开发"})

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "值班服务暂时不可用") {
		t.Errorf("PlainText = %q, want the unavailable message", res.PlainText)
	}
}

func TestOncallCreatesTicketGroup(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "eks", "网关异常")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if len(env.lark.created) != 1 || env.lark.created[0] != "[处理中][P4]网关异常" {
		t.Errorf("created chat titles = %v", env.lark.created)
	}
	if !strings.Contains(res.Markdown.Content, "工单创建成功") {
		t.Errorf("Markdown = %q", res.Markdown.Content)
	}
}

func TestDutyListsModules(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.Interactive == nil {
		t.Fatalf("no card was built for /duty: %+v", res)
	}
	if !strings.Contains(res.Markdown.Content, "值班组") || !strings.Contains(res.Markdown.Content, "Lingfei Kong") {
		t.Errorf("Markdown = %q", res.Markdown.Content)
	}
}

func TestDutyUnknownModuleErrors(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) }, "nope")
	if err == nil || !strings.Contains(err.Error(), "值班组 [nope] 不存在") {
		t.Errorf("error = %v, want the not-found message", err)
	}
}

func TestDutyInactiveModuleReportsNoRotation(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withInactiveGroup("go", "Go 语言")

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) }, "go")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "值班组 [go] 当前没有值班人") {
		t.Errorf("PlainText = %q, want the no-rotation message", res.PlainText)
	}
	if strings.Contains(res.PlainText, "不存在") {
		t.Errorf("PlainText = %q, a registered module must not be reported as missing", res.PlainText)
	}
}

// An empty store is a service problem, not an empty rotation: the user must not
// be told to go look at a list that would also come back empty.
func TestDutyBareWithEmptyStoreReportsUnavailable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.setDutyStore(dutystore.New())

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "值班服务暂时不可用") {
		t.Errorf("PlainText = %q, want the unavailable message", res.PlainText)
	}
}

// Modules are loaded but nobody is on call: that is a rotation state, and the
// distinct message is the whole point of telling the two apart.
func TestDutyBareWithInactiveGroupsExplains(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.setDutyStore(dutystore.New()) // no active group: only the rotation-less one
	env.withInactiveGroup("go", "Go 语言")

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "当前没有可用的值班组") {
		t.Errorf("PlainText = %q, want the no-rotation hint", res.PlainText)
	}
}

func TestDutyUnknownWithEmptyStoreReportsUnavailable(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.setDutyStore(dutystore.New())

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) }, "nope")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "值班服务暂时不可用") {
		t.Errorf("PlainText = %q, want the unavailable message", res.PlainText)
	}
	if strings.Contains(res.PlainText, "不存在") {
		t.Errorf("PlainText = %q, an empty store must not be reported as a missing group", res.PlainText)
	}
}

func TestDutyPartialArgsReportTheMisses(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) }, "eks", "nope")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.Markdown.Content, "Lingfei Kong") {
		t.Errorf("the matched group is missing from the card: %q", res.Markdown.Content)
	}
	if !strings.Contains(res.Markdown.Content, "未找到值班组：nope") {
		t.Errorf("the miss note is missing from the card: %q", res.Markdown.Content)
	}
}

func TestOncallInactiveModuleIsNotReportedAsMissing(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withInactiveGroup("go", "Go 语言")

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewOncallCmd(env.sr, out) }, "go", "网关异常")
	if err == nil || !strings.Contains(err.Error(), "值班组 [go] 当前没有值班人") {
		t.Errorf("error = %v, want the no-rotation message", err)
	}
}

func TestHandInactiveModuleIsNotReportedAsMissing(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withInactiveGroup("go", "Go 语言")

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewHandCmd(env.sr, out) }, "go")
	if err == nil || !strings.Contains(err.Error(), "值班组 [go] 当前没有值班人") {
		t.Errorf("error = %v, want the no-rotation message", err)
	}
}

func TestInviteInactiveModuleIsNotReportedAsMissing(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.withInactiveGroup("go", "Go 语言")

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewInviteCmd(env.sr, out) }, "go")
	if err == nil || !strings.Contains(err.Error(), "值班组 [go] 当前没有值班人") {
		t.Errorf("error = %v, want the no-rotation message", err)
	}
}

func TestCloseSyncsChatNameAndSession(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewCloseCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if ticket.Status != model.TicketStatusClosed {
		t.Errorf("ticket status = %q", ticket.Status)
	}
	if session.ClosedAt == nil {
		t.Error("session.ClosedAt was not stamped")
	}
	if got := last(env.lark.chatNames); got != "oc_1=[已关闭][P4]标题" {
		t.Errorf("chat name = %q", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewCloseCmd(env.sr, out) }

	if _, err := env.exec(t, build); err != nil {
		t.Fatalf("first close = %v", err)
	}
	res, err := env.exec(t, build)
	if err != nil {
		t.Fatalf("second close = %v", err)
	}
	if !strings.Contains(res.PlainText, "工单已经关闭") {
		t.Errorf("second close = %q, want the idempotent notice", res.PlainText)
	}
}

func TestSaveAndUnsave(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewSaveCmd(env.sr, out) }

	if _, err := env.exec(t, build); err != nil {
		t.Fatalf("save = %v", err)
	}
	if ticket.Status != model.TicketStatusSaved || session.SavedAt == nil {
		t.Fatalf("after save: status=%q savedAt=%v", ticket.Status, session.SavedAt)
	}
	if got := last(env.lark.chatNames); got != "oc_1=[已保存][P4]标题" {
		t.Errorf("chat name = %q", got)
	}

	if _, err := env.exec(t, build, "--unsave"); err != nil {
		t.Fatalf("unsave = %v", err)
	}
	if ticket.Status != model.TicketStatusProcessing || session.SavedAt != nil {
		t.Errorf("after unsave: status=%q savedAt=%v", ticket.Status, session.SavedAt)
	}
}

func TestLevelChangesNameAndValidates(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewLevelCmd(env.sr, out) }

	if _, err := env.exec(t, build, "2"); err != nil {
		t.Fatalf("level = %v", err)
	}
	if ticket.Level != 2 || last(env.lark.chatNames) != "oc_1=[处理中][P2]标题" {
		t.Errorf("level=%d chatName=%q", ticket.Level, last(env.lark.chatNames))
	}

	if _, err := env.exec(t, build, "9"); err == nil || !strings.Contains(err.Error(), "级别必须是 1、2、3 或 4") {
		t.Errorf("invalid level error = %v", err)
	}
}

func TestHandInvitesAndReassigns(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewHandCmd(env.sr, out) }, "eks")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if len(env.lark.invites) != 1 || env.lark.invites[0][0] != "go.kong" {
		t.Errorf("invites = %v", env.lark.invites)
	}
	if ticket.AssigneeID != "go.kong" || ticket.GroupID != "eks" {
		t.Errorf("ticket = %+v", ticket)
	}
	if !strings.Contains(res.PlainText, `user_id="go.kong"`) {
		t.Errorf("PlainText = %q, want an @-mention of the new on-call", res.PlainText)
	}
}

func TestAssignFromSingleMention(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.Mentions = []runtime.MentionedUser{{UserID: "alice", Name: "Alice"}}

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewAssignCmd(env.sr, out) }, "@Alice")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if ticket.AssigneeID != "alice" {
		t.Errorf("assignee = %q", ticket.AssigneeID)
	}
	if !strings.Contains(res.PlainText, `user_id="alice"`) {
		t.Errorf("PlainText = %q", res.PlainText)
	}
}

func TestAssignRejectsWrongMentionCount(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.Mentions = []runtime.MentionedUser{{UserID: "a"}, {UserID: "b"}}

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewAssignCmd(env.sr, out) }, "@a")
	if err == nil || !strings.Contains(err.Error(), "只能移交给一个负责人") {
		t.Errorf("error = %v", err)
	}
}

func TestInviteModuleMembers(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewInviteCmd(env.sr, out) }, "eks")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if len(env.lark.invites) != 1 || env.lark.invites[0][0] != "go.kong" {
		t.Errorf("invites = %v", env.lark.invites)
	}
	if !strings.Contains(res.Markdown.Content, "已邀请 [eks] 值班人") {
		t.Errorf("Markdown = %q", res.Markdown.Content)
	}
}

// TestMarkAppendsTicketNote pins the log's append-only shape: a second /mark
// adds a row rather than overwriting the first, and each row carries its own
// author and marks. It is what the old single processing_record column could
// not express.
func TestMarkAppendsTicketNote(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	build := func(out runtime.Responder) *cobra.Command { return NewMarkCmd(env.sr, out) }

	res, err := env.exec(t, build, "--review", "需要补充", "SOP")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.PlainText != "工单已完成标记。" {
		t.Errorf("PlainText = %q", res.PlainText)
	}

	if _, err := env.exec(t, build, "--demand", "用户想要导出"); err != nil {
		t.Fatalf("second execute = %v", err)
	}

	notes := env.store.ticketNotes.notes
	if len(notes) != 2 {
		t.Fatalf("notes = %d, want 2 (the log must append, not overwrite)", len(notes))
	}
	first := notes[0]
	if first.TicketID != ticket.TicketID || first.Note != "需要补充 SOP" || !first.Review || first.Demand {
		t.Errorf("first note = %+v", first)
	}
	if first.AuthorID != "go.kong" || first.AuthorName != "name-of-go.kong" {
		t.Errorf("author = %q/%q", first.AuthorID, first.AuthorName)
	}
	second := notes[1]
	if second.Note != "用户想要导出" || second.Review || !second.Demand {
		t.Errorf("second note = %+v", second)
	}
}

func TestMarkRejectsUnboundSession(t *testing.T) {
	env := newTestEnv(t, nil, &model.SessionM{SessionID: "oc_1"})
	env.dr.TicketM = nil

	_, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewMarkCmd(env.sr, out) }, "x")
	if err == nil || !strings.Contains(err.Error(), "当前群未关联工单") {
		t.Errorf("error = %v", err)
	}
}

// chatRows returns the card's key/value rows, so the OpenID assertions read the
// same data the Lark template renders rather than the JSON beside it.
func chatRows(t *testing.T, res *runtime.CommandResult) []kvRow {
	t.Helper()
	rows, ok := res.LarkVariables["raw_data"].([]kvRow)
	if !ok {
		t.Fatalf("raw_data = %#v, want []kvRow", res.LarkVariables["raw_data"])
	}
	return rows
}

// TestChatReportsCallerOpenIDInPrivateChat pins the one surface that returns an
// open_id: a private chat, where both session_id and chat_id are the caller's
// bare user_id and the app-scoped handle is the only identity not already in the
// payload. An empty SenderOpenID — a caller that reached this with no Lark
// message behind it — must not leave an empty row on the card.
func TestChatReportsCallerOpenIDInPrivateChat(t *testing.T) {
	session := &model.SessionM{
		SessionID:   "go.kong",
		ChatID:      "go.kong",
		SessionType: model.SessionTypePrivateChat,
	}
	env := newTestEnv(t, nil, session)
	env.dr.Sender = v1.NewSender(env.dr.Identity(), "ou_abc123")

	build := func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) }
	res, err := env.exec(t, build)
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if got := res.Raw.(chatInfoResponse).OpenID; got != "ou_abc123" {
		t.Errorf("Raw open_id = %q, want ou_abc123", got)
	}
	if !strings.Contains(res.Markdown.Content, `"open_id": "ou_abc123"`) {
		t.Errorf("JSON payload is missing the open_id:\n%s", res.Markdown.Content)
	}
	var sawRow bool
	for _, row := range chatRows(t, res) {
		if row.Key == "OpenID" {
			sawRow = true
			if row.Value != "ou_abc123" {
				t.Errorf("OpenID row = %#v", row.Value)
			}
		}
	}
	if !sawRow {
		t.Errorf("card rows = %+v, want an OpenID row", res.LarkVariables["raw_data"])
	}

	// No open_id to report: the field stays out of the card rather than being
	// rendered as a blank value.
	env.dr.Sender = v1.NewSender(env.dr.Identity(), "")
	res, err = env.exec(t, build)
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	for _, row := range chatRows(t, res) {
		if row.Key == "OpenID" {
			t.Errorf("card carries an OpenID row with nothing in it: %#v", row)
		}
	}
}

// TestChatOmitsOpenIDInGroupChat is the other half: a group caller gets the
// ticket context and no open_id, in neither the JSON payload nor the card.
func TestChatOmitsOpenIDInGroupChat(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.Sender = v1.NewSender(env.dr.Identity(), "ou_group_sender")

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if got := res.Raw.(chatInfoResponse).OpenID; got != "" {
		t.Errorf("Raw open_id = %q, want empty", got)
	}
	if strings.Contains(res.Markdown.Content, "ou_group_sender") {
		t.Errorf("group payload leaked the open_id:\n%s", res.Markdown.Content)
	}
	for _, row := range chatRows(t, res) {
		if row.Key == "OpenID" {
			t.Errorf("group card carries an OpenID row: %#v", row)
		}
	}
}

// chatRowValue returns one card row's value, so an assertion reads the data the
// Lark template renders rather than the JSON beside it.
func chatRowValue(t *testing.T, res *runtime.CommandResult, key string) string {
	t.Helper()
	for _, row := range chatRows(t, res) {
		if row.Key == key {
			v, _ := row.Value.(string)
			return v
		}
	}
	t.Fatalf("card rows = %+v, want a %q row", res.LarkVariables["raw_data"], key)
	return ""
}

// TestChatReportsTheCallersOwnAgents is the whole point of the command's agent
// field: it answers "what am I running as", and the answer has to be the
// caller's rather than the conversation's.
//
// A conversation declares an intent (`@peer`, `@default`, a name) and the intent
// resolves per speaker — `@peer` is one agent for whoever bound an account and
// another for the next person to talk. The command used to render a result the
// scheduler had written on the conversation's row, so a group's members read back
// whichever agent the last speaker used. The assertion that matters is therefore
// not only what the card says but **who it was resolved for**: the runtime handed
// to the scheduler has to carry this caller's own identity and handle.
func TestChatReportsTheCallersOwnAgents(t *testing.T) {
	const (
		callerOpenID = "ou_bob"
		callerUserID = "go.bob"
	)

	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.Sender = v1.NewSender(callerUserID, callerOpenID)

	var asked *runtime.DynamicRuntime
	env.dr.ResolvedAgents = func(_ context.Context, dr *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
		asked = dr
		return runtime.AgentResolution{
			Refs:   []string{"@peer", "@default"},
			Agents: []string{"onex-u-user-bob"},
		}, nil
	}

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}

	if asked == nil {
		t.Fatal("the scheduler was never asked; the card would say nothing about this caller")
	}
	if got := asked.Handle(); got != callerOpenID {
		t.Errorf("the scheduler was asked about handle %q, want the caller's %q — a group's members must "+
			"not be shown the last speaker's agent", got, callerOpenID)
	}
	if got := asked.Identity(); got != callerUserID {
		t.Errorf("the scheduler was asked about identity %q, want the caller's %q", got, callerUserID)
	}

	if got := chatRowValue(t, res, "Agents"); got != "onex-u-user-bob" {
		t.Errorf("Agents row = %q, want the caller's agent", got)
	}
	// The declaration is a scheduling input, not a card field: the card names the
	// agent, and the payload's refs is where a reader tells a declared agent from a
	// defaulted one. A policy row here would have to print the conversation type's
	// template for a conversation that declares nothing, which reads as if somebody
	// had configured it.
	for _, row := range chatRows(t, res) {
		if row.Key == "Agent 策略" {
			t.Errorf("card carries a policy row: %#v", row)
		}
	}

	payload, ok := res.Raw.(chatInfoResponse)
	if !ok {
		t.Fatalf("Raw = %#v, want the chat-info payload", res.Raw)
	}
	var fromPayload []string
	if err := json.Unmarshal([]byte(payload.Agents), &fromPayload); err != nil {
		t.Fatalf("payload agents = %v", err)
	}
	if len(fromPayload) != 1 || fromPayload[0] != "onex-u-user-bob" {
		t.Errorf("payload agents = %v, want the caller's agent", fromPayload)
	}
	if got := strings.Join(payload.Refs, ","); got != "@peer,@default" {
		t.Errorf("payload refs = %q, want the declaration", got)
	}
}

// TestChatReportsWhyThereIsNoAgent pins the empty case. Rendering an empty list
// would send the reader to an administrator about a binding they can complete
// themselves, so the three situations are worded apart and the card carries the
// wording.
func TestChatReportsWhyThereIsNoAgent(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.ResolvedAgents = func(context.Context, *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
		return runtime.AgentResolution{Refs: []string{"@peer"}, Hint: runtime.AgentHintUnbound}, nil
	}

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}

	if got := chatRowValue(t, res, "Agents"); !strings.Contains(got, "绑定") {
		t.Errorf("Agents row = %q, want the invitation to bind rather than an empty list", got)
	}
}

// TestChatStillAnswersWhenTheResolutionFails pins that one field failing does not
// take the card down with it, and — just as important — that the failure is not
// dressed up as an ordinary empty answer: "nothing is configured" is a claim
// about the conversation, and a failed read knows nothing about it.
func TestChatStillAnswersWhenTheResolutionFails(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.ResolvedAgents = func(context.Context, *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
		return runtime.AgentResolution{}, errors.New("the binding store is unavailable")
	}

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v, want the card to render anyway", err)
	}
	if got := res.LarkVariables["raw_data"]; got == nil {
		t.Error("the card rendered no rows")
	}
	got := chatRowValue(t, res, "Agents")
	if !strings.Contains(got, "无法解析") {
		t.Errorf("Agents row = %q, want the failure worded as a failure", got)
	}
	if strings.Contains(got, "没有配置") {
		t.Errorf("Agents row = %q, want no claim about the conversation's configuration", got)
	}
}

// TestChatFallsBackToTheDeclarationWithoutAScheduler pins the REST face, which
// has no scheduler behind it and therefore no way to resolve for a caller.
//
// It reports the declaration and no agents. It must not report an agent it did
// not resolve: an empty answer is the honest one, and it is the wording that
// tells the reader nothing was resolved rather than that nothing applies.
func TestChatFallsBackToTheDeclarationWithoutAScheduler(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dr.SessionM.Refs = model.EncodeNameList([]string{"@peer"})
	env.dr.ResolvedAgents = nil

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewChatCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}

	if got := chatRowValue(t, res, "Agents"); !strings.Contains(got, "没有配置可用的助手") {
		t.Errorf("Agents row = %q, want the wording for an agent that could not be resolved", got)
	}
	// The declaration is still reported, just not as a card row: it is the only
	// thing this surface knows about agents, and dropping it would leave the
	// payload silent about the conversation's intent.
	if got := strings.Join(res.Raw.(chatInfoResponse).Refs, ","); got != "@peer" {
		t.Errorf("payload refs = %q, want the stored declaration %q", got, "@peer")
	}
}

func last(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}
