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

package duty

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	chatLong = templates.LongDesc(`
        该工具用于查询【当前所在的Lark群组（即工单群）】的上下文和状态信息。无需传递任何参数。

        【触发场景】当用户意图包含以下情况时，请主动调用此工具：
        1. 查询群信息：如“当前群的 Chat ID 是多少？”、“查一下这个工单的记录 ID”。
        2. 查询值班情况：如“当前群是谁在值班？”、“值班组/业务线是什么？”。
        3. 查询工单状态：如“这个工单关闭了吗？”、“工单保存成功了吗？”。

        【工具返回的数据包含】：
        - ChatID：当前 Lark 群的唯一标识；
        - OpenID：仅【私聊 (P2P)】会话返回，是当前用户的 Lark open_id（ou_ 开头），可用于按人寻址；群聊中不返回；
        - Agents：为【提问者本人】此刻解析出的 agent 名称（注意是提问者的，不是群里上一位说话人的）；
        - 值班组：关联的值班团队或模块名称；
        - 值班人：当前的主值班人 (On-call)；
        - 报账单 ID：底层绑定的多维表格 (Base) 记录 ID；
        - 状态标识：是否已保存 (Saved)、是否已关闭/完结 (Closed)。

        【重要认知】：
        - 语境等价映射：“Lark群组” 等同于 “工单群” 或 “当前工单”。
        - 如果本工具返回“无法获取会话上下文”或“当前无工单上下文”，说明当前调用不在任何 Lark 工单群内。
        - 此时若用户意图是【新建工单】，请直接调用 /oncall 建单，无需当前工单上下文，不要因缺少当前群而拒绝建单。`)

	chatExample = templates.Examples(`
        # 用户输入：“查一下当前群的值班人和工单状态”
        # 用户输入：“这个工单的记录ID是多少”
        /chat`)
)

// ChatInfoOptions defines configuration for the "chat" command.
type ChatInfoOptions struct {
	out runtime.Responder
}

// NewChatCmd creates the "chat" command.
func NewChatCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &ChatInfoOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "chat",
		Short:                 "查询当前Lark群组(工单)的值班人、群ID及流转状态",
		Long:                  chatLong,
		Example:               chatExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.MCPTool:   chat.True,
			chat.ChatGroup: chat.ChatGroupDuty,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()))
		},
	}

	return cmd
}

