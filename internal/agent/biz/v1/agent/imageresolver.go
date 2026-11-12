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

package agent

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
)

// imageResolveMiddleware turns image references into model-ready data URLs at
// the last moment before the model is called.
//
// # Why this is a model wrapper and not a state rewrite
//
// adk offers two hooks near the model call. BeforeModelRewriteState runs before
// it and writes its result back into the agent's state — and that state is what
// a paused run checkpoints, so a data URL written there is a base64 payload in
// Redis, which is the thing this design exists to avoid. WrapModel wraps the
// call itself: its input is what that one call sees, and nothing it does is
// persisted. That difference is the entire reason for this file.
//
// Everything else follows from it. The history keeps references; the checkpoint
// keeps references; and the bytes appear only in the argument to Generate or
// Stream, for as long as that call takes.
type imageResolveMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

// newImageResolveMiddleware returns the middleware. It is stateless: the
// resolver is read from the run's context, so the handler can be built with the
// agent set and reused across runs.
func newImageResolveMiddleware() *imageResolveMiddleware {
	return &imageResolveMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

// WrapModel wraps the model with the resolver, or returns it untouched when the
// run has nothing to resolve with.
func (h *imageResolveMiddleware) WrapModel(
	ctx context.Context, m model.BaseChatModel, _ *adk.ModelContext,
) (model.BaseChatModel, error) {
	in := agentedge.InlinerFrom(ctx)
	if in == nil || !in.Available() {
		return m, nil
	}
	return &imageResolvingModel{inner: m, inliner: in}, nil
}

// imageResolvingModel resolves a call's image references before delegating.
type imageResolvingModel struct {
	inner   model.BaseChatModel
	inliner *agentedge.Inliner
}

func (m *imageResolvingModel) Generate(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	return m.inner.Generate(ctx, m.resolve(ctx, in), opts...)
}

func (m *imageResolvingModel) Stream(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, m.resolve(ctx, in), opts...)
}

// resolve returns copies of the messages whose image parts are model-ready.
//
// A message with nothing to resolve is passed through as-is, and only a message
// that changed is copied: the input belongs to the agent's state, which is about
// to be checkpointed, and writing a data URL into it would undo the whole point.
func (m *imageResolvingModel) resolve(ctx context.Context, in []*schema.Message) []*schema.Message {
	var out []*schema.Message
	for i, message := range in {
		if message == nil || len(message.UserInputMultiContent) == 0 {
			continue
		}
		parts, changed := m.inliner.ResolveParts(ctx, message.UserInputMultiContent)
		if !changed {
			continue
		}
		// Copied on the first change, so a run with no images allocates nothing
		// and a run with them never writes into the slice it was handed.
		if out == nil {
			out = make([]*schema.Message, len(in))
			copy(out, in)
		}
		cp := ptr.Clone(message)
		cp.UserInputMultiContent = parts
		out[i] = cp
	}
	if out == nil {
		return in
	}
	return out
}
