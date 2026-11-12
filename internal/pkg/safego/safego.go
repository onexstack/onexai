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

// Package safego runs the goroutines that carry a request.
//
// The HTTP layer already recovers panics, but a request's work outlives the
// handler that started it: a run streams from its own goroutine so it can keep
// going after the request returns. A panic in one of those is not caught by any
// middleware — it unwinds the whole process, taking every other conversation
// with it. This is the recovery that makes one bad turn a failed turn.
package safego

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

// panicReporter, when set, is called for each recovered panic.
//
// A hook rather than an import: this package is a leaf on purpose, and making
// it depend on a metrics package would put the application's observability
// stack underneath every caller that only wants a goroutine that cannot take
// the process down.
var panicReporter atomic.Pointer[func(ctx context.Context, where string)]

// OnPanic registers the reporter. It is set once at startup.
func OnPanic(fn func(ctx context.Context, where string)) {
	if fn == nil {
		return
	}
	panicReporter.Store(&fn)
}

// Go runs fn in a new goroutine, recovering a panic and logging it.
//
// name identifies what was running, because the stack alone rarely says which
// conversation or which agent the panic belonged to.
func Go(ctx context.Context, name string, fn func()) {
	go func() {
		defer Recover(ctx, name)
		fn()
	}()
}

// Recover turns a panic in flight into a log line. Call it with defer at the
// top of a goroutine's body; it is a no-op when nothing is panicking.
func Recover(ctx context.Context, name string) {
	r := recover()
	if r == nil {
		return
	}
	// The stack is the only thing that says where it happened, and this is the
	// one place it is available.
	slog.ErrorContext(ctx, "recovered from a panic; the turn is lost but the process continues",
		"where", name, "panic", r, "stack", string(debug.Stack()))
	if report := panicReporter.Load(); report != nil {
		(*report)(ctx, name)
	}
}
