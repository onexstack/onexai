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
// The original repo for this file is https://github.com/onexstack/onex.

package agentbuilder

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
)

// instructionAppendHandler appends a fixed text to the agent's instruction
// before each run. It is how deployment-wide prompt fragments (Options.Prompts)
// reach the model without being baked into any agent's stored persona.
type instructionAppendHandler struct {
	*adk.BaseChatModelAgentMiddleware
	text string
}

func newInstructionAppendHandler(text string) *instructionAppendHandler {
	return &instructionAppendHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		text:                         text,
	}
}

// BeforeAgent appends the text to the run's instruction. ChatModelAgentContext
// only exposes an instruction string, so everything injected at this layer
// lands there.
func (h *instructionAppendHandler) BeforeAgent(
	ctx context.Context, runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil || h.text == "" {
		return ctx, runCtx, nil
	}
	if runCtx.Instruction == "" {
		runCtx.Instruction = h.text
	} else {
		runCtx.Instruction += "\n" + h.text
	}
	return ctx, runCtx, nil
}

// ── image extraction ────────────────────────────────────────────────────────

// visionMiddlewareFactory adapts a vision model into a VisionMiddleware.
type visionMiddlewareFactory struct {
	visionModel model.BaseChatModel
	// needs decides, per run, whether the primary model requires extraction.
	// Nil means always. It reads the run context, because one cached graph
	// serves runs whose effective model differs.
	needs func(ctx context.Context) bool
}

// NewVisionMiddleware returns a VisionMiddleware that describes images with
// the given model. Callers should only construct it when image extraction is
// actually wanted — deciding whether the primary model needs it is a
// deployment question, not a builder one.
func NewVisionMiddleware(visionModel model.BaseChatModel) VisionMiddleware {
	return &visionMiddlewareFactory{visionModel: visionModel}
}

// NewVisionMiddlewareWithPredicate returns a VisionMiddleware that decides per
// run whether the primary model needs the extraction. A nil predicate means
// always. A nil vision model makes it a guard: images are stripped and replaced
// with a note, because the primary model cannot read them and there is nothing
// to describe them with.
func NewVisionMiddlewareWithPredicate(
	visionModel model.BaseChatModel, needs func(ctx context.Context) bool,
) VisionMiddleware {
	return &visionMiddlewareFactory{visionModel: visionModel, needs: needs}
}

// Middleware returns the (stateless) extraction handler, or nil when no model
// was supplied.
func (f *visionMiddlewareFactory) Middleware(context.Context) (adk.ChatModelAgentMiddleware, error) {
	if f == nil || f.visionModel == nil {
		return nil, nil
	}
	return &imageExtractionMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		visionModel:                  f.visionModel,
		needs:                        f.needs,
	}, nil
}

// imageExtractionMiddleware replaces image parts in user messages with a text
// description produced by a vision-capable model, for a primary model that
// cannot accept images at all.
type imageExtractionMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	visionModel model.BaseChatModel
	needs       func(ctx context.Context) bool
}

// imageExtractionInstruction is what the vision model is asked for. It asks
// for everything a later reasoning step could need, because the image is gone
// from the conversation afterwards.
const imageExtractionInstruction = "请分析以下图片，提取其中所有有价值的信息，包括但不限于：文字、数字、代码片段、错误信息、" +
	"图表数据、架构图中的组件和关系。请用中文简洁描述。"

// BeforeModelRewriteState converts every image part of every user message into
// an "[图片内容：…]" (image content) text part.
//
// Images are removed even when the vision call fails: leaving them in would
// send the primary model content it rejects, turning a degraded answer into a
// failed request.
func (m *imageExtractionMiddleware) BeforeModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}
	// The effective model is a property of this run, not of the graph: a
	// cached runner may serve a run whose model can see images and one whose
	// model cannot.
	if m.needs != nil && !m.needs(ctx) {
		return ctx, state, nil
	}
	for _, message := range state.Messages {
		if message == nil || message.Role != schema.User || len(message.UserInputMultiContent) == 0 {
			continue
		}
		describeMessageImages(ctx, agentedge.InlinerFrom(ctx), m.visionModel, message)
	}
	return ctx, state, nil
}

// describeMessageImages rewrites one message's image parts into text parts.
//
// The image parts carry references rather than bytes, so they are resolved for
// the vision call alone. The resolved URLs are deliberately not written back:
// this runs before the model call and its result is persisted in the agent's
// state, so leaving a data URL anywhere in the message would put base64 into the
// checkpoint the whole design keeps it out of. The image parts are removed
// anyway — only the description survives.
func describeMessageImages(
	ctx context.Context, in *agentedge.Inliner, visionModel model.BaseChatModel, message *schema.Message,
) {
	textParts, imageParts := splitMessageParts(message.UserInputMultiContent)
	if len(imageParts) == 0 {
		return
	}
	if visionModel == nil {
		// The primary model cannot read images and there is nothing to describe
		// them with. Sending them anyway fails the whole request; dropping them
		// with a note at least answers the text.
		message.UserInputMultiContent = append(textParts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: "[图片无法解析：当前模型不支持图片输入，且部署未配置视觉模型]",
		})
		return
	}
	if in != nil {
		imageParts, _ = in.ResolveParts(ctx, imageParts)
	}

	extracted, err := extractImageText(ctx, visionModel, imageParts)
	if err != nil {
		slog.WarnContext(ctx, "[agentbuilder] vision model failed; dropping images", "error", err)
		message.UserInputMultiContent = textParts
		return
	}
	message.UserInputMultiContent = append(textParts, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: fmt.Sprintf("[图片内容：%s]", extracted),
	})
}

// splitMessageParts separates the text parts from the image parts, preserving
// the order of the text ones.
func splitMessageParts(parts []schema.MessageInputPart) (text, images []schema.MessageInputPart) {
	for _, part := range parts {
		if part.Type == schema.ChatMessagePartTypeImageURL {
			images = append(images, part)
		} else {
			text = append(text, part)
		}
	}
	return text, images
}

// extractImageText asks the vision model to describe the given image parts.
func extractImageText(
	ctx context.Context, visionModel model.BaseChatModel, imageParts []schema.MessageInputPart,
) (string, error) {
	input := make([]schema.MessageInputPart, 0, len(imageParts)+1)
	input = append(input, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: imageExtractionInstruction,
	})
	input = append(input, imageParts...)

	resp, err := visionModel.Generate(ctx, []*schema.Message{{
		Role:                  schema.User,
		UserInputMultiContent: input,
	}})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("vision model returned no message")
	}
	if resp.Content != "" {
		return resp.Content, nil
	}
	return joinTextParts(resp.UserInputMultiContent), nil
}

// joinTextParts concatenates the text parts of a multimodal response, which is
// how some providers return the description.
func joinTextParts(parts []schema.MessageInputPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == schema.ChatMessagePartTypeText {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}
