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

package bot

import (
	"errors"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// What a turn says when the backend refused it for credentials or
// authorization. The two are deliberately separate sentences: a 401 is this
// deployment's problem — nobody's permission would fix it — while a 403 is the
// asking user's, and only that one is worth telling them how to act on.
//
// The backend's own message is never shown. It is English, it names internal
// subject ids, and it exists to explain the refusal to an operator reading the
// log, not to be read by the person who was refused.
const (
	permissionBodyDenied = "🔒 **无法使用该 Agent**\n" +
		"你没有使用当前 Agent 的权限，本次请求未执行。如需开通，请联系管理员。"
	permissionPlainDenied = "权限不足：无法使用当前 Agent，本次请求未执行"

	permissionBodyUnauthorized = "🔒 **服务认证失败**\n" +
		"后台服务未能通过身份校验，本次请求未执行。请稍后重试或联系管理员。"
	permissionPlainUnauthorized = "服务认证失败，本次请求未执行"
)

// The footer markers for the same two refusals. They live here rather than with
// the decision reasons because they name what the backend did, not what a
// person chose: a refused turn has no decision behind it. They are constants
// because both the ask path and a resumed approval arrive at the same card.
const (
	reasonPermission   = "权限不足"
	reasonUnauthorized = "服务认证失败"
	// reasonQuotaExceeded marks a refusal with time on it: an allowance is
	// spent, or a run-lifecycle hook said no for a reason of its own. It is a
	// third sentence because it is neither of the other two — nothing about the
	// person's permission is at issue, and the deployment is working.
	reasonQuotaExceeded = "用量已达上限"
	// quotaBodyFallback is what a refusal with no sentence of its own shows. The
	// backend always sends one (see agent.ErrQuotaExceeded); this covers the
	// case where the body did not survive the trip, which is better than an
	// empty card.
	quotaBodyFallback = "当前账号的 AI 用量已达上限，请稍后再试。"
)

// permissionNotice is the card a refused turn produces, resolved from the
// backend error. It is a value rather than a function so the two call sites —
// the ask path and a resumed approval — draw the same card from the same words.
type permissionNotice struct {
	// body replaces the answer in the final card.
	body string
	// plain is the same thing as one line, for the TUI mirror and the
	// markdown fallback a failed close sends.
	plain string
	// reason is what the footer says became of the run.
	reason string
	// metric is the label recorded against the refusal.
	metric string
}

// classifyPermission resolves a backend refusal into the card it should
// produce, reporting false for anything that is not one — including the
// operational 5xx the same client returns, which is a failure and stays one.
func classifyPermission(err error) (permissionNotice, bool) {
	switch {
	case errors.Is(err, agent.ErrUnauthorized):
		return permissionNotice{
			body:   permissionBodyUnauthorized,
			plain:  permissionPlainUnauthorized,
			reason: reasonUnauthorized,
			metric: "backend_unauthenticated",
		}, true
	case errors.Is(err, agent.ErrPermissionDenied):
		return permissionNotice{
			body:   permissionBodyDenied,
			plain:  permissionPlainDenied,
			reason: reasonPermission,
			metric: "backend_permission_denied",
		}, true
	case errors.Is(err, agent.ErrQuotaExceeded):
		// The backend's own sentence is the card here, unlike the two above.
		// Those two are suppressed because theirs are English and name internal
		// subject ids; this one is written for the reader — by whichever side
		// knows which allowance ran out, including a run-lifecycle hook whose
		// whole job is to say so in the person's own terms. Replacing it with a
		// sentence of ours would throw away the only actionable line.
		message := agentclient.HTTPErrorMessage(err)
		if message == "" {
			message = quotaBodyFallback
		}
		return permissionNotice{
			body:   "⏳ **" + reasonQuotaExceeded + "**\n" + message,
			plain:  reasonQuotaExceeded + "：" + message,
			reason: reasonQuotaExceeded,
			metric: "backend_quota_exceeded",
		}, true
	default:
		return permissionNotice{}, false
	}
}
