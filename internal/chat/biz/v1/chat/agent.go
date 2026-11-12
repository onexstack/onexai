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

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/media"
	"github.com/onexstack/onexai/pkg/wshub"
)

// Virtual conversation ID prefixes. Robot/platform conversations do not map to a
// Lark chat_id; they are addressed directly to an agent via oai-agent-apiserver.
const (
	convRobotPrefix    = "robot:"
	convPlatformPrefix = "platform:"
)

// aiTurnTimeout bounds a single agent stream so a stuck agent can't leave
// the goroutine (and the WS client) hanging forever.
const aiTurnTimeout = 5 * time.Minute

// isVirtualConv reports whether convID is a virtual agent conversation
// (robot:<agent> or platform:<agent>).
func isVirtualConv(convID string) bool {
	return strings.HasPrefix(convID, convRobotPrefix) || strings.HasPrefix(convID, convPlatformPrefix)
}

// agentFromConvID extracts the agent name from a virtual convID
// ("robot:foo" → "foo"). Returns ok=false for non-virtual convIDs.
func agentFromConvID(convID string) (agentName string, ok bool) {
	for _, p := range []string{convRobotPrefix, convPlatformPrefix} {
		if strings.HasPrefix(convID, p) {
			return strings.TrimPrefix(convID, p), true
		}
	}
	return "", false
}

// ListAgent returns all agents from oai-agent-apiserver, with the built-in OneX
// agent(s) ordered first.
func (b *chatBiz) ListAgent(ctx context.Context) (*api.ListAgentResponse, error) {
	if b.llm == nil {
		return &api.ListAgentResponse{Agents: []api.AgentItem{}}, nil
	}
	agents, err := b.llm.ListAgent(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list agents from agent", "error", err)
		return nil, errno.ErrInternal.WithMessage("failed to list agents from the agent service")
	}
	sortAgents(agents)
	items := make([]api.AgentItem, 0, len(agents))
	for _, a := range agents {
		items = append(items, api.AgentItem{
			Name:        a.GetName(),
			DisplayName: a.GetDisplayName(),
			Description: a.GetDescription(),
			Status:      a.GetStatus(),
			Labels:      a.GetLabels(),
		})
	}
	return &api.ListAgentResponse{Agents: items}, nil
}

// SendToAgent streams an agent answer for the given conversation back to the
// client. It is used for virtual robot/platform conversations and for bound
// group/p2p conversations (via /askai). The stream is pushed as command_output
// chunks, terminated by a command_done frame.
func (b *chatBiz) SendToAgent(ctx context.Context, clientID, convID, agentName, content string, images []media.Ref, clientMsgID string) {
	// Confirm the user's optimistic message up front so it no longer shows as
	// "pending" (agent answers stream separately, so there is no server echo to
	// confirm it later).
	if clientMsgID != "" {
		b.hub.SendTo(clientID, wshub.ServerFrame{
			Type:    wshub.FrameTypeAck,
			Payload: wshub.AckPayload{ClientMsgID: clientMsgID},
		})
	}

	if b.llm == nil {
		b.hub.SendTo(clientID, wshub.ServerFrame{
			Type:    wshub.FrameTypeCommandDone,
			Payload: wshub.CommandDonePayload{Error: "AI engine is not configured"},
		})
		return
	}
	if agentName == "" {
		// A name is not cosmetic here: the LLM server scopes its MCP tools by the
		// requested agent names, so an empty one means no tools at all rather than
		// "the default". See the constant for why it is spelled this way.
		agentName = chat.DefaultAgentName
	}

	userID, _ := b.hub.ClientIdentity(clientID)

	ctx, cancel := context.WithTimeout(ctx, aiTurnTimeout)
	defer cancel()

	req := &v1.ChatCompletionRequest{
		SessionID: convID,
		Agent:     agentName,
		SenderID:  userID,
		Messages: []*v1.ChatCompletionMessage{{
			Role:    "user",
			Content: content,
			// The compact string form, which the backend parses back into the
			// reference it names. This service never holds the bytes.
			Images: media.RefStrings(images),
		}},
	}
	err := b.llm.ChatCompletionsStream(ctx, req, func(chunk *v1.ChatCompletionStreamResponse) error {
		for _, choice := range chunk.GetChoices() {
			d := choice.GetDelta()
			if d == nil {
				continue
			}
			if d.GetContent() != "" {
				b.hub.SendTo(clientID, wshub.ServerFrame{
					Type: wshub.FrameTypeCommandOutput,
					Payload: wshub.CommandOutputPayload{
						Chunk:     d.GetContent(),
						AgentName: agentName,
					},
				})
			}
			// A picture the run drew arrives on its own chunk. It is relayed as
			// a message rather than a command_output chunk because that is what
			// it is: something the conversation now contains, rendered by its
			// own reference, not more text of the answer.
			if refs := agentapi.RefsFromProto(d.GetImageRefs()); len(refs) > 0 {
				b.hub.SendTo(clientID, wshub.ServerFrame{
					Type: wshub.FrameTypeMessage,
					Payload: wshub.MessagePayload{
						ConvID:      convID,
						SenderName:  agentName,
						ContentType: "image",
						Images:      refs,
						CreateTime:  time.Now().Unix(),
						IsBot:       true,
					},
				})
			}
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "agent stream failed", "conv_id", convID, "agent", agentName, "error", err)
		b.hub.SendTo(clientID, wshub.ServerFrame{
			Type:    wshub.FrameTypeCommandDone,
			Payload: wshub.CommandDonePayload{Error: err.Error()},
		})
		return
	}
	b.hub.SendTo(clientID, wshub.ServerFrame{
		Type:    wshub.FrameTypeCommandDone,
		Payload: wshub.CommandDonePayload{},
	})
}

// sortAgents orders agents so the built-in OneX assistant comes first, keeping
// the remaining agents in their original order.
func sortAgents(agents []*v1.Agent) {
	sort.SliceStable(agents, func(i, j int) bool {
		return isBuiltinAgent(agents[i].GetName()) && !isBuiltinAgent(agents[j].GetName())
	})
}

func isBuiltinAgent(name string) bool {
	// `__builtin__` is the sys-prompt sentinel, not an agent name; it is matched
	// here because the two were once spelled the same way. Kept rather than
	// dropped so a stored value from before the split still sorts first.
	return name == "" || name == chat.DefaultAgentName || name == chat.BuiltinAgentName
}
