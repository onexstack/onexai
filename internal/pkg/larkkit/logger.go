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

package larkkit

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// SDKLogger adapts the Lark SDK's logger onto slog.
//
// The SDK is silent unless it is handed a logger: it reports nothing about the
// long connection — not a failed connect, not a reconnect, not a frame it read
// and chose not to answer. A service running the SDK without one cannot be
// told apart from a service that is not running it at all.
//
// chat keeps its own adapter beside this one because it adds a case this
// package has no business knowing about: a card-callback frame the SDK silently
// drops, which it promotes to a warning. Everything else — the slog mapping and
// the redaction below — is the same, so a change to either belongs here.
type SDKLogger struct{}

var _ larkcore.Logger = SDKLogger{}

// Debug logs a frame or detail the SDK read.
func (SDKLogger) Debug(ctx context.Context, args ...any) {
	slog.DebugContext(ctx, "lark sdk "+render(args))
}

// Info logs a lifecycle message from the SDK.
func (SDKLogger) Info(ctx context.Context, args ...any) {
	slog.InfoContext(ctx, "lark sdk "+render(args))
}

// Warn logs a warning from the SDK.
func (SDKLogger) Warn(ctx context.Context, args ...any) {
	slog.WarnContext(ctx, "lark sdk "+render(args))
}

// Error logs an error from the SDK.
func (SDKLogger) Error(ctx context.Context, args ...any) {
	slog.ErrorContext(ctx, "lark sdk "+render(args))
}

// connSecrets matches the credential query parameters of the connection URL the
// SDK logs when it connects. The access key and ticket in it authorise reading
// this application's event stream until the connection is torn down, so the
// values are replaced before the line reaches a log sink — seeing *that* the
// connection came up is the point, not the key it came up with.
var connSecrets = regexp.MustCompile(`(access_key|ticket)=[^&\s]+`)

// render rebuilds the single line the SDK handed over. The SDK formats its own
// log lines (fmt.Sprintf before the call) and sometimes forgets to spread the
// argument list, so a slice arrives rendered as one value; joining the parts
// with a space leaves a well-formed line either way.
func render(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	return connSecrets.ReplaceAllString(strings.Join(parts, " "), "$1=REDACTED")
}
