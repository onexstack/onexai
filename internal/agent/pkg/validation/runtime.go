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
	"context"
	"errors"
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/onexstack/onexai/internal/agent/pkg/mediaparse"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ValidateChatCompletionRequest checks the OpenAI-compatible completion request.
//
// The agent is required: this endpoint runs an agent, and `model` names a model
// endpoint rather than an agent, so there is no fallback. A request that names
// no agent would otherwise run the builtin assistant, which has no instruction,
// no MCP servers and no knowledge bases — and the caller would have no way to
// tell that from a run they configured.
func (v *Validator) ValidateChatCompletionRequest(ctx context.Context, rq *v1.ChatCompletionRequest) error {
	// Normalize the identifiers here so the handler can address the request
	// as-is. A validator receives the request pointer, so the trimmed values
	// are what every later step reads.
	rq.Agent = strings.TrimSpace(rq.GetAgent()) //nolint:protogetter // writing the request field
	rq.Model = strings.TrimSpace(rq.GetModel()) //nolint:protogetter // writing the request field

	if rq.GetAgent() == "" {
		return errno.ErrInvalidArgument.WithMessage("agent is required")
	}

	if err := validateCompletionMessages(rq.GetMessages()); err != nil {
		return err
	}

	return v.requireAgentExists(ctx, rq.GetAgent())
}

// validateCompletionMessages checks that the request carries a user turn.
func validateCompletionMessages(messages []*v1.ChatCompletionMessage) error {
	question, images, err := lastUserMessage(messages)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}

	if question == "" && len(images) == 0 {
		return errno.ErrInvalidArgument.WithMessage("messages must contain a user message")
	}

	return nil
}

// requireAgentExists refuses a name that resolves to nothing. The engine skips
// unknown names, so a typo would otherwise complete with no instruction and no
// tools while looking like a normal answer. NoAgent is the one value that is not
// a row name: it asks for the builtin assistant.
func (v *Validator) requireAgentExists(ctx context.Context, agentName string) error {
	if v.resources == nil || agentName == v1.NoAgent {
		return nil
	}

	if _, err := v.resources.Agents().Get(ctx, where.F("name", agentName)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errno.ErrNotFound.WithMessage("agent %q not found", agentName)
		}

		return err
	}

	return nil
}

// ValidateAgentAskRequest checks the backend-neutral ask request.
//
// The signed face is the trusted one: it accepts the identity and lifecycle
// fields. It is the console-only run controls that it refuses, because those
// exist for the browser surfaces and a signed caller that sent them would be
// relying on behaviour nobody exercises.
func (v *Validator) ValidateAgentAskRequest(_ context.Context, rq *v1.AgentAskRequest) error {
	if _, err := mediaparse.FromRequest(nil, rq.GetInput().GetImages()); err != nil {
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}

	if rq.GetCapabilities() != nil || rq.GetComposition() != nil ||
		strings.TrimSpace(rq.GetSessionNonce()) != "" {
		return errno.ErrInvalidArgument.WithMessage(
			"capabilities/composition/sessionNonce 只用于控制台接口",
		)
	}

	return nil
}

// ValidateAgentApproveRequest checks the approval body. RequestID and Decision
// are deliberately not required here: the business layer answers 410 for a
// pause the run has already moved past, and that is a state question rather
// than a malformed request.
func (v *Validator) ValidateAgentApproveRequest(_ context.Context, rq *v1.AgentApproveRequest) error {
	if strings.TrimSpace(rq.GetRunID()) == "" {
		return errno.ErrInvalidArgument.WithMessage("runID is required")
	}

	return nil
}

// ValidateAgentStopRequest checks the stop body.
func (v *Validator) ValidateAgentStopRequest(_ context.Context, rq *v1.AgentStopRequest) error {
	if strings.TrimSpace(rq.GetRunID()) == "" {
		return errno.ErrInvalidArgument.WithMessage("runID is required")
	}

	return nil
}

// ValidateAdminAskRequest checks the management debug-run request.
//
// The management surface always requires a real agent: its contract is that the
// run executes as the agent's owner. A hostless playground run is a
// self-service concern and goes through ValidateUserAskRequest instead.
func (v *Validator) ValidateAdminAskRequest(_ context.Context, rq *v1.AgentAskRequest) error {
	ask, err := ReadConsoleAsk(rq)
	if err != nil {
		return err
	}

	if len(ask.Agents) != 1 {
		return errno.ErrInvalidArgument.WithMessage("agent is required: a debug run names exactly one agent")
	}
	if ask.Agents[0] == v1.NoAgent {
		// "default" names the builtin assistant, which has no owner to execute
		// as; the management surface's contract is "execute as the agent owner".
		return errno.ErrInvalidArgument.WithMessage("内置助手不能作为管理面调试对象")
	}

	return validateConsoleAsk(ask)
}

// ValidateUserAskRequest checks a self-service run request.
//
// Unlike the management surface, the self-service surface may omit the agent
// when a composition is present. That is the hostless playground: the caller is
// the execution subject, there is no agent row to authorize against, and the
// composition is the whole configuration. An empty agent without a composition
// has no meaning and is refused.
func (v *Validator) ValidateUserAskRequest(_ context.Context, rq *v1.AgentAskRequest) error {
	ask, err := ReadConsoleAsk(rq)
	if err != nil {
		return err
	}

	builtin := len(ask.Agents) == 1 && ask.Agents[0] == v1.NoAgent
	if len(ask.Agents) == 0 && ask.Composition == nil {
		return errno.ErrInvalidArgument.WithMessage("agent is required unless composition is present")
	}

	if err := validateConsoleAsk(ask); err != nil {
		return err
	}

	if builtin && len(ask.Images) > 0 {
		// The builtin branch is a stateless text generation used by smart-create;
		// it has no instruction or tools, so an image has nothing to reach. A 400
		// is better than letting the caller believe it was seen.
		return errno.ErrInvalidArgument.WithMessage("内置助手不支持图片")
	}

	return nil
}

