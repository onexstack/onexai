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

package di

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/pkg/llmhttp"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"

	"github.com/onexstack/onexai/internal/agent/biz/v1/imagegen"
	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// fakeMCPStore serves a fixed set of MCP rows; only List is meaningful.
type fakeMCPStore struct {
	store.MCPStore
	rows []*model.MCPM
}

func (f *fakeMCPStore) List(context.Context, *where.Options) (int64, []*model.MCPM, error) {
	return int64(len(f.rows)), f.rows, nil
}

type fakeAIStore struct {
	store.AIStore
	mcps store.MCPStore
}

func (f *fakeAIStore) MCPs() store.MCPStore { return f.mcps }

// fakeModelStore answers with one row, so a test can say what a model declares.
type fakeModelStore struct {
	store.ModelStore
	row *model.ModelM
}

func (f *fakeModelStore) Get(context.Context, *where.Options) (*model.ModelM, error) {
	if f.row == nil {
		return nil, store.ErrNotFound
	}
	return f.row, nil
}

// modelAIStore is a resource store whose model lookup is the given row.
type modelAIStore struct {
	store.AIStore
	models store.ModelStore
}

func (f *modelAIStore) Models() store.ModelStore { return f.models }

// TestConfiguredMCPs covers the startup warning's input. It must report what the
// store declares rather than what has been dialed: discovery is lazy, so a
// warning driven by the manager's cache would never fire at startup — the one
// moment it exists to catch a misconfigured engine.
func TestConfiguredMCPs(t *testing.T) {
	ctx := context.Background()

	if got := configuredMCPs(ctx, nil); got != nil {
		t.Fatalf("a nil store should yield nothing, got %v", got)
	}

	rs := &fakeAIStore{mcps: &fakeMCPStore{rows: []*model.MCPM{
		{Name: "zke-kubeagent", Status: model.StatusEnabled},
		{Name: "retired", Status: model.StatusDisabled},
		{Name: "unset-status"},
	}}}
	got := configuredMCPs(ctx, rs)
	want := []string{"zke-kubeagent", "unset-status"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestBuildVisionMiddleware pins the gating of the image-extraction middleware.
//
// Both halves are load-bearing. A nil VisionModel is the common deployment and
// must be recognised before it is dereferenced — getting that wrong is a
// nil-pointer panic at startup, not a soft failure. And a vision model that IS
// configured must actually produce a middleware: an inverted guard there would
// silently drop every image instead of describing it, which looks like a model
// that "just doesn't handle images".
func TestBuildVisionMiddleware(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		resources store.AIStore
		wantNil   bool
		why       string
	}{
		{
			name:    "no agent options at all",
			cfg:     &Config{AgentOptions: &customoptions.AgentOptions{}},
			wantNil: false,
			why:     "nothing is configured",
		},
		{
			name:    "no vision model configured",
			cfg:     &Config{LLMModel: "deepseek-v4-flash", AgentOptions: &customoptions.AgentOptions{}},
			wantNil: false,
			why:     "the crash scenario: a text-only primary model with no vision endpoint",
		},
		{
			name: "vision model on a deepseek primary",
			cfg: &Config{
				LLMBaseURL:   "https://api.deepseek.com/v1",
				LLMAPIKey:    "test-key",
				LLMModel:     "deepseek-v4-flash",
				AgentOptions: &customoptions.AgentOptions{VisionModel: &customoptions.ChatModelConfig{Model: "qwen-vl-max"}},
			},
			wantNil: false,
			why:     "this is the configuration the middleware exists for",
		},
		{
			name: "vision model on a primary that can already see",
			cfg: &Config{
				LLMBaseURL:   "https://example.invalid/v1",
				LLMAPIKey:    "test-key",
				LLMModel:     "qwen-plus",
				AgentOptions: &customoptions.AgentOptions{VisionModel: &customoptions.ChatModelConfig{Model: "qwen-vl-max"}},
			},
			wantNil: false,
			why:     "extraction would replace images with a lossy description for nothing",
		},
		{
			// The name says DeepSeek, the row says it can see. The row wins:
			// a deployment that runs a vision adapter in front of a DeepSeek
			// name has said so, and describing images for it anyway would
			// replace them with a lossy paragraph for nothing.
			name: "a declared capability beats the name's inference",
			cfg: &Config{
				LLMBaseURL:   "https://api.deepseek.com/v1",
				LLMAPIKey:    "test-key",
				LLMModel:     "deepseek-v4-flash",
				AgentOptions: &customoptions.AgentOptions{VisionModel: &customoptions.ChatModelConfig{Model: "qwen-vl-max"}},
			},
			resources: &modelAIStore{models: &fakeModelStore{row: &model.ModelM{
				Name: "deepseek-v4-flash", Provider: "deepseek", Capabilities: []string{"vision"},
			}}},
			wantNil: false,
			why:     "the row declared vision, so there is nothing to describe",
		},
		{
			// The mirror image: an OpenAI-shaped name whose row declares no
			// vision. The name would have said it could see.
			name: "a row that declares no vision is believed over an OpenAI name",
			cfg: &Config{
				LLMBaseURL:   "https://example.invalid/v1",
				LLMAPIKey:    "test-key",
				LLMModel:     "qwen-plus",
				AgentOptions: &customoptions.AgentOptions{VisionModel: &customoptions.ChatModelConfig{Model: "qwen-vl-max"}},
			},
			resources: &modelAIStore{models: &fakeModelStore{row: &model.ModelM{
				Name: "qwen-plus", Provider: "qwen", Capabilities: []string{"reasoning"},
			}}},
			wantNil: false,
			why:     "the row declared no vision, so images need describing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Wire always supplies a store (an empty one when resources are
			// off), so a case that declares no rows still gets one.
			resources := tt.resources
			if resources == nil {
				resources = store.NewEmptyStore()
			}
			got := buildVisionMiddleware(tt.cfg, resources)
			if tt.wantNil && got != nil {
				t.Fatalf("got a middleware, want nil (%s)", tt.why)
			}
			if !tt.wantNil && got == nil {
				t.Fatalf("got nil, want a middleware (%s)", tt.why)
			}
		})
	}
}

