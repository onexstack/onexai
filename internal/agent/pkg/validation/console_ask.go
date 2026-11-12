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

package validation

import (
	"strings"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ConsoleAsk is the subset of the shared AgentAskRequest a browser surface may
// control.
//
// # Why the shared message is read through this type
//
// Resolving the browser's fields is what keeps one wire contract honest. The
// signed face is called with AK/SK and its subject/actor assertions are covered
// by the signature; a browser holds a bearer token, so the same fields must
// never be trusted there. Rather than keep a second message that could drift,
// both console surfaces bind AgentAskRequest and pass it through this type,
// which rejects every field the surface owns and exposes only what the caller
// may choose. A field the browser sends instead of the server is a 400, not a
// value quietly discarded: silently ignoring identity makes an operator believe
// a run executed as somebody it did not.
//
// It lives in the validation package because the validator and the handler must
// read the same fields: a second extraction would let the two disagree about
// which requests are valid, which is the class of bug this boundary exists to
// prevent.
type ConsoleAsk struct {
	Agents       []string
	Text         string
	Images       []*v1.ImageRef
	Reasoning    *bool
	Capabilities *v1.CapabilitySelection
	Composition  *v1.AgentComposition
	SessionNonce string
}

// ReadConsoleAsk extracts the browser-controlled fields, refusing anything the
// surface owns.
//
// runId is deliberately absent from the refusal list. AgentAskRequest.Default
// mints one at bind time when the caller omitted it, so "the client sent a run
// id" and "the default ran" are indistinguishable here. Both handlers overwrite
// it with an identifier they minted (the dbg-/usr- prefixes are also what
// approve/stop check), so a supplied value cannot affect the run.
func ReadConsoleAsk(req *v1.AgentAskRequest) (ConsoleAsk, error) {
	if req.GetSubject() != nil || req.GetActor() != nil {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受身份字段（subject/actor），执行主体由登录态推导",
		)
	}
	if req.GetSessionId() != "" {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受 sessionId，会话桶由服务端推导",
		)
	}
	if req.GetMetering() != nil && req.GetMetering().GetMode() != v1.Metering_MODE_UNSPECIFIED {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受 metering，计量方式由服务端决定",
		)
	}
	if len(req.GetMetadata()) > 0 {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受 metadata",
		)
	}
	if req.GetOptions() != nil {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受执行策略（options）",
		)
	}
	if req.GetModel() != "" {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受 model，模型选择请使用 composition.model",
		)
	}

	selection := req.GetSelection()
	if len(selection.GetAgents()) > 1 {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台一次只能运行一个智能体",
		)
	}
	agents := make([]string, len(selection.GetAgents()))
	for i, name := range selection.GetAgents() {
		name = strings.TrimSpace(name)
		if name == "" {
			return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
				"agent 名称不能为空",
			)
		}
		agents[i] = name
	}
	if len(selection.GetInvokedSkills()) > 0 {
		return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
			"控制台接口不接受 invokedSkills",
		)
	}

	var reasoning *bool
	if generation := req.GetGeneration(); generation != nil {
		if generation.Temperature != nil || generation.MaxOutputTokens != nil ||
			generation.TopP != nil || len(generation.ProviderOptions) > 0 {
			return ConsoleAsk{}, errno.ErrInvalidArgument.WithMessage(
				"控制台接口只接受 generation.reasoning",
			)
		}
		reasoning = generation.Reasoning
	}

	return ConsoleAsk{
		Agents:       agents,
		Text:         req.GetInput().GetText(),
		Images:       req.GetInput().GetImages(),
		Reasoning:    reasoning,
		Capabilities: req.GetCapabilities(),
		Composition:  req.GetComposition(),
		SessionNonce: strings.TrimSpace(req.GetSessionNonce()),
	}, nil
}
