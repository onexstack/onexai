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

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// runModelRouter is the stable model a built graph holds. It resolves the
// effective model from the run's context on every call, so one cached runner
// (keyed by agent set) can serve runs that selected different model endpoints.
//
// The graph itself must stay a function of the agent set: eino freezes a built
// ChatModelAgent, and the runner cache pins a paused run's graph. Putting the
// per-run model in the graph would make it part of the cache key and, worse,
// let one user's endpoint leak into another user's run. The context is the
// per-run seam eino already reads on every Generate/Stream call.
type runModelRouter struct {
	fallback einomodel.ToolCallingChatModel
	resolve  func(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error)
	tools    []*schema.ToolInfo
}

var _ einomodel.ToolCallingChatModel = (*runModelRouter)(nil)

// NewRunModelRouter builds the router. fallback is the deployment's model and
// is used when the run names no endpoint. resolve is the per-run resolver; a
// nil resolver leaves every run on the fallback.
func NewRunModelRouter(
	fallback einomodel.ToolCallingChatModel,
	resolve func(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error),
) *runModelRouter {
	return &runModelRouter{fallback: fallback, resolve: resolve}
}

// WithTools returns a copy carrying the tools. Tools are bound once per graph
// build; the copy keeps routing by context.
func (r *runModelRouter) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	cp := ptr.Clone(r)
	cp.tools = tools
	return cp, nil
}

// Generate delegates one call to the run's effective model.
func (r *runModelRouter) Generate(
	ctx context.Context, input []*schema.Message, opts ...einomodel.Option,
) (*schema.Message, error) {
	m, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	if len(r.tools) > 0 {
		if m, err = m.WithTools(r.tools); err != nil {
			return nil, err
		}
	}
	return m.Generate(ctx, input, opts...)
}

// Stream delegates one streaming call to the run's effective model.
func (r *runModelRouter) Stream(
	ctx context.Context, input []*schema.Message, opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m, err := r.pick(ctx)
	if err != nil {
		return nil, err
	}
	if len(r.tools) > 0 {
		if m, err = m.WithTools(r.tools); err != nil {
			return nil, err
		}
	}
	return m.Stream(ctx, input, opts...)
}

// pick returns the model this call must use.
func (r *runModelRouter) pick(ctx context.Context) (einomodel.ToolCallingChatModel, error) {
	if name := agentapi.ModelNameFromContext(ctx); name != "" && r.resolve != nil {
		m, err := r.resolve(ctx, name)
		if err != nil {
			return nil, err
		}
		if m != nil {
			return m, nil
		}
	}
	if r.fallback == nil {
		return nil, errno.ErrNotConfigured.WithMessage("no model is configured for this run")
	}
	return r.fallback, nil
}