// validateConsoleAsk is shared by both browser run surfaces.
func validateConsoleAsk(ask ConsoleAsk) error {
	if strings.TrimSpace(ask.Text) == "" {
		return errno.ErrInvalidArgument.WithMessage("question is required")
	}
	if _, err := mediaparse.FromRequest(nil, ask.Images); err != nil {
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}

	return validateConsoleComposition(ask)
}

// validateConsoleComposition checks the optional replacement set.
//
// A composition and a capability narrowing answer different questions — one
// replaces the host's declaration, the other takes a subset of it — so sending
// both is ambiguous rather than "the narrower wins". The nonce is required
// because it is what keeps two conversations with the same combination from
// sharing a memory bucket.
func validateConsoleComposition(ask ConsoleAsk) error {
	comp := ask.Composition
	if comp == nil {
		// A stray nonce without a composition has no meaning; ignoring it is
		// safer than silently opening a session the caller did not ask for.
		return nil
	}
	if ask.Capabilities != nil {
		return errno.ErrInvalidArgument.WithMessage("capabilities 与 composition 不能同时使用")
	}
	if len(ask.Agents) == 1 && ask.Agents[0] == v1.NoAgent {
		// "default" is the builtin assistant, which has no resources to
		// authorize a composition against. Hostless composition is expressed by
		// leaving the selection empty on the self-service surface, not by using
		// the builtin sentinel.
		return errno.ErrInvalidArgument.WithMessage(
			"内置助手不支持能力组合；无宿主组合调试请省略 agent",
		)
	}

	nonce := ask.SessionNonce
	if nonce == "" {
		return errno.ErrInvalidArgument.WithMessage("composition 缺少 sessionNonce")
	}
	if len(nonce) > 128 {
		return errno.ErrInvalidArgument.WithMessage("sessionNonce 过长，最多 128 字节")
	}
	for _, r := range nonce {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			continue
		}
		return errno.ErrInvalidArgument.WithMessage("sessionNonce 只允许字母、数字和 - _ . ~")
	}

	var err error
	if comp.Prompts, err = normalizeRequestNames("prompt", comp.GetPrompts(), 50); err != nil {
		return err
	}
	if comp.Skills, err = normalizeRequestNames("skill", comp.GetSkills(), 100); err != nil {
		return err
	}
	if comp.Mcps, err = normalizeRequestNames("mcp", comp.GetMcps(), 100); err != nil {
		return err
	}
	if comp.KnowledgeBases, err = normalizeRequestNames("knowledgeBase", comp.GetKnowledgeBases(), 100); err != nil {
		return err
	}
	if comp.CustomCommands, err = normalizeRequestNames("customCommand", comp.GetCustomCommands(), 100); err != nil {
		return err
	}
	comp.Model = strings.TrimSpace(comp.GetModel()) //nolint:protogetter // writing the request field
	if len(comp.Model) > 128 {
		return errno.ErrInvalidArgument.WithMessage("model 过长，最多 128 字节")
	}
	comp.Namespace = strings.TrimSpace(comp.GetNamespace()) //nolint:protogetter // writing the request field
	if comp.Namespace != "" {
		if msgs := k8svalidation.IsDNS1123Label(comp.Namespace); len(msgs) > 0 {
			return errno.ErrInvalidArgument.WithMessage(
				"composition.namespace %q 不合法：%s", comp.Namespace, msgs[0])
		}
	}
	return nil
}

// normalizeRequestNames trims and deduplicates one composition list.
//
// A blank entry is refused rather than dropped: it names nothing, and a caller
// that sent it is looking at a different list than the one being validated.
func normalizeRequestNames(kind string, names []string, limit int) ([]string, error) {
	if len(names) > limit {
		return nil, errno.ErrInvalidArgument.WithMessage("%s 列表过长，最多 %d 项", kind, limit)
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errno.ErrInvalidArgument.WithMessage("%s 名称不能为空", kind)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// ValidateImageGenerationRequest checks the image endpoint.
func (v *Validator) ValidateImageGenerationRequest(_ context.Context, rq *v1.ImageGenerationRequest) error {
	if strings.TrimSpace(rq.GetPrompt()) == "" {
		return errno.ErrInvalidArgument.WithMessage("prompt is required")
	}

	return nil
}

// lastUserMessage returns the last user turn's text and image references. It is
// shared with the handler's own use of the same fields so the two cannot
// disagree about which turn the request is asking about.
func lastUserMessage(messages []*v1.ChatCompletionMessage) (string, []media.Ref, error) {
	question := ""
	var images []media.Ref

	for _, m := range messages {
		if m.GetRole() != agent.RoleUser {
			continue
		}

		question = m.GetContent()
		refs, err := mediaparse.FromRequest(m.GetImages(), m.GetImageRefs())
		if err != nil {
			return "", nil, err
		}

		images = refs
	}

	return question, images, nil
}
