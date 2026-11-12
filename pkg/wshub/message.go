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

package wshub

import (
	"encoding/json"

	"github.com/onexstack/onexai/pkg/media"
)

// FrameType is the type of a WebSocket frame.
type FrameType string

const (
	// Client → Server
	FrameTypePing      FrameType = "ping"
	FrameTypeSubscribe FrameType = "subscribe"
	FrameTypeSend      FrameType = "send"
	FrameTypeAck       FrameType = "ack"

	// Server → Client
	FrameTypePong          FrameType = "pong"
	FrameTypeMessage       FrameType = "message"
	FrameTypeEvent         FrameType = "event"
	FrameTypeError         FrameType = "error"
	FrameTypeCommandOutput FrameType = "command_output"
	FrameTypeCommandDone   FrameType = "command_done"
	// FrameTypeConversationChanged tells the client to re-fetch its
	// conversation list (e.g. after the bot is added to a new group chat).
	FrameTypeConversationChanged FrameType = "conversation_changed"
)

// ClientFrame is sent from the onexai TUI client to oai-chat-apiserver.
type ClientFrame struct {
	Type      FrameType       `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	ConvID    string          `json:"conv_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// ServerFrame is sent from oai-chat-apiserver to the onexai TUI client.
type ServerFrame struct {
	Type      FrameType `json:"type"`
	RequestID string    `json:"request_id,omitempty"`
	Payload   any       `json:"payload,omitempty"`
}

// SendPayload is the payload for a "send" frame.
type SendPayload struct {
	Content string `json:"content"`
	// Images are references to images the client has already uploaded, attached
	// to this message.
	//
	// # Why references and not bytes
	//
	// A client uploads through POST /v1/assets and sends what it was given. The
	// alternative — base64 in this frame — would put megabytes through the
	// socket that carries every keystroke of every conversation, and would make
	// the same picture cost its size again on every retry of a dropped
	// connection.
	//
	// A message may carry images and no text: a screenshot with nothing typed
	// beside it is a question in itself.
	Images []media.Ref `json:"images,omitempty"`
	// ClientMsgID is a client-generated id used to correlate the optimistic
	// local message with the server ack / broadcast echo (for de-duplication).
	ClientMsgID string `json:"client_msg_id,omitempty"`
}

// AckPayload is the payload for an "ack" frame (server → client), confirming a
// sent message and mapping the client id to the server-assigned id.
type AckPayload struct {
	ClientMsgID string `json:"client_msg_id"`
	ServerID    string `json:"server_id"`
	Error       string `json:"error,omitempty"`
}

// CommandOutputPayload is the payload for a "command_output" frame.
type CommandOutputPayload struct {
	Chunk string `json:"chunk"`
	// AgentName is the sender label for the streamed chunk (e.g. the answering
	// agent's name). Empty means the default "onexai" label.
	AgentName string `json:"agent_name,omitempty"`
	// Kind tags the chunk's stream lane: "answer" (default), "reasoning", or
	// "progress". Clients may render each lane differently; omitempty keeps the
	// answer path wire-compatible with older clients.
	Kind string `json:"kind,omitempty"`
}

// CommandDonePayload is the payload for a "command_done" frame.
type CommandDonePayload struct {
	Error string `json:"error,omitempty"`
}

// MessagePayload is the payload for a "message" frame (IM message push).
type MessagePayload struct {
	ID          string `json:"id"`
	ConvID      string `json:"conv_id"`
	SenderID    string `json:"sender_id"`
	SenderName  string `json:"sender_name"`
	Content     string `json:"content"`
	ContentType string `json:"content_type,omitempty"` // "text"|"image"|"file"|"audio"|"post"|"card"
	// Images are the message's images, as references the client resolves
	// against the media route. ContentType stays "image" for an image-only
	// message; a message with both words and pictures carries its text in
	// Content and its pictures here.
	Images     []media.Ref `json:"images,omitempty"`
	CreateTime int64       `json:"create_time"`
	IsBot      bool        `json:"is_bot,omitempty"`
	// ClientMsgID echoes the sender's client id so the originating client can
	// replace its optimistic local message instead of appending a duplicate.
	ClientMsgID string `json:"client_msg_id,omitempty"`
}

// SubscribePayload is the payload for a "subscribe" frame.
type SubscribePayload struct {
	ConvID string `json:"conv_id"`
}

// ErrorPayload is the payload for an "error" frame.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
