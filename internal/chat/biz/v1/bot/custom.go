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
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/onexstack/onexstack/pkg/ptr"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	chatv1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// RunCustom implements runtime.CustomRunner.
//
// The command line was parsed by cobra in the command package; what crosses the
// wire is the structured parameter map, so the agent service and the chat
// service cannot disagree about what a flag meant. A prompt command becomes an
// ordinary turn; an http or tool command's result is rendered as a card.
func (b *botBiz) RunCustom(
	ctx context.Context,
	dr *runtime.DynamicRuntime,
	out runtime.Responder,
	command runtime.CustomCommand,
	params map[string]any,
) error {
	if b.sr == nil || b.sr.Clientset == nil || b.sr.Clientset.Agent() == nil {
		return runtime.NewUserError("自定义命令未启用。")
	}

	// On the Lark surface the command carries the agent that exposed it. On MCP
	// the tool list is built once from every command definition, so the agent is
	// resolved from the caller's own selection here.
	agentName := command.Agent
	if agentName == "" {
		agentName = b.boundAgentFor(ctx, dr, command)
	}
	if agentName == "" {
		return runtime.NewUserError("无法确定命令 %q 绑定的 Agent。", command.Name)
	}

	req := &agentv1.InvokeCustomCommandRequest{
		Agent: agentName,
		User:  userInfoOf(dr),
		Context: &agentv1.InvocationContext{
			ConversationId: convIDOf(dr),
			ChatType:       dr.ChatType(),
			Surface:        string(dr.Source),
		},
		Params: structOf(params),
	}
	resp, err := b.sr.Clientset.Agent().InvokeCustomCommand(ctx, command.Name, req)
	if err != nil {
		return runtime.NewUserError("执行命令失败：%v", err)
	}

	status := resp.GetStatus()
	if resp.GetKind() == "prompt" {
		if status.GetPhase() != "Succeeded" {
			return runtime.NewUserError("%s", statusMessage(status))
		}
		text := status.GetPrompt().GetPrompt()
		if dr.Source == runtime.RequestSourceMCP {
			// An MCP caller has no card to draw a turn on. The rendered prompt
			// is the useful answer: the model receives the instruction itself.
			_, err := out.Write([]byte(text))
			return err
		}
		if !dr.HasLarkContext() {
			return runtime.NewUserError("prompt 类型命令仅在飞书会话中可用。")
		}
		turn := ptr.Clone(dr)
		return b.runAI(ctx, turn, text, nil)
	}

	if status.GetPhase() == "PendingApproval" {
		return b.sendCustomApprovalCard(ctx, dr, out, command, agentName, status.GetApproval())
	}
	if status.GetPhase() != "Succeeded" {
		return runtime.NewUserError("%s", statusMessage(status))
	}

	content := renderCustomResult(status)
	if content == "" {
		content = "命令已执行。"
	}
	if warnings := status.GetWarnings(); len(warnings) > 0 {
		content += "\n\n" + strings.Join(warnings, "\n")
	}
	return out.SendMarkdownCard(ctx, "/"+command.Name, content, "blue")
}

// boundAgentFor finds the agent in the caller's selection that binds command.
func (b *botBiz) boundAgentFor(
	ctx context.Context,
	dr *runtime.DynamicRuntime,
	command runtime.CustomCommand,
) string {
	if b.sr == nil || b.sr.AgentCommands == nil || dr == nil || dr.ResolvedAgents == nil {
		return ""
	}
	res, err := dr.ResolvedAgents(ctx, dr)
	if err != nil {
		return ""
	}
	selection := b.sr.AgentCommands.Resolve(ctx, res.Agents)
	for _, c := range selection.Customs {
		if c.Name == command.Name {
			return c.Agent
		}
	}
	return ""
}

// customApprovePrefix marks the card action that answers a parked tool call.
const customApprovePrefix = "capprove:"

