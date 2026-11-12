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
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// fakeDrawer records the prompts it was given and answers with a fixed image.
type fakeDrawer struct {
	prompts []string
	err     error
}

func (d *fakeDrawer) Draw(_ context.Context, prompt string) (media.Ref, error) {
	d.prompts = append(d.prompts, prompt)
	if d.err != nil {
		return media.Ref{}, d.err
	}
	return media.Ref{AssetID: "img_drawn", URL: "/media/images/u/img_drawn/a.png", MimeType: "image/png"}, nil
}

// recordingSink captures what a tool reported out of band.
type recordingSink struct{ got [][]media.Ref }

func (s *recordingSink) Image(_ context.Context, refs []media.Ref) { s.got = append(s.got, refs) }

// invokable is the slice of the tool this test drives.
type invokable interface {
	InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
}

func newTool(t *testing.T, gen ImageGenerator) invokable {
	t.Helper()
	base := newGenerateImageTool(gen)
	if base == nil {
		t.Fatal("newGenerateImageTool returned nil")
	}
	in, ok := base.(invokable)
	if !ok {
		t.Fatalf("the tool does not implement InvokableRun: %T", base)
	}
	return in
}

// TestGenerateImageReportsOutOfBand is the property the tool exists for: the
// picture leaves on the sink and the model gets words.
//
// Both halves matter. A tool that returned the image would put a base64 payload
// into a conversation that asked for a picture; a tool that reported nothing
// would leave the model to say it cannot draw — the answer the person did not
// want.
func TestGenerateImageReportsOutOfBand(t *testing.T) {
	drawer := &fakeDrawer{}
	sink := &recordingSink{}
	inv := newTool(t, drawer)

	ctx := agent.WithImageSink(context.Background(), sink)
	out, err := inv.InvokableRun(ctx, `{"prompt":"一只戴帽子的猫"}`)
	if err != nil {
		t.Fatalf("InvokableRun returned %v", err)
	}

	if len(drawer.prompts) != 1 || drawer.prompts[0] != "一只戴帽子的猫" {
		t.Errorf("prompts = %v, want the model's description", drawer.prompts)
	}
	if len(sink.got) != 1 || len(sink.got[0]) != 1 || sink.got[0][0].AssetID != "img_drawn" {
		t.Fatalf("the sink received %+v, want one reference to img_drawn", sink.got)
	}
	if strings.Contains(out, "base64") || strings.Contains(out, "/media/") {
		t.Errorf("the tool result carries the image: %q", out)
	}
	// The model has to be told the drawing happened, and told not to narrate
	// one it cannot see.
	if !strings.Contains(out, "已") {
		t.Errorf("the tool result does not say the image was sent: %q", out)
	}
	if !strings.Contains(out, "不要重复描述") {
		t.Errorf("the tool result does not tell the model to stop describing: %q", out)
	}
}

// TestGenerateImageWithoutSinkStillDraws pins the degradation: a run with
// nowhere to put a picture — a text-only caller — still draws and still tells
// the model the truth, rather than reporting a success nobody saw.
func TestGenerateImageWithoutSinkStillDraws(t *testing.T) {
	drawer := &fakeDrawer{}
	inv := newTool(t, drawer)

	out, err := inv.InvokableRun(context.Background(), `{"prompt":"一只猫"}`)
	if err != nil {
		t.Fatalf("InvokableRun returned %v", err)
	}
	if len(drawer.prompts) != 1 {
		t.Errorf("the image was not drawn: %v", drawer.prompts)
	}
	if !strings.Contains(out, "已") {
		t.Errorf("the model was not told a drawing happened: %q", out)
	}
}

// TestGenerateImageFailureIsAToolResult pins that a provider failure comes back
// as words rather than as an error: the run can tell the user and try a
// different description, which is a better answer than a turn that stops.
func TestGenerateImageFailureIsAToolResult(t *testing.T) {
	inv := newTool(t, &fakeDrawer{err: errors.New("quota exhausted")})

	out, err := inv.InvokableRun(context.Background(), `{"prompt":"一只猫"}`)
	if err != nil {
		t.Fatalf("a provider failure became a tool error: %v", err)
	}
	if !strings.Contains(out, "quota exhausted") {
		t.Errorf("the reason was dropped: %q", out)
	}
}

// TestGenerateImageWithoutPrompt pins that an empty description is refused in
// words rather than spending a provider call on nothing.
func TestGenerateImageWithoutPrompt(t *testing.T) {
	drawer := &fakeDrawer{}
	inv := newTool(t, drawer)

	out, err := inv.InvokableRun(context.Background(), `{"prompt":""}`)
	if err != nil {
		t.Fatalf("InvokableRun returned %v", err)
	}
	if len(drawer.prompts) != 0 {
		t.Errorf("a provider was called for an empty prompt: %v", drawer.prompts)
	}
	if out == "" {
		t.Error("the model was told nothing")
	}
}

// TestNewGenerateImageToolWithoutGenerator pins that a deployment with no image
// model has no tool: a tool the model can see but cannot use is worse than no
// tool, because the model will call it and report a failure the operator has to
// read the logs to explain.
func TestNewGenerateImageToolWithoutGenerator(t *testing.T) {
	if got := newGenerateImageTool(nil); got != nil {
		t.Errorf("newGenerateImageTool(nil) = %T, want nil", got)
	}
}

// TestGenerateImageInfo names the tool the model is told about. The name is a
// contract with every prompt written against it.
func TestGenerateImageInfo(t *testing.T) {
	inv := newTool(t, &fakeDrawer{})
	info, err := inv.(tool.BaseTool).Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned %v", err)
	}
	if info.Name != "generate_image" {
		t.Errorf("Name = %q, want generate_image", info.Name)
	}
	if info.Desc == "" {
		t.Error("the tool has no description, so the model cannot know when to call it")
	}
	if info.ParamsOneOf == nil {
		t.Fatal("the tool declares no parameters")
	}
}
