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

package runtime

import "strings"

// AgentHint names why a scheduling pass produced no agent. The zero value means
// it produced at least one.
//
// It lives here rather than beside the scheduler because two packages need the
// same word for the same state and neither may import the other: the scheduler
// lives in the bot, which imports the command package, so the answer crosses the
// boundary on DynamicRuntime.ResolvedAgents — and the reason crosses with it.
type AgentHint string

const (
	// AgentHintNone is the success case.
	AgentHintNone AgentHint = ""
	// AgentHintUnbound: the conversation asked for `@peer` and this speaker has no
	// binding. The reply is an invitation to bind.
	AgentHintUnbound AgentHint = "unbound"
	// AgentHintNoAgent: the speaker is bound but nothing is usable — no membership,
	// or one that lapsed. The reply is an invitation to subscribe.
	AgentHintNoAgent AgentHint = "no-agent"
	// AgentHintUnresolved: nothing else applies. The default agent may simply not
	// be configured for this audience, which is the state of a fresh deployment.
	AgentHintUnresolved AgentHint = "unresolved"
)

// Notice words the three reasons a request has no agent.
//
// They are worded apart because the reader has to do three different things:
// bind, subscribe, or ask an administrator. One message for all three would be
// one that nobody can act on.
func (h AgentHint) Notice() string {
	switch h {
	case AgentHintUnbound:
		return "我还没有你的 OneX 账号，绑定后才能用你自己的助手回答。控制台 → 账号 → 安全设置里有「绑定飞书」。"
	case AgentHintNoAgent:
		return "你的 OneX 账号暂时没有可用的助手：可能是会员未开通或已到期。开通后可直接在这里提问。"
	default:
		return "这个会话还没有配置可用的助手，请联系管理员。"
	}
}

// AgentResolution is what one scheduling pass settled on.
//
// It is the projection of the scheduler's own answer that crosses the bot →
// command boundary: the bot owns the scheduler and imports the command package,
// so a command can only be told the result, never ask for it directly.
type AgentResolution struct {
	// Refs is the declaration the pass consumed, after the conversation type's
	// template was applied. It travels with the result because "why is it this
	// agent" is the next question every reader asks, and only the scheduler knows
	// whether that answer was written down or inherited from the template.
	Refs []string
	// Agents is the final set, in order. Empty is a state Hint explains.
	Agents []string
	// Hint names why Agents is empty; AgentHintNone when it is not.
	Hint AgentHint
	// BindingPreferred reports that Refs came from a private chat's speaker
	// binding rather than from the conversation's declaration. Only the
	// scheduler can know this, and a command that just wrote a declaration would
	// otherwise report it as the one in effect.
	BindingPreferred bool
}

// Line renders the resolution as the one line /agent reports, so the command and
// the card that show the same thing cannot disagree about it.
//
// It is presentation rather than data, and it sits beside the resolution because
// the alternative — each caller wording it — is how two wordings drift apart.
func (r AgentResolution) Line() string {
	if len(r.Agents) == 0 {
		return "此刻解析不出 Agent。" + r.Hint.Notice()
	}
	quoted := make([]string, 0, len(r.Agents))
	for _, a := range r.Agents {
		quoted = append(quoted, "`"+a+"`")
	}
	return "此刻按你的身份解析为：" + strings.Join(quoted, "、")
}
