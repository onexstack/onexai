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

package chat

// Chat type constants identify the kind of conversation a message belongs to.
// They are used as the chat_type label in metrics and to resolve where to reply
// (a direct user versus a group chat).
const (
	// ChatTypeP2P identifies a direct (peer-to-peer) chat with a single user.
	ChatTypeP2P = "p2p"

	// ChatTypeGroup identifies a group chat.
	ChatTypeGroup = "group"
)
