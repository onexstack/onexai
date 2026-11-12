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
	"time"

	"github.com/google/uuid"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/media"
	"github.com/onexstack/onexai/pkg/wshub"
)

// Stream lanes carried in a command_output frame's Kind. The answer lane uses
// the empty string so answer frames stay wire-compatible with older TUI clients.
const (
	streamKindReasoning = "reasoning"
	streamKindProgress  = "progress"
)

// mirrorToTUI pushes an outbound bot reply into the WebSocket hub so the TUI
// shows it as a bot message. Lark does not deliver a bot's own messages back as
// receive_v1 events, so this is the only path for a bot reply to reach the TUI.
//
// It is scoped to group chats: in P2P the botserver keys receivers by open_id
// while the TUI relay keys conversations by chat_id, so the ID spaces do not
// line up. The call is fire-and-forget and must never fail the send/AI path.
func (b *botBiz) mirrorToTUI(recv *v1.Receiver, text string) {
	if b.sr.Hub == nil || recv == nil || !recv.IsGroup() || text == "" {
		return
	}
	b.sr.Hub.SendToConv(recv.Id, wshub.ServerFrame{
		Type: wshub.FrameTypeMessage,
		Payload: wshub.MessagePayload{
			ID:          uuid.NewString(),
			ConvID:      recv.Id,
			SenderName:  b.sr.BotName,
			Content:     text,
			ContentType: "text",
			CreateTime:  time.Now().Unix(),
			IsBot:       true,
		},
	})
}

// mirrorStreamChunkToTUI streams one chunk of an AI turn into the TUI as a
// command_output frame. kind tags the stream lane ("answer", "reasoning", or
// "progress") so the TUI can render each lane distinctly; it defaults to answer
// when empty. The TUI renders these incrementally (appendChunk) under the bot's
// name, so the reply appears live instead of only after the turn ends.
// Group-chat scoped for the same reason as mirrorToTUI.
func (b *botBiz) mirrorStreamChunkToTUI(recv *v1.Receiver, kind, chunk string) {
	if b.sr.Hub == nil || recv == nil || !recv.IsGroup() || chunk == "" {
		return
	}
	b.sr.Hub.SendToConv(recv.Id, wshub.ServerFrame{
		Type: wshub.FrameTypeCommandOutput,
		Payload: wshub.CommandOutputPayload{
			Chunk:     chunk,
			AgentName: b.sr.BotName,
			Kind:      kind,
		},
	})
}

// mirrorImagesToTUI shows a run's generated images to the TUI subscribers.
//
// The frames carry references rather than bytes for the same reason every other
// path does: a picture is resolved once by whoever renders it, not copied
// through every socket on the way.
func (b *botBiz) mirrorImagesToTUI(recv *v1.Receiver, refs []media.Ref) {
	if b.sr.Hub == nil || recv == nil || !recv.IsGroup() || len(refs) == 0 {
		return
	}
	b.sr.Hub.SendToConv(recv.Id, wshub.ServerFrame{
		Type: wshub.FrameTypeMessage,
		Payload: wshub.MessagePayload{
			ConvID:      recv.Id,
			SenderName:  b.sr.BotName,
			ContentType: "image",
			Images:      refs,
			CreateTime:  time.Now().Unix(),
			IsBot:       true,
		},
	})
}

// mirrorStreamDoneToTUI signals the end of a streamed AI answer (optionally with
// an error). The TUI uses it to clear its streaming spinner.
func (b *botBiz) mirrorStreamDoneToTUI(recv *v1.Receiver, err error) {
	if b.sr.Hub == nil || recv == nil || !recv.IsGroup() {
		return
	}
	payload := wshub.CommandDonePayload{}
	if err != nil {
		payload.Error = err.Error()
	}
	b.sr.Hub.SendToConv(recv.Id, wshub.ServerFrame{
		Type:    wshub.FrameTypeCommandDone,
		Payload: payload,
	})
}