// TestModelAcceptsImagesReadsTheUpstreamModel pins the reason a logical
// endpoint name is not enough: "deepseek-secured" says nothing about image
// support, while its upstream model "deepseek-v4-flash" does. Reading the row
// name would deny every vision-capable DeepSeek endpoint a deployment named
// after its credential.
func TestModelAcceptsImagesReadsTheUpstreamModel(t *testing.T) {
	resources := &modelAIStore{models: &fakeModelStore{row: &model.ModelM{
		Name:     "deepseek-secured",
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
	}}}

	if !modelAcceptsImages("deepseek-secured", resources) {
		t.Fatal("a row whose upstream is deepseek-v4-flash must accept images")
	}

	older := &modelAIStore{models: &fakeModelStore{row: &model.ModelM{
		Name:     "deepseek-secured",
		Provider: "deepseek",
		Model:    "deepseek-chat",
	}}}
	if modelAcceptsImages("deepseek-secured", older) {
		t.Fatal("a row whose upstream is deepseek-chat must stay text-only")
	}
}

// fakeImageGen records what the drawer asked it to draw.
type fakeImageGen struct {
	imagegen.ImageGenBiz
	got *v1.ImageGenerationRequest
}

func (f *fakeImageGen) Generate(_ context.Context, req *v1.ImageGenerationRequest) (*v1.ImageGenerationResponse, error) {
	f.got = req
	return &v1.ImageGenerationResponse{Data: []*v1.GeneratedImage{{
		AssetId: "img_drawn",
		Url:     "/media/generated/u/img_drawn/a.png",
		Ref: &v1.ImageRef{
			AssetId: "img_drawn", Url: "/media/generated/u/img_drawn/a.png", MimeType: "image/png",
		},
	}}}, nil
}

func (f *fakeImageGen) Configured() bool { return true }

// TestImageDrawerAttributesTheDrawingToTheAsker pins the fix for a bug the
// feature's first live run exposed: every drawing was stored under one shared
// `anon` directory.
//
// The tool that asks for an image has no argument for who asked — the model
// supplies a prompt and nothing else — so the identity comes off the run's
// context. It is the same value a chat turn carries for the MCP tools to scope
// records by, and leaving it unset is not a harmless default: the row is keyed
// by it, so an unnamed owner makes every user's drawings land together and none
// of them attributable.
func TestImageDrawerAttributesTheDrawingToTheAsker(t *testing.T) {
	gen := &fakeImageGen{}
	drawer := ProvideImageDrawer(gen)
	if drawer == nil {
		t.Fatal("ProvideImageDrawer returned nil for a configured generator")
	}

	ctx := contextx.WithUserID(context.Background(), "go.kong")
	ref, err := drawer.Draw(ctx, "一个红色的方块")
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if gen.got.GetSenderID() != "go.kong" {
		t.Errorf("SenderID = %q, want the asker from the context", gen.got.GetSenderID())
	}
	if gen.got.GetPrompt() != "一个红色的方块" {
		t.Errorf("Prompt = %q", gen.got.GetPrompt())
	}
	// The reference comes back whole, which is what the tool reports on the run's
	// sink and what the delivery path later resolves.
	if ref.AssetID != "img_drawn" || ref.MimeType != "image/png" {
		t.Errorf("the drawer lost fields: %+v", ref)
	}

	// A run with no identity still draws: refusing would lose the picture, and
	// an unattributed asset is recoverable where a missing one is not.
	ref, err = drawer.Draw(context.Background(), "另一个")
	if err != nil || ref.AssetID == "" {
		t.Errorf("a run with no identity could not draw: %+v, %v", ref, err)
	}
	if gen.got.GetSenderID() != "" {
		t.Errorf("an absent identity became %q", gen.got.GetSenderID())
	}
}

