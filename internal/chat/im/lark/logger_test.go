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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// captureLogger installs a slog default that writes into a buffer and restores
// the previous one when the test ends.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return &buf
}

// TestSDKLoggerCardFrameIsAWarning pins the one line that explains a 200341: a
// card callback frame the SDK will not answer. It has to survive an info-level
// log, because that is the level the service runs at.
func TestSDKLoggerCardFrameIsAWarning(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	SDKLogger{}.Debug(context.Background(), frameArgs("card"))

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("card frame was not reported at WARN: %q", out)
	}
	if !strings.Contains(out, "message_type: card") {
		t.Fatalf("card frame detail was dropped: %q", out)
	}
}

// frameArgs builds the argument list the SDK passes for a frame, which is a
// slice rather than the spread arguments every other call site uses. Passing it
// as one value is what makes fmt.Sprint wrap the line in brackets, so a test
// that spelled the line out flat would pass while the real thing never matched.
func frameArgs(messageType string) []any {
	return []any{"receive message, message_type: " + messageType +
		", message_id: om_x, trace_id: t_x, payload: {}", "[conn_id=abc]"}
}

// TestSDKLoggerEventFrameStaysDebug keeps the per-frame noise at debug: an
// event frame is the SDK working as intended, and it carries message content.
func TestSDKLoggerEventFrameStaysDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	SDKLogger{}.Debug(context.Background(), frameArgs("event"))

	if out := buf.String(); out != "" {
		t.Fatalf("an event frame was logged at info level: %q", out)
	}
}

// TestSDKLoggerFrameLineShape pins the adapter against the shape the SDK
// actually produces. The brackets are not decoration: a frame line arrives as a
// slice rendered as one value, and reading that as the message is what made an
// earlier version of this file miss every card frame.
func TestSDKLoggerFrameLineShape(t *testing.T) {
	buf := captureLogger(t)

	SDKLogger{}.Debug(context.Background(), frameArgs("event"))

	out := buf.String()
	if !strings.Contains(out, "lark sdk receive message, message_type: event,") {
		t.Fatalf("frame line kept its slice brackets: %q", out)
	}
	if strings.Contains(out, "sdk [") {
		t.Fatalf("frame line was logged wrapped: %q", out)
	}
}

// TestSDKLoggerPayloadCannotForgeACardFrame guards the prefix match: a message
// body may contain the same words, and a frame carrying one is not a lost
// click.
func TestSDKLoggerPayloadCannotForgeACardFrame(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	SDKLogger{}.Debug(context.Background(), []any{
		`receive message, message_type: event, message_id: om_x, trace_id: t_x, ` +
			`payload: {"text":"receive message, message_type: card,"}`,
		"[conn_id=abc]",
	})

	if out := buf.String(); out != "" {
		t.Fatalf("a card frame was forged by a payload: %q", out)
	}
}

// TestSDKLoggerKeepsConnectionID checks that the connection id the SDK appends
// arrives separated from the message rather than glued to its end.
func TestSDKLoggerKeepsConnectionID(t *testing.T) {
	buf := captureLogger(t)

	SDKLogger{}.Info(context.Background(), "ping success", "[conn_id=abc]")

	out := buf.String()
	if !strings.Contains(out, "ping success [conn_id=abc]") {
		t.Fatalf("connection id was mangled: %q", out)
	}
}

// TestSDKLoggerRedactsConnectionSecrets checks that the credentials in the
// connection URL the SDK logs do not reach the log sink.
func TestSDKLoggerRedactsConnectionSecrets(t *testing.T) {
	buf := captureLogger(t)

	SDKLogger{}.Info(context.Background(),
		`connected to wss://msg-frontier.feishu.cn/ws/v2?fpid=493&aid=552564&access_key=c76a18&service_id=33&ticket=50d05331-abc`,
		"[conn_id=769]")

	out := buf.String()
	for _, leaked := range []string{"c76a18", "50d05331-abc"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("credential %q reached the log: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "access_key=REDACTED") || !strings.Contains(out, "ticket=REDACTED") {
		t.Fatalf("credentials were not redacted in place: %q", out)
	}
	// The rest of the URL is diagnostic and must survive.
	if !strings.Contains(out, "wss://msg-frontier.feishu.cn/ws/v2?fpid=493") {
		t.Fatalf("redaction took too much: %q", out)
	}
}

// TestSDKLoggerLevels checks the level mapping for the remaining verbs. The
// SDK renders its own lines before the call, so each argument list holds the
// finished string — except for the reconnect error, which it passes bare.
func TestSDKLoggerLevels(t *testing.T) {
	buf := captureLogger(t)

	SDKLogger{}.Warn(context.Background(), "ping failed, err: EOF")
	SDKLogger{}.Error(context.Background(), "connect failed, err: boom")
	SDKLogger{}.Error(context.Background(), errors.New("reconnect failed"))

	out := buf.String()
	wants := []string{
		"level=WARN", "ping failed, err: EOF",
		"level=ERROR", "connect failed, err: boom",
		"reconnect failed",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}
