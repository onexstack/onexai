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

package agentbuilder

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/adk"
	adkpatchtoolcalls "github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/options"
)

// ── fakes ───────────────────────────────────────────────────────────────────

// fakeTool is the smallest thing that satisfies tool.BaseTool.
type fakeTool struct {
	name    string
	infoErr error
}

func (f *fakeTool) Info(context.Context) (*schema.ToolInfo, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return &schema.ToolInfo{Name: f.name}, nil
}

// fakeModel satisfies einomodel.BaseChatModel. The builder never calls it, so the
// methods only need to exist.
type fakeModel struct{}

func (fakeModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (fakeModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	r, w := schema.Pipe[*schema.Message](1)
	w.Close()
	return r, nil
}

type fakeSkillMiddleware struct {
	called    int
	gotKey    string
	gotSkills int
	err       error
}

func (f *fakeSkillMiddleware) Middleware(_ context.Context, agentKey string, skills []*model.SkillM) (adk.ChatModelAgentMiddleware, error) {
	f.called++
	f.gotKey = agentKey
	f.gotSkills = len(skills)
	if f.err != nil {
		return nil, f.err
	}
	return &instructionAppendHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, text: "skill"}, nil
}

type fakeKBTools struct{ names []string }

func (f *fakeKBTools) Tools(context.Context, []*model.KnowledgeBaseM) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(f.names))
	for _, n := range f.names {
		out = append(out, &fakeTool{name: n})
	}
	return out
}

func testBuilder(t *testing.T, deps Deps, opts Options) *Builder {
	t.Helper()
	if deps.ChatModel == nil {
		deps.ChatModel = fakeModel{}
	}
	return NewBuilder(deps, opts)
}

// ── tests ───────────────────────────────────────────────────────────────────

