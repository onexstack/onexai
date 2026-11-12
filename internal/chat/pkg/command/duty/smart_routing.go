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

package duty

import (
	"context"
	"fmt"
	"strings"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// SmartRoutingModuleID is the synthetic duty module shown last in the
// /oncall module picker.
//
// It is not a Regen module and is deliberately never registered in the duty
// store: it has no watchers, so it stays out of /oncall --list and /duty, and
// selecting it means "I don't know which module owns this — pick one for me".
// The backend then asks the LLM to route the ticket to a real, staffed module
// (see resolveSmartRoutingModule).
const SmartRoutingModuleID = "smart-routing"

// smartRoutingPrompt is the one-shot instruction sent to oai-agent-apiserver. It has
// to carry everything — the module list, the question and the output contract —
// because the completion endpoint answers from the last user message alone and
// drops any system message.
const smartRoutingInstruction = `你是运维工单的路由助手。请从下面的值班模块中，为用户的问题选择最合适的一个。

值班模块列表（格式：模块ID | 模块名称 | 模块描述）：
%s
用户的问题：
%s

要求：只输出一个模块ID（必须严格来自上面的列表），不要输出任何解释或标点；如果都不合适，只输出 none。`

// resolveSmartRoutingModule asks oai-agent-apiserver which staffed module best fits
// the ticket title, and returns that module's code.
//
// Every failure — no AI client, a failed call, a `none` answer, or a code that
// is not an active module — is reported as a user error, so a smart-routed
// ticket is never created against a guessed module. The call is deliberately
// stateless (no SessionID), so the routing question neither reads nor pollutes
// the user's conversation history.
func resolveSmartRoutingModule(ctx context.Context, sr *runtime.StaticRuntime, title string) (string, error) {
	groups, err := sr.Duty.ActiveGroupInfos(ctx)
	if err != nil {
		return "", runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	if len(groups) == 0 {
		if rosterUnavailable(sr) {
			// The module list is not empty, it is unstaffed as far as this
			// process can tell: the roster backend could not be read, so the
			// answer is "try again", not "there are no modules".
			return "", runtime.NewUserError("%s", groupServiceUnavailableMessage())
		}
		return "", runtime.NewUserError("当前没有可用的值班模块")
	}
	if sr.Clientset == nil || sr.Clientset.Agent() == nil {
		return "", runtime.NewUserError("AI 服务不可用，无法自动匹配值班模块，请使用 /oncall --list 选择后重试")
	}

	resp, err := sr.Clientset.Agent().ChatCompletions(ctx, &llmv1.ChatCompletionRequest{
		// A routing decision needs the catalog and nothing else: NoAgent is the
		// far side's explicit request for the builtin assistant, so no agent's
		// instruction or tool set can influence which module is picked.
		Agent: llmv1.NoAgent,
		Messages: []*llmv1.ChatCompletionMessage{{
			Role:    "user",
			Content: fmt.Sprintf(smartRoutingInstruction, moduleCatalog(groups), title),
		}},
		Temperature: 0,
	})
	if err != nil {
		return "", runtime.NewUserError("自动匹配值班模块失败，请使用 /oncall --list 选择后重试")
	}

	activeIDs, err := sr.Duty.ActiveGroupIDs(ctx)
	if err != nil {
		return "", runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	code := matchActiveModule(firstChoiceContent(resp), activeIDs)
	if code == "" {
		return "", runtime.NewUserError("无法自动匹配值班模块，请使用 /oncall --list 选择后重试")
	}
	return code, nil
}

// moduleCatalog renders the candidate modules as one line each, flattened so a
// multi-line description cannot be mistaken for the next module.
func moduleCatalog(groups []dutystore.Group) string {
	var sb strings.Builder
	for _, g := range groups {
		desc := g.ShortDesc
		if desc == "" {
			desc = g.LongDesc
		}
		desc = strings.NewReplacer("\n", " ", "\r", " ").Replace(desc)
		fmt.Fprintf(&sb, "- %s | %s | %s\n", g.ID, g.Name, desc)
	}
	return sb.String()
}

// matchActiveModule extracts a module code from the model's answer and returns
// it only when it names an active module. It tokenizes on anything outside a
// module code's alphabet, so "推荐 eks" (recommended eks), "eks（容器平台）" (eks (container platform)) and "`eks`" all
// resolve, while an invented or absent code resolves to "".
func matchActiveModule(answer string, active map[string]struct{}) string {
	tokens := strings.FieldsFunc(strings.ToLower(answer), func(r rune) bool {
		isCodeChar := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		return !isCodeChar
	})
	for _, tok := range tokens {
		if _, ok := active[tok]; ok {
			return tok
		}
	}
	return ""
}

// firstChoiceContent returns the first completion's text, tolerating a nil
// response or an empty choice list.
func firstChoiceContent(resp *llmv1.ChatCompletionResponse) string {
	choices := resp.GetChoices()
	if len(choices) == 0 {
		return ""
	}
	return choices[0].GetMessage().GetContent()
}