// sendCustomApprovalCard asks a human to allow or deny the parked call.
func (b *botBiz) sendCustomApprovalCard(
	ctx context.Context,
	dr *runtime.DynamicRuntime,
	out runtime.Responder,
	command runtime.CustomCommand,
	agentName string,
	approval *agentv1.CustomCommandApproval,
) error {
	if approval == nil || approval.GetApprovalId() == "" {
		return runtime.NewUserError("审批信息缺失。")
	}
	extra := map[string]string{
		"approval_id": approval.GetApprovalId(),
		"command":     command.Name,
		"agent":       agentName,
	}
	allow := larkcard.Btn("允许", "primary", customApprovePrefix+"allow")
	allow.Extra = extra
	deny := larkcard.Btn("拒绝", "danger", customApprovePrefix+"deny")
	deny.Extra = extra

	body := fmt.Sprintf("命令 `/%s` 需要人工审批。\n\n- 工具：`%s`\n- MCP：`%s`\n- 参数：`%s`",
		command.Name, approval.GetTool(), approval.GetMcpServer(), approval.GetArgumentsJson())
	card := larkcard.NewCard().
		Title("需要审批", "orange").
		Markdown(body).
		ButtonsEqual(allow, deny).
		Note("15 分钟内有效，仅发起人可操作").
		Build()
	if dr != nil && dr.Receiver != nil {
		return b.SendCardModel(ctx, dr.Receiver, card)
	}
	return out.SendInteractiveCard(ctx, card)
}

// handleCustomApproveAction answers a capprove: click by calling the agent
// service with the stored approval id and rendering the tool result.
func (b *botBiz) handleCustomApproveAction(
	ctx context.Context,
	event *callback.CardActionTriggerEvent,
	action string,
	recv *chatv1.Receiver,
) (*callback.CardActionTriggerResponse, error) {
	decision := strings.TrimPrefix(action, customApprovePrefix)
	if decision != "allow" && decision != "deny" {
		return nil, nil
	}
	if event.Event.Action == nil {
		return stopToast("error", "审批信息缺失"), nil
	}
	value := event.Event.Action.Value
	approvalID, _ := value["approval_id"].(string)
	commandName, _ := value["command"].(string)
	agentName, _ := value["agent"].(string)
	if approvalID == "" || commandName == "" {
		return stopToast("error", "审批信息缺失"), nil
	}

	dr := b.cardNavRuntime(ctx, senderOfCardOperator(event), recv)
	if dr.Identity() == "" {
		return stopToast("warning", "无法确认操作人"), nil
	}
	resp, err := b.sr.Clientset.Agent().ApproveCustomCommand(ctx, commandName, &agentv1.ApproveCustomCommandRequest{
		ApprovalId: approvalID,
		Decision:   decision,
		Agent:      agentName,
		User:       userInfoOf(dr),
	})
	if err != nil {
		slog.WarnContext(ctx, "custom command approval failed", "command", commandName, "error", err)
		return stopToast("error", "审批失败："+err.Error()), nil
	}
	status := resp.GetStatus()
	if status.GetPhase() != "Succeeded" {
		return stopToast("warning", statusMessage(status)), nil
	}
	content := renderCustomResult(status)
	if content == "" {
		content = "命令已执行。"
	}
	card := larkcard.NewCard().Title("/"+commandName, "blue").Markdown(content).Build()
	if err := b.SendCardModel(ctx, recv, card); err != nil {
		slog.WarnContext(ctx, "send custom command result failed", "command", commandName, "error", err)
	}
	return stopToast("success", "已执行"), nil
}

// statusMessage turns a denial or failure into one line for the user.
func statusMessage(status *agentv1.InvocationStatus) string {
	message := status.GetMessage()
	if message == "" {
		message = status.GetEvaluationError()
	}
	if message == "" {
		message = status.GetReason()
	}
	switch status.GetPhase() {
	case "Denied":
		return "命令被拒绝：" + message
	default:
		if status.GetEvaluationError() != "" && status.GetMessage() == "" {
			return "命令无法执行：" + message
		}
		return "命令执行失败：" + message
	}
}

func renderCustomResult(status *agentv1.InvocationStatus) string {
	if rendered := status.GetHttp().GetRendered(); rendered != "" {
		return rendered
	}
	if result := status.GetTool().GetResult(); result != nil {
		return valueText(result)
	}
	return ""
}

func valueText(v *structpb.Value) string {
	if v == nil {
		return ""
	}
	if s, ok := v.GetKind().(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	return v.String()
}

func structOf(params map[string]any) *structpb.Struct {
	if len(params) == 0 {
		return nil
	}
	out, err := structpb.NewStruct(params)
	if err != nil {
		return nil
	}
	return out
}

func userInfoOf(dr *runtime.DynamicRuntime) *agentv1.UserInfo {
	identity := dr.Identity()
	if identity == "" {
		return nil
	}
	return &agentv1.UserInfo{Username: identity}
}

func convIDOf(dr *runtime.DynamicRuntime) string {
	if dr == nil {
		return ""
	}
	return dr.ConvID
}