// TestBuild_FeatureOffKeepsToolSet pins the central backwards-compatibility
// promise: with every new capability switched off, the agent gets exactly the
// tools the caller passed and nothing else.
func TestBuild_FeatureOffKeepsToolSet(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	res, err := b.Build(context.Background(), Request{
		BaseTools: []tool.BaseTool{&fakeTool{name: "current_time"}, &fakeTool{name: "ask_user_question"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Runner == nil {
		t.Fatal("expected a runner")
	}
	if want := []string{"current_time", "ask_user_question"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v", res.ToolNames, want)
	}
	if res.IsolatedSkillsDir != "" {
		t.Errorf("skills are off, so no isolated dir should exist, got %q", res.IsolatedSkillsDir)
	}
	if res.AgentKey != DefaultAgentKey {
		t.Errorf("AgentKey = %q, want %q", res.AgentKey, DefaultAgentKey)
	}
}

func TestBuild_DeduplicatesToolNamesKeepingFirst(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	first := &fakeTool{name: "dup"}
	res, err := b.Build(context.Background(), Request{
		BaseTools: []tool.BaseTool{first, &fakeTool{name: "other"}, &fakeTool{name: "dup"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := []string{"dup", "other"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v", res.ToolNames, want)
	}
}

func TestBuild_DropsToolWithUnreadableInfo(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	res, err := b.Build(context.Background(), Request{
		BaseTools: []tool.BaseTool{&fakeTool{name: "good"}, &fakeTool{infoErr: io.ErrUnexpectedEOF}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := []string{"good"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v", res.ToolNames, want)
	}
}

func TestBuild_NilToolIsIgnored(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	res, err := b.Build(context.Background(), Request{
		BaseTools: []tool.BaseTool{nil, &fakeTool{name: "good"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := []string{"good"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v", res.ToolNames, want)
	}
}

func TestBuild_AgentKeyIsOrderIndependent(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	forward, err := b.Build(context.Background(), Request{AgentNames: []string{"bke-agent", "sre-agent"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reverse, err := b.Build(context.Background(), Request{AgentNames: []string{"sre-agent", "bke-agent"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if forward.AgentKey != reverse.AgentKey {
		t.Errorf("agent key depends on order: %q vs %q", forward.AgentKey, reverse.AgentKey)
	}
	if forward.AgentKey != "bke-agent.sre-agent" {
		t.Errorf("AgentKey = %q, want bke-agent.sre-agent", forward.AgentKey)
	}
}

// TestBuild_SkillsEnableSandboxAndMiddleware pins that a configured skills root
// plus resolved skills produces an isolated directory and calls the skill
// subsystem with the normalized key.
func TestBuild_SkillsEnableSandboxAndMiddleware(t *testing.T) {
	skills := &fakeSkillMiddleware{}
	b := testBuilder(t, Deps{Skills: skills}, Options{SkillsDir: "/var/lib/onexai/skills"})
	res, err := b.Build(context.Background(), Request{
		AgentNames: []string{"sre-agent"},
		Skills:     []*model.SkillM{{Name: "sre-skills"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if skills.called != 1 {
		t.Fatalf("skill middleware called %d times, want 1", skills.called)
	}
	if skills.gotKey != "sre-agent" {
		t.Errorf("skill middleware key = %q, want sre-agent", skills.gotKey)
	}
	if skills.gotSkills != 1 {
		t.Errorf("skill middleware got %d skills, want 1", skills.gotSkills)
	}
	if want := IsolatedSkillsDir("/var/lib/onexai/skills", "sre-agent"); res.IsolatedSkillsDir != want {
		t.Errorf("IsolatedSkillsDir = %q, want %q", res.IsolatedSkillsDir, want)
	}
}

// TestBuild_SkillsDirAloneDoesNotArmTheSandbox pins that a skills root without
// any resolved skill leaves the filesystem tools off — the sandbox is only
// worth having when there is something to put inside it.
func TestBuild_SkillsDirAloneDoesNotArmTheSandbox(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{SkillsDir: "/var/lib/onexai/skills"})
	res, err := b.Build(context.Background(), Request{AgentNames: []string{"sre-agent"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.IsolatedSkillsDir != "" {
		t.Errorf("no skills resolved, so no isolated dir should exist, got %q", res.IsolatedSkillsDir)
	}
}

func TestBuild_KnowledgeBaseToolsAreAppendedAndDeduped(t *testing.T) {
	kb := &fakeKBTools{names: []string{"search_knowledge_base", "dup"}}
	b := testBuilder(t, Deps{KnowledgeBases: kb}, Options{})
	res, err := b.Build(context.Background(), Request{
		BaseTools:      []tool.BaseTool{&fakeTool{name: "dup"}, &fakeTool{name: "current_time"}},
		KnowledgeBases: []*model.KnowledgeBaseM{{Name: "sre-kb"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// "dup" came from the caller first, so the knowledge-base one is dropped.
	if want := []string{"dup", "current_time", "search_knowledge_base"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v", res.ToolNames, want)
	}
}

func TestBuild_NilChatModelIsAnError(t *testing.T) {
	b := NewBuilder(Deps{}, Options{})
	if _, err := b.Build(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error when ChatModel is nil")
	}
}

// TestAssembleHandlers_Order pins the middleware ordering rules: dangling tool
// calls repaired first, then image extraction, the tool-error handler ahead of
// the caller's tool handlers, and the summarizer last.
func TestAssembleHandlers_Order(t *testing.T) {
	caller := &instructionAppendHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, text: "caller"}
	modelHandler := &instructionAppendHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, text: "model"}
	summarizer := &instructionAppendHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, text: "sum"}

	b := testBuilder(t, Deps{
		Vision: NewVisionMiddleware(fakeModel{}),
	}, Options{Prompts: options.PromptsConfig{System: "deployment-wide"}})

	got, err := b.assembleHandlers(context.Background(), Request{
		Handlers: HandlerGroups{
			ToolHandlers:   []adk.ChatModelAgentMiddleware{caller},
			ModelHandlers:  []adk.ChatModelAgentMiddleware{modelHandler},
			SummaryHandler: summarizer,
		},
	}, "agent", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("assembleHandlers: %v", err)
	}

	// The patch middleware's type is the library's, so the expected instance is
	// built the same way the builder builds it and only its type is compared.
	patchWant, err := adkpatchtoolcalls.New(context.Background(), &adkpatchtoolcalls.Config{
		PatchedToolResultGenerator: danglingToolResult,
	})
	if err != nil {
		t.Fatalf("patchtoolcalls.New: %v", err)
	}

	want := []any{
		patchWant,
		&imageExtractionMiddleware{},
		&ToolErrorHandler{},
		caller,
		modelHandler,
		&instructionAppendHandler{},
		summarizer,
	}
	if len(got) != len(want) {
		t.Fatalf("handler count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if reflect.TypeOf(got[i]) != reflect.TypeOf(want[i]) {
			t.Errorf("handler %d is %T, want %T", i, got[i], want[i])
		}
	}
}

func TestAssembleHandlers_OmitsNilSummarizer(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{})
	got, err := b.assembleHandlers(context.Background(), Request{}, "agent", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("assembleHandlers: %v", err)
	}
	// Repairing dangling tool calls and converting tool errors are the two
	// unconditional handlers.
	if len(got) != 2 {
		t.Fatalf("handler count = %d, want 2 (patch and tool-error)", len(got))
	}
	if _, ok := got[1].(*ToolErrorHandler); !ok {
		t.Errorf("handler 1 is %T, want *ToolErrorHandler", got[1])
	}
}

func TestExecuteSequentially(t *testing.T) {
	cases := []struct {
		name           string
		configured     bool
		approvalActive bool
		want           bool
	}{
		{name: "off by default", want: false},
		{name: "configured on", configured: true, want: true},
		{name: "forced on by approval", approvalActive: true, want: true},
		{name: "forced on even without config", approvalActive: true, configured: false, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testBuilder(t, Deps{}, Options{ExecuteSequentially: tc.configured})
			if got := b.executeSequentially(Request{ApprovalActive: tc.approvalActive}); got != tc.want {
				t.Errorf("executeSequentially = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestModelRetryConfig(t *testing.T) {
	if modelRetryConfig(0) != nil {
		t.Error("a zero budget must not add a retry config")
	}
	cfg := modelRetryConfig(3)
	if cfg == nil || cfg.MaxRetries != 3 {
		t.Errorf("modelRetryConfig(3) = %+v, want MaxRetries 3", cfg)
	}
}

// TestFailoverConfigKeepsTheRunModelFirst pins eino's counter-intuitive
// contract: GetFailoverModel is called for the first attempt too, so returning
// the fallback there would silently replace the run's own model.
func TestFailoverConfigKeepsTheRunModelFirst(t *testing.T) {
	primary := fakeModel{}
	fallback := fakeModel{}

	b := NewBuilder(Deps{ChatModel: primary}, Options{
		Failover: &Failover{
			Model: func(context.Context) (einomodel.BaseChatModel, error) { return fallback, nil },
		},
	})

	cfg := b.failoverConfig()
	if cfg == nil {
		t.Fatal("failoverConfig returned nil for a configured policy")
	}
	if cfg.MaxRetries != failoverAttempts {
		t.Errorf("MaxRetries = %d, want %d", cfg.MaxRetries, failoverAttempts)
	}

	first, _, err := cfg.GetFailoverModel(context.Background(), &adk.FailoverContext[*schema.Message]{FailoverAttempt: 1})
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if reflect.TypeOf(first) != reflect.TypeOf(primary) {
		t.Errorf("attempt 1 used %T, want the run's own model %T", first, primary)
	}

	second, _, err := cfg.GetFailoverModel(context.Background(), &adk.FailoverContext[*schema.Message]{FailoverAttempt: 2})
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if reflect.TypeOf(second) != reflect.TypeOf(fallback) {
		t.Errorf("attempt 2 used %T, want the fallback %T", second, fallback)
	}
}

func TestFailoverConfigWithoutAPolicy(t *testing.T) {
	b := NewBuilder(Deps{ChatModel: fakeModel{}}, Options{})
	if cfg := b.failoverConfig(); cfg != nil {
		t.Fatalf("failoverConfig returned %T with no policy", cfg)
	}
}

func TestFailoverShouldFailover(t *testing.T) {
	b := NewBuilder(Deps{ChatModel: fakeModel{}}, Options{
		Failover: &Failover{
			Model: func(context.Context) (einomodel.BaseChatModel, error) { return fakeModel{}, nil },
		},
	})
	cfg := b.failoverConfig()

	if cfg.ShouldFailover(context.Background(), nil, nil) {
		t.Error("ShouldFailover accepted a nil error")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if cfg.ShouldFailover(cancelled, nil, context.Canceled) {
		t.Error("ShouldFailover retried a cancelled run")
	}
	if !cfg.ShouldFailover(context.Background(), nil, errors.New("boom")) {
		t.Error("ShouldFailover refused an ordinary failure")
	}
}
