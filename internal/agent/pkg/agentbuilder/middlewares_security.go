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
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/datamask"

	"github.com/onexstack/onexai/internal/agent/pkg/modeljson"
)

// securityRedactedNotice is appended to an answer that was rewritten, so the
// reader knows the text in front of them is not verbatim — a silently altered
// answer is worse than none, because it will be trusted.
const securityRedactedNotice = "\n\n> ⚠️ 若安全脱敏为误报，请联系管理员"

// reviewerTimeout bounds one review call. It is longer than the inbound
// audit's budget because it runs after the answer exists rather than in front
// of the caller's first token.
const reviewerTimeout = 30 * time.Second

// SecurityReviewer builds the middleware that screens an answer before it
// leaves the service. It is implemented in this package and lives behind an
// interface only so the builder can be handed a nil one.
type SecurityReviewer interface {
	Middleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error)
}

// securityReview is the outbound counterpart of the inbound audit: it runs on
// the finished answer, after every model round, and rewrites it if it carries
// something that must not leave.
//
// Two layers, in order, because they catch different things:
//
//  1. Pattern redaction (datamask) catches credentials by shape. It is
//     deterministic and cannot be talked out of its verdict.
//  2. A reviewer model catches what has no shape — a customer record pasted
//     into prose, an internal figure quoted in a summary.
//
// The second layer is best-effort: a reviewer that is unavailable, slow, or
// answers with something unparsable leaves the first layer's result in place
// rather than failing the answer.
type securityReview struct {
	reviewer model.BaseChatModel
}

// NewSecurityReview returns a SecurityReviewer backed by the given model, or
// nil when no model was supplied (which disables the feature).
func NewSecurityReview(reviewer model.BaseChatModel) SecurityReviewer {
	if reviewer == nil {
		return nil
	}
	return &securityReview{reviewer: reviewer}
}

// Middleware returns the handler.
func (s *securityReview) Middleware(context.Context) (adk.ChatModelAgentMiddleware, error) {
	return &securityReviewMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		reviewer:                     s.reviewer,
	}, nil
}

// securityReviewMiddleware rewrites the final assistant message in place.
type securityReviewMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	reviewer model.BaseChatModel
}

// AfterAgent rewrites the answer when it carried something to redact.
//
// It looks for the last assistant message with no tool calls — the answer, as
// opposed to the reasoning steps that precede it. Rewriting a step would leave
// the tool call that depends on it inconsistent; the answer is what leaves the
// service.
func (m *securityReviewMiddleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if state == nil {
		return ctx, nil
	}
	idx := lastAnswerIndex(state.Messages)
	if idx < 0 {
		return ctx, nil
	}

	original := state.Messages[idx].Content
	redacted := datamask.RedactSensitive(original)
	reviewed, rewritten := m.review(ctx, redacted)
	if redacted == original && !rewritten {
		return ctx, nil
	}
	state.Messages[idx].Content = reviewed + securityRedactedNotice
	return ctx, nil
}

// lastAnswerIndex returns the index of the final answer message, or -1.
func lastAnswerIndex(messages []*schema.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) == 0 && message.Content != "" {
			return i
		}
	}
	return -1
}

// reviewerResponse is the reviewer's structured verdict.
type reviewerResponse struct {
	Triggered bool   `json:"triggered"`
	Content   string `json:"content"`
}

// reviewPrompt asks the reviewer to rewrite the answer if, and only if, it
// carries something that must not leave. The "do not trigger" list matters as
// much as the rest: a reviewer that redacts every IP address makes the product
// useless for the people who need to read one.
const reviewPrompt = `你是安全合规专家，对 AI 助手的回复做最终检查。

判断标准（必须同时满足才算需要处理）：
- 内容是真实的、具体可用的凭据或机密数据（非示例、占位符、文档说明）
- 一旦泄露会直接造成安全事故或违反监管规定

需要处理的类型（仅限以下几类）：
- 明文密钥/密码/token（数据库密码、API secret、私钥内容）
- 真实用户的个人身份信息（身份证、手机号、银行卡号等）
- 核心业务数据（账户余额、真实交易流水、订单数据）

不需要处理（不得误报）：
- IP 地址、端口、域名、资源名、环境变量名称
- 配置文件路径、命令行参数、日志格式
- 示例值、占位符、已显示为 [REDACTED:xxx] 的内容
- 一般运营统计数据、监控指标、告警阈值

严格返回 JSON，不要有任何其他内容。
- 无需处理：{"triggered": false, "content": ""}
- 需要处理：{"triggered": true, "content": "<用 [REDACTED] 替换敏感值后的完整文本>"}

回复内容：
%s`

// review runs the reviewer model. It reports the (possibly rewritten) text and
// whether the reviewer rewrote it; every failure returns the input unchanged.
func (m *securityReviewMiddleware) review(ctx context.Context, text string) (string, bool) {
	if text == "" {
		return text, false
	}
	ctx, cancel := context.WithTimeout(ctx, reviewerTimeout)
	defer cancel()

	resp, err := m.reviewer.Generate(ctx, []*schema.Message{
		schema.UserMessage(fmt.Sprintf(reviewPrompt, text)),
	})
	if err != nil || resp == nil {
		slog.WarnContext(ctx, "[security-review] reviewer failed; keeping the pattern-redacted text", "error", err)
		return text, false
	}

	var verdict reviewerResponse
	if err := json.Unmarshal([]byte(modeljson.StripFence(resp.Content)), &verdict); err != nil {
		slog.WarnContext(ctx, "[security-review] unparsable verdict; keeping the pattern-redacted text", "error", err)
		return text, false
	}
	if !verdict.Triggered || verdict.Content == "" {
		return text, false
	}
	return verdict.Content, true
}
