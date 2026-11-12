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

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
)

// UserError represents errors caused by user input or business rule rejections.
// Commands distinguish UserError from internal errors so the dispatcher can
// reply with the message verbatim (rather than a trace id).
type UserError struct {
	message string
}

func (e *UserError) Error() string { return e.message }

// NewUserError creates a UserError.
func NewUserError(format string, args ...any) *UserError {
	return &UserError{message: fmt.Sprintf(format, args...)}
}

// IsUserError checks if err is a UserError.
func IsUserError(err error) bool {
	var ue *UserError
	return errors.As(err, &ue)
}

// Predefined user-facing errors for the duty/ticket commands.
var (
	ErrMissingArgs            = NewUserError("缺少必需参数")
	ErrTooManyArgs            = NewUserError("参数过多")
	ErrInvalidArgs            = NewUserError("参数格式无效")
	ErrInvalidLevel           = NewUserError("级别必须是 P1、P2、P3 或 P4")
	ErrInvalidStatus          = NewUserError("状态必须是：打开、处理中、已解决、已关闭 其中之一")
	ErrGroupRequired          = NewUserError("必须提供值班组，例如：eks/gateway/monitor/bke/unknown")
	ErrGroupOnlyCommand       = "抱歉，命令 %s 只能在工单群（Lark 群）中执行"
	ErrP2POnlyCommand         = "抱歉，命令 %s 只能在机器人私聊中执行"
	ErrSREOnlyCommand         = "命令 %s 只能被 SRE 团队成员使用"
	ErrTicketAlreadyExists    = "此群已关联报障单%s，请勿重复提交"
	ErrTicketClosed           = "工单已经关闭，无法执行此命令。"
	ErrHandoverTargetRequired = "handover target group is required"
	ErrStatusRequired         = "status is required"
	ErrRateLimitExceeded      = "未知错误，请联系 Helper 负责人解决"
	ErrUserBlocked            = NewUserError("使用权限不足")
)

// GoSafe starts a goroutine with panic recovery protection. source names the
// call site, e.g. "lark.message.dispatch"; it is logged and used as a metric
// label.
func GoSafe(ctx context.Context, source string, fn func()) {
	go func() {
		defer RecoverPanic(ctx, source)
		fn()
	}()
}

// RecoverPanic recovers a panic in the current goroutine via defer, then logs
// and counts it. Use it directly (instead of GoSafe) when the recovery site
// also has to notify someone, e.g. reply to the Lark user who triggered it.
func RecoverPanic(ctx context.Context, source string) {
	if r := recover(); r != nil {
		LogRecoveredPanic(ctx, source, r)
	}
}

// LogRecoveredPanic reports an already-recovered panic value.
//
// Recovering keeps the process alive, which also means the failure no longer
// announces itself through a restart: the stack trace here and the
// opsassist_panic_recovered_total metric are the only signal left, so both are
// emitted unconditionally.
func LogRecoveredPanic(ctx context.Context, source string, r any) {
	slog.ErrorContext(ctx, "panic recovered",
		"source", source,
		"panic", fmt.Sprint(r),
		"stack", string(debug.Stack()),
	)
	metrics.M.RecordPanicRecovered(ctx, source)
}
