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

// Package larktool exposes outbound messaging to the agent as a tool.
//
// The semantics are "copy in", not "reply here": the run's own answer is
// delivered by the surface that started it, and this tool sends an additional
// copy somewhere else. A model that understood it as redirection would stop
// answering, which is why the tool description says so in as many words.
//
// It is gated on the caller being an SRE. A tool that can post into any chat
// the bot is a member of is a tool that can be used to make the bot say things;
// restricting it to a group whose membership is already a privileged fact keeps
// the blast radius to people who could have posted there themselves.
package larktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	larkv3 "github.com/larksuite/oapi-sdk-go/v3"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// ToolName is the name the model sees.
const ToolName = "send_lark_message"

// Receiver types as the Lark message API names them.
const (
	receiveIDTypeChatID = "chat_id"
	receiveIDTypeOpenID = "open_id"
)

// sendMessageTool implements the copy-in tool.
type sendMessageTool struct {
	client *larkv3.Client
}

// input is the tool's argument shape.
type input struct {
	// Target identifies the recipient: a chat id ("oc_…"), an open id ("ou_…"),
	// or an email address, which is resolved to an open id first.
	Target string `json:"target"`
	// Content is the message body, sent verbatim.
	Content string `json:"content"`
	// Format is "text" (default) or "markdown".
	Format string `json:"format"`
	// Title is the card title, used only for markdown.
	Title string `json:"title"`
}

// NewSendMessage builds the tool, or nil when no Lark client is configured.
func NewSendMessage(client *larkv3.Client) tool.BaseTool {
	if client == nil {
		return nil
	}
	return &sendMessageTool{client: client}
}

// Info advertises the tool.
func (t *sendMessageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ToolName,
		Desc: "把一段内容【额外抄送】一份到指定的人或群。适用于用户要求「把这个结果也发给某人 / 发到某个群」的场景。" +
			"注意：这是抄送，当前会话的回复照常发送，不要因为调用了本工具就省略回复内容。" +
			"本工具只负责投递，正文按 content 原样传入。" +
			"仅 SRE 团队成员可用：非成员调用会被拒绝，此时直接告知用户无权限即可，不要重试。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"target": {
				Type: schema.String,
				Desc: "接收方标识：群聊填 chat_id（以 oc_ 开头）；已知对方 open_id 时填 open_id（以 ou_ 开头）；" +
					"否则填对方的邮箱（如 user@example.com），工具会自行解析。" +
					"不要填显示名、部门或职位；不知道邮箱时先向用户确认，切勿凭空构造。",
				Required: true,
			},
			"content": {
				Type:     schema.String,
				Desc:     "要抄送的消息正文。需要保留原始内容时请原样传入，不要压缩或改写。",
				Required: true,
			},
			"format": {
				Type: schema.String,
				Desc: `正文格式："text"（默认，纯文本）或 "markdown"（发送交互卡片）。`,
			},
			"title": {
				Type: schema.String,
				Desc: "卡片标题，仅 format 为 markdown 时生效。",
			},
		}),
	}, nil
}

// InvokableRun resolves the recipient, checks the caller, and sends.
func (t *sendMessageTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in input
	if err := json.Unmarshal([]byte(argumentsInJSON), &in); err != nil {
		return "", fmt.Errorf("expected an object with target and content: %w", err)
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", fmt.Errorf("content cannot be empty")
	}
	if err := requireTrustedCaller(ctx); err != nil {
		return "", err
	}

	receiverID, receiverType, err := t.resolveTarget(ctx, in.Target)
	if err != nil {
		return "", err
	}

	if strings.EqualFold(strings.TrimSpace(in.Format), "markdown") {
		return t.sendCard(ctx, receiverID, receiverType, in.Title, in.Content)
	}
	return t.sendText(ctx, receiverID, receiverType, in.Content)
}

// requireTrustedCaller refuses a caller that is not an internal service
// account.
//
// The tool used to be gated on the SRE role. Run requests no longer carry a
// role assertion (the field was removed), so the gate is now the one identity
// property the service can verify itself: the call executes as a trusted
// service, not as a browser user. A caller with no subject is refused too —
// the absence of an identity is not a grant, and this is the one tool whose
// whole effect is outside the service.
func requireTrustedCaller(ctx context.Context) error {
	subject, ok := authz.SubjectFromContext(ctx)
	if !ok {
		return fmt.Errorf("this action needs a caller identity, and none was provided")
	}
	if subject.Kind != authz.SubjectKindServiceAccount {
		return fmt.Errorf("only an internal service account may send messages through this tool")
	}
	return nil
}

// resolveTarget turns the argument into the id and id-type the message API
// wants, resolving an email address through the directory.
func (t *sendMessageTool) resolveTarget(ctx context.Context, target string) (id, idType string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", fmt.Errorf("target cannot be empty")
	}
	switch {
	case strings.HasPrefix(target, "oc_"):
		return target, receiveIDTypeChatID, nil
	case strings.HasPrefix(target, "ou_"):
		return target, receiveIDTypeOpenID, nil
	case strings.Contains(target, "@"):
		openID, err := t.resolveEmail(ctx, target)
		if err != nil {
			return "", "", err
		}
		return openID, receiveIDTypeOpenID, nil
	default:
		return "", "", fmt.Errorf(
			"target %q is not usable: send a chat id (oc_…), an open id (ou_…), or an email address", target)
	}
}

// resolveEmail looks an address up in the directory.
//
// A display name is deliberately not accepted: two people can share one, and a
// message delivered to the wrong person cannot be recalled.
func (t *sendMessageTool) resolveEmail(ctx context.Context, email string) (string, error) {
	resp, err := t.client.Contact.V3.User.BatchGetId(ctx, larkcontact.NewBatchGetIdUserReqBuilder().
		UserIdType("open_id").
		Body(larkcontact.NewBatchGetIdUserReqBodyBuilder().Emails([]string{email}).Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("could not look up %s: %w", email, err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("could not look up %s: code=%d msg=%s", email, resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return "", fmt.Errorf("no user is registered for %s", email)
	}
	for _, user := range resp.Data.UserList {
		if user != nil && ptr.From(user.UserId) != "" {
			return *user.UserId, nil
		}
	}
	return "", fmt.Errorf("no user is registered for %s", email)
}

// sendText sends a plain-text message.
func (t *sendMessageTool) sendText(ctx context.Context, receiverID, receiverType, content string) (string, error) {
	body, err := json.Marshal(map[string]string{"text": content})
	if err != nil {
		return "", fmt.Errorf("encode message: %w", err)
	}
	return t.send(ctx, receiverID, receiverType, "text", string(body))
}

// sendCard sends an interactive card.
//
// The card is the plain markdown element rather than one of the application's
// rich templates: this tool's content comes from a model, and a template's
// fixed copy would be sent alongside text that has nothing to do with it.
func (t *sendMessageTool) sendCard(ctx context.Context, receiverID, receiverType, title, markdown string) (string, error) {
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": markdown}},
		},
	}
	if title != "" {
		card["header"] = map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": title},
			"template": "blue",
		}
	}
	body, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("encode card: %w", err)
	}
	return t.send(ctx, receiverID, receiverType, "interactive", string(body))
}

// send performs one message-create call.
func (t *sendMessageTool) send(ctx context.Context, receiverID, receiverType, msgType, body string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiverType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiverID).
			MsgType(msgType).
			Content(body).
			Build()).
		Build()

	resp, err := t.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("send message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "message sent", nil
	}
	return fmt.Sprintf("message sent (id=%s)", *resp.Data.MessageId), nil
}