// TestProvideImageDrawerWithoutAGenerator pins that a deployment with no image
// model has no tool: a tool the model can see but cannot use is worse than none.
func TestProvideImageDrawerWithoutAGenerator(t *testing.T) {
	if got := ProvideImageDrawer(&fakeImageGen{}); got == nil {
		t.Error("ProvideImageDrawer refused a configured generator")
	}
}

// TestBuildFailoverWithoutFallbackIsNil pins the ordinary case: a deployment
// that names no fallback model gets no failover wrapper at all, so the graph is
// exactly the one it had before the feature existed.
func TestBuildFailoverWithoutFallbackIsNil(t *testing.T) {
	cfg := &Config{
		LLMBaseURL:   "http://127.0.0.1:1",
		LLMModel:     "primary",
		AgentOptions: customoptions.NewAgentOptions(),
	}
	if cfg.AgentOptions.FallbackModel = ""; buildFailover(cfg) != nil {
		t.Fatal("buildFailover built a policy with no fallback model")
	}
}

// TestBuildFailoverCoversTheDeploymentModelOnly pins the boundary: a run on a
// user-configured endpoint neither borrows the deployment's fallback model nor
// fails over at all.
func TestBuildFailoverCoversTheDeploymentModelOnly(t *testing.T) {
	cfg := &Config{
		LLMBaseURL: "http://127.0.0.1:1",
		LLMAPIKey:  "test-key",
		LLMModel:   "primary",
	}
	cfg.AgentOptions = customoptions.NewAgentOptions()
	cfg.AgentOptions.FallbackModel = "fallback"

	policy := buildFailover(cfg)
	if policy == nil {
		t.Fatal("buildFailover returned nil with a fallback configured")
	}

	m, err := policy.Model(context.Background())
	if err != nil || m == nil {
		t.Fatalf("deployment fallback model = (%v, %v), want a model", m, err)
	}

	userCtx := agentapi.WithModelName(context.Background(), "my-endpoint")
	if _, err := policy.Model(userCtx); err == nil {
		t.Error("a user-configured endpoint borrowed the deployment fallback")
	}
	if policy.ShouldFailover(userCtx, nil, errors.New("boom")) {
		t.Error("a user-configured endpoint failed over")
	}
}

// TestBuildFailoverSkipsHopelessErrors pins the status filter: a rejected key
// fails the same way on every model, so the fallback is not spent on it.
func TestBuildFailoverSkipsHopelessErrors(t *testing.T) {
	cfg := &Config{
		LLMBaseURL: "http://127.0.0.1:1",
		LLMAPIKey:  "test-key",
		LLMModel:   "primary",
	}
	cfg.AgentOptions = customoptions.NewAgentOptions()
	cfg.AgentOptions.FallbackModel = "fallback"
	policy := buildFailover(cfg)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unauthorized", &llmhttp.HTTPError{Code: 401}, false},
		{"bad request", &llmhttp.HTTPError{Code: 400}, false},
		{"rate limited", &llmhttp.HTTPError{Code: 429}, true},
		{"unavailable", &llmhttp.HTTPError{Code: 503}, true},
		{"network", errors.New("connection reset"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.ShouldFailover(context.Background(), nil, c.err); got != c.want {
				t.Errorf("ShouldFailover(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestProvideAuthzCoordinatorRefusesToStartWithoutAnAgentLevelBackend pins the
// difference between "no rules were written" (a runtime state that denies every
// request, and is fixable by writing a rule) and "no authorizer was built" (a
// deployment error that would deny every request with a reason naming nothing
// fixable). The second is a startup failure: an empty agent/tool-level Union is
// fail-closed, so serving would turn a missing rule source into an opaque 403
// for every caller.
func TestProvideAuthzCoordinatorRefusesToStartWithoutAnAgentLevelBackend(t *testing.T) {
	cfg := &Config{AuthzOptions: &customoptions.AuthzOptions{
		Enabled: true,
		OPA:     customoptions.OPAOptions{Enabled: true},
	}}

	// opa enabled but no rule source and no other backend: exactly the
	// misconfiguration that used to come up and allow everything.
	coordinator, err := ProvideAuthzCoordinator(context.Background(), cfg, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected startup to fail, got coordinator=%v", coordinator)
	}
	if coordinator != nil {
		t.Fatal("a failed startup must not also return a coordinator")
	}

	// Authorization disabled is still the documented "no-op" configuration and
	// must not be turned into a startup failure.
	disabled := &Config{AuthzOptions: &customoptions.AuthzOptions{Enabled: false}}
	coordinator, err = ProvideAuthzCoordinator(context.Background(), disabled, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("authz disabled must start: %v", err)
	}
	if coordinator != nil {
		t.Fatal("authz disabled must yield a nil coordinator (allow everything, no gate)")
	}
}
