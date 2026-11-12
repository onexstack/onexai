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

// Package agentcommand serves the read-only view of command grants.
//
// Commands are granted by AgentBinding objects, which the agent service owns and
// validates. This package keeps the one question chat can answer better than any
// other service — "what does a conversation's agent actually have right now" —
// and deliberately has no write path: a second way to grant commands would be a
// second authorization model.
package agentcommand

import (
	"context"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// ProviderSet defines Wire DI providers for the agent command biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(AgentCommandBiz), new(*agentCommandBiz)))

// AgentCommandBiz defines the command-grant reporting business logic.
type AgentCommandBiz interface {
	// Coverage reports what the stored bindings grant every agent the service
	// knows. It is the only way to see a grant that was never used: commands are
	// deny-by-default, so an agent no binding covers is invisible until somebody
	// tries to use one.
	Coverage(ctx context.Context) (*agentcmd.Coverage, error)
}

// agentCommandBiz is the concrete implementation of AgentCommandBiz.
type agentCommandBiz struct {
	res *agentcmd.Resolver
}

var _ AgentCommandBiz = (*agentCommandBiz)(nil)

// New constructs an agentCommandBiz over the resolver.
func New(res *agentcmd.Resolver) *agentCommandBiz {
	return &agentCommandBiz{res: res}
}

// Coverage reports what the stored rules grant every agent the service knows.
//
// It goes through the resolver rather than re-deriving the answer here: the
// report has to be the command set a conversation would actually be handed, and
// a second implementation of the same rules would be right until the day the
// two disagreed.
func (b *agentCommandBiz) Coverage(ctx context.Context) (*agentcmd.Coverage, error) {
	coverage, err := b.res.CoverageOf(ctx)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return coverage, nil
}
