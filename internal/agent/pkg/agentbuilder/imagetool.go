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
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// ImageGenerator draws one image and stores it.
//
// It is defined here, on the consumer side, so this package depends on one
// method rather than on the image-generation service — and so a test can answer
// it without a provider.
type ImageGenerator interface {
	// Draw stores the drawn image and returns the reference that names it.
	Draw(ctx context.Context, prompt string) (media.Ref, error)
}

// generateImageTool is the tool the model calls to draw something.
//
// # Why the result is words and not the picture
//
// A tool returns a string to the model. Handing it the image — as a data URL,
// or as a path it would have to read — puts a payload the model is billed to
// read into a conversation that asked for a picture, and produces an answer
// about the picture rather than the picture. So the image goes out of band, on
// the run's ImageSink, and what the model gets back is a sentence saying one
// was drawn.
//
// The sentence still matters. The model has to know the drawing happened, and
// has to be told not to describe it — a model that has just been told "an image
// was drawn" will otherwise narrate one it cannot see.
type generateImageTool struct {
	gen ImageGenerator
}

// Info describes the tool to the model.
func (t *generateImageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "generate_image",
		Desc: "根据文字描述生成一张图片，并把图片直接发送给用户。" +
			"当你需要为用户画图时调用它。生成后不要说“我无法生成图片”，" +
			"也不要描述图片内容——图片已经发给用户了，你只需简述画了什么。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"prompt": {
				Type:     schema.String,
				Desc:     "要画的内容，用中文或英文描述。描述越具体，结果越接近预期。",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun draws the image and reports it on the run's sink.
func (t *generateImageTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("generate_image: %w", err)
	}
	if args.Prompt == "" {
		return "没有给出要画的内容。", nil
	}

	ref, err := t.gen.Draw(ctx, args.Prompt)
	if err != nil {
		// The failure goes back to the model rather than ending the run: it can
		// tell the user, or try a different description, and either is a better
		// answer than a turn that stops here.
		slog.WarnContext(ctx, "[agentbuilder] image generation failed", "error", err)
		return fmt.Sprintf("生成图片失败：%v", err), nil
	}

	if sink := agent.ImageSinkFrom(ctx); sink != nil {
		sink.Image(ctx, []media.Ref{ref})
	} else {
		// No sink means this run has nowhere to put a picture — a caller that
		// asked for text. The image is stored either way, so the model is told
		// the truth about what happened and the caller is not told a drawing
		// reached them.
		slog.WarnContext(ctx, "[agentbuilder] an image was generated but the run has no sink to report it on",
			"asset_id", ref.AssetID)
	}

	name := ref.Name
	if name == "" {
		name = ref.AssetID
	}
	return fmt.Sprintf("已根据描述生成图片并发送给用户（%s）。不要重复描述图片内容。", name), nil
}

// newGenerateImageTool builds the tool, or nil when no generator is configured.
func newGenerateImageTool(gen ImageGenerator) tool.BaseTool {
	if gen == nil {
		return nil
	}
	return &generateImageTool{gen: gen}
}