// Complete is a no-op for the chat command (no positional arguments).
func (o *ChatInfoOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate is a no-op for the chat command (no positional arguments).
func (o *ChatInfoOptions) Validate(ctx context.Context, args []string) error {
	return nil
}

// Run performs the chat info operation.
func (o *ChatInfoOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) error {
	sessionM := dr.SessionM
	if sessionM == nil {
		return o.out.SendText(ctx, "当前不在任何工单群会话中，无法获取工单上下文。如需新建工单，请使用 /oncall 命令。")
	}

	var oncallGroup, primaryOncall, ticketID string
	if dr.TicketM != nil {
		oncallGroup = dr.TicketM.GroupID
		primaryOncall = dr.TicketM.AssigneeName
		ticketID = dr.TicketM.TicketID
	}

	// The caller's open_id is reported for a private chat only. A P2P session is
	// keyed by the person's bare user_id (session_id/chat_id are both that
	// user_id), so the app-scoped handle for the same person is the one identity a
	// caller cannot derive from anything else in this payload. A group carries a
	// ticket context that already names everyone involved, where the open_id would
	// be noise on every card.
	senderOpenID := ""
	if sessionM.SessionType == model.SessionTypePrivateChat {
		senderOpenID = dr.Handle()
	}

	// The agents are resolved **now, for this caller**, not read off the session
	// row.
	//
	// # Why it is asked rather than looked up
	//
	// A conversation declares an intent (session.refs: `@peer`, `@default`, or a
	// name); what that intent picks is the speaker's, not the room's. `@peer` is
	// one agent for whoever bound an account and another for the next person to
	// talk, so a conversation-wide record of the answer cannot describe "what am
	// I running as" — in a group it describes whoever spoke last, and this
	// command used to report that as if it were the reader's. The resolution is
	// delegated to the scheduler through the runtime callback for the reason
	// /agent delegates it: the scheduler lives in the bot, which imports this
	// package.
	//
	// The cost is one peer-binding read, paid once per invocation by a command a
	// person typed. A surface that cannot name an agent (the REST face) keeps a
	// nil callback and is answered from the declaration alone.
	res, resolveErr := resolveForCaller(ctx, dr)
	if resolveErr != nil {
		// Not fatal: the rest of the card is still true, and a card missing one
		// line beats a card that is not sent. Saying so beats a card that silently
		// omits the one field the reader came for.
		slog.WarnContext(ctx, "could not resolve the agents for /chat", "error", resolveErr)
	}

	rawResp := chatInfoResponse{
		ChatID:        sessionM.ChatID,
		SessionID:     sessionM.SessionID,
		SessionType:   sessionM.SessionType,
		OpenID:        senderOpenID,
		OncallGroup:   oncallGroup,
		PrimaryOncall: primaryOncall,
		TicketID:      ticketID,
		Refs:          res.Refs,
		Agents:        model.EncodeNameList(res.Agents),
		IsSaved:       sessionM.IsSaved(),
		IsClosed:      sessionM.IsClosed(),
	}

	tableData := []kvRow{
		{"会话 ID", sessionM.SessionID},
	}
	if senderOpenID != "" {
		// Next to the session ID (会话 ID), whose app-scoped twin it is.
		tableData = append(tableData, kvRow{"OpenID", senderOpenID})
	}
	tableData = append(tableData,
		kvRow{"会话类型", sessionM.SessionType},
		// The agents, and nothing about where they came from: the declaration
		// (`session.refs`) that produced them is a scheduling input, and the card
		// answers what the caller is running as. It stays readable in the payload's
		// refs field for anyone who needs to tell a declared agent from a defaulted
		// one.
		kvRow{"Agents", agentsText(res, resolveErr)},
	)

	if sessionM.SessionType == model.SessionTypeTicketGroup {
		tableData = append(tableData, []kvRow{
			{"值班组", oncallGroup},
			{"值班人", primaryOncall},
			{"工单ID", ticketID},
			{"是否已保存", sessionM.IsSaved()},
			{"是否已关闭", sessionM.IsClosed()},
		}...)
	}

	jsonBytes, _ := json.MarshalIndent(rawResp, "", "  ")
	responseText := fmt.Sprintf("当前会话(Chat)的状态信息如下：\n```json\n%s\n```", string(jsonBytes))

	return o.out.SendResult(ctx, &runtime.CommandResult{
		Raw:            rawResp,
		LarkTemplateID: chat.ChatInfoTemplateID,
		LarkVariables:  map[string]any{"raw_data": tableData},
		Markdown:       &runtime.MarkdownContent{Content: responseText},
		PlainText:      fmt.Sprintf("当前会话 ChatID: %s, 关联工单: %s", sessionM.ChatID, ticketID),
	})
}

// resolveForCaller asks the scheduler what this caller is running as right now,
// falling back to the conversation's raw declaration on a surface that has no
// scheduler to ask.
//
// The fallback is the declaration, not a recorded answer: there is no recorded
// answer any more (session.agents was deleted precisely because it described the
// last speaker), and an empty declaration is honest — it says "this conversation
// states nothing", which is also why the platform template applies.
func resolveForCaller(ctx context.Context, dr *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
	if dr == nil || dr.ResolvedAgents == nil {
		if dr != nil && dr.SessionM != nil {
			return runtime.AgentResolution{Refs: dr.SessionM.RefList()}, nil
		}
		return runtime.AgentResolution{}, nil
	}
	return dr.ResolvedAgents(ctx, dr)
}

// agentsText renders the resolved set for a card row.
//
// An empty set is rendered as the reason rather than as `[]`: the three reasons
// (not bound / no membership / nothing configured) each have a different next
// step for the reader, and a command that reports only "nothing" sends them to
// ask an administrator about a binding they can complete themselves.
//
// A resolution that failed is worded apart from all three. "Nothing is
// configured" is a claim about the conversation, and a failed read is a claim
// about nothing at all — reporting it as the first would send the reader looking
// for a configuration problem that may not exist.
func agentsText(res runtime.AgentResolution, err error) string {
	if err != nil {
		return "— 暂时无法解析（请稍后重试，或联系管理员）"
	}
	if len(res.Agents) == 0 {
		return "— " + res.Hint.Notice()
	}
	return strings.Join(res.Agents, "、")
}

// chatInfoResponse is the JSON payload returned by the chat command.
type chatInfoResponse struct {
	ChatID      string `json:"chat_id"`
	SessionID   string `json:"session_id"`
	SessionType string `json:"session_type"`
	// OpenID is the caller's Lark open_id. It is set for a private-chat session
	// and omitted everywhere else, so a group caller sees no such field at all.
	OpenID        string `json:"open_id,omitempty"`
	OncallGroup   string `json:"oncall_group"`
	PrimaryOncall string `json:"primary_oncall"`
	TicketID      string `json:"ticket_id"`
	// Refs is the effective declaration this caller's agents were resolved from:
	// the conversation's declaration, a private chat's usable binding, or the
	// conversation type's template — whichever the scheduler applied.
	Refs []string `json:"refs,omitempty"`
	// Agents is the JSON array of agent names this caller runs as, empty when
	// there are none. It stays a string rather than becoming a []string so the
	// field keeps the shape every existing reader of this payload expects.
	Agents   string `json:"agents"`
	IsSaved  bool   `json:"is_saved"`
	IsClosed bool   `json:"is_closed"`
}

// kvRow is a key/value row rendered by the Lark chat-info card template.
type kvRow struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}
