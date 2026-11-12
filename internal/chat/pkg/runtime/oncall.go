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

	"github.com/onexstack/onexai/internal/chat/biz/v1/oncall"
)

// OncallReader is how a command reads the conversation a ticket was raised in.
//
// # Why it is here rather than reached for directly
//
// The command layer holds a StaticRuntime and nothing else; the conversation
// lives behind the chat biz, which the bot owns. Handing commands one method of
// that surface is what lets `/close` summarize a ticket without the command
// package learning how a conversation is fetched, cached or archived — the
// three-level fallback and its provenance markers stay in the package that
// already has them.
//
// # Why the return type is named rather than restated
//
// A local copy of the shape would be a second description of a fallback's result,
// and the one thing a caller of this method must not lose is *which level
// answered*: 「没有会话」 (no session) and 「读不到会话」 (session unreadable) both arrive as an empty list, and a
// summary written without knowing which it got cannot say so.
//
// The interface is declared by its consumer, as Assets and AgentCommandResolver
// above are, so a test can drive a command with a transcript without a database.
type OncallReader interface {
	// Transcript returns the ticket group's conversation, oldest first.
	Transcript(ctx context.Context, ticketID string) (*oncall.Transcript, error)
}
