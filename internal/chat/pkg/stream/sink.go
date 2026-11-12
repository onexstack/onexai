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

package stream

import "context"

// Sink receives the current content of one lane. Implementations render it
// somewhere: a card element, a websocket frame, a log line.
//
// Every method takes the shared Sequence and must advance it with Next() rather
// than computing a number, because the platform rejects a sequence that does not
// exceed the previous one and rejects it silently. Preview calls a sink one
// method at a time, so an implementation needs no synchronisation of its own.
type Sink interface {
	UpdateText(ctx context.Context, seq *Sequence, text string) error
	UpdateReasoning(ctx context.Context, seq *Sequence, text string) error
	UpdateProgress(ctx context.Context, seq *Sequence, text string) error
}
