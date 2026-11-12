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

package lark

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
// and chose not to answer. That silence is why a card callback that never
// produced a reply looked exactly like one that was never sent, so every
// server that runs the SDK should pass one of these.
//
// The SDK formats its log lines itself (fmt.Sprintf before the call), so each
// argument list already holds one rendered string; the helpers below take the
// list apart only far enough to keep that string intact.
type SDKLogger struct{}

var _ larkcore.Logger = SDKLogger{}

// cardFrame is the prefix of the SDK's own log line for a card callback frame.
//
// Anchoring on the whole prefix rather than the message type alone keeps an
// event frame whose payload happens to contain the same words from matching.
const cardFrame = "receive message, message_type: card,"

// Debug logs a frame the SDK read, with one exception: a card callback frame
// is reported at Warn.
//
// The SDK reads those frames, logs them, and returns without writing a
// response (oapi-sdk-go/v3/ws/client.go: `case MessageTypeCard: return`), so
// the click is lost and Lark answers the person who made it with 200341. That
// makes the line evidence of a lost user action rather than a detail — at
// debug it would be hidden in exactly the runs where it matters, and a card
// frame is rare enough that warning on one cannot flood a log.
func (SDKLogger) Debug(ctx context.Context, args ...any) {
	line := unwrap(render(args))
	if strings.HasPrefix(line, cardFrame) {
		slog.WarnContext(ctx, "lark sdk received a card callback frame it cannot answer",
			"detail", line)
		return
	}
	slog.DebugContext(ctx, "lark sdk "+line)
}

// unwrap strips the brackets fmt.Sprint puts around a slice, because the SDK's
// per-frame call is the one place it forgets the `...` that would spread its
// argument list (`ws/client.go`, the Debug just above its frame switch): the
// line arrives as a slice rendered as one value — `[receive message, …]` —
// while the ping, connection and error lines arrive flat. Without this the
// frame line never matches cardFrame, and the one warning that explains a
// 200341 stays silent.
func unwrap(line string) string {
	if len(line) >= 2 && strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		return line[1 : len(line)-1]
	}
	return line
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

// connSecrets matches the credential query parameters of the connection URL
// the SDK logs when it connects. The access key and ticket in it authorise
// reading this app's event stream until the connection is torn down, so the
// values are replaced before the line reaches a log sink — being able to see
// *that* the connection came up is the point, not the key it came up with.
var connSecrets = regexp.MustCompile(`(access_key|ticket)=[^&\s]+`)

// render rebuilds the single line the SDK handed over. Joining the arguments
// with a space mirrors what the SDK's own logger does with them, and differs
// from fmt.Sprint only for a second argument that is not a string — the
// connection id the SDK appends is one, and would otherwise arrive glued to
// the end of the message.
func render(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	return connSecrets.ReplaceAllString(strings.Join(parts, " "), "$1=REDACTED")
}
