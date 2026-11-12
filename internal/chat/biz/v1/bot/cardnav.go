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
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/command/resource"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// commandInfo is a single chat command surfaced in the navigation menu.
type commandInfo struct {
	name  string
	short string
}

// commandGroup is a display group of commands, keyed by the ChatGroup annotation.
type commandGroup struct {
	name  string
	items []commandInfo
}

// chatGroupLabel is the name a group is shown under on a card. The name comes
// from the command catalog, which is where a group is declared — keeping the
// list here as well is how the card and the text help came to name the same
// command differently.
//
// The annotation key itself stays ASCII: it is what the group's buttons carry
// ("nav:group:<key>") and what the callback is matched against, so renaming the
// key would strand every card sent before the rename. An unknown key is
// returned unchanged, which is what a card built before a group was removed
// still shows.
func chatGroupLabel(name string) string {
	return command.GroupLabel(name)
}

// defaultCardNavHandler renders the next card for a "nav:"/"act:" card action,
// driving onexai's chat command tree as a navigable menu. cc-connect's engine-
// configuration menu cards (/model, /provider, /cron, ...) have no onexai
// counterpart; here the menu surfaces onexai's own ChatOps command surface.
//
// Recognized actions:
//
//	nav:/help | nav:/commands        → grouped command menu
//	nav:/status                      → bot status card
//	nav:group:<Group>                → a group's command list (buttons run them)
//	nav:/<cmd> | act:/<cmd> [args]   → run the command, render its output
func (b *botBiz) defaultCardNavHandler(ctx context.Context, event *callback.CardActionTriggerEvent, action string, recv *v1.Receiver) *larkcard.Card {
	sender := senderOfCardOperator(event)
	_, body := splitCardNavAction(action)
	body = strings.TrimPrefix(strings.TrimSpace(body), "/")

	switch {
	case body == "" || body == "help" || body == "commands":
		return b.renderHelpCard(ctx, sender, recv)
	case body == "status":
		return b.renderStatusCard(recv)
	case body == "history":
		return b.renderHistoryCard(ctx, recv)
	case body == "reasoning" || strings.HasPrefix(body, "reasoning "):
		return b.renderReasoningCard(recv, strings.TrimSpace(strings.TrimPrefix(body, "reasoning")))
	case body == "switch" || body == "agents":
		return b.renderAgentCard(ctx, event, recv)
	case strings.HasPrefix(body, "switch "):
		return b.selectAgent(ctx, event, recv, strings.TrimSpace(strings.TrimPrefix(body, "switch")))
	case strings.HasPrefix(body, "group:"):
		return b.renderGroupCard(ctx, sender, recv, strings.TrimPrefix(body, "group:"))
	case strings.HasPrefix(body, resource.PodCardActionPrefix):
		// A /pod card's own controls: selecting a pod or paging its detail.
		// Handled here rather than as a command so the card is redrawn in place
		// from the result the command already fetched.
		if card, ok := resource.HandlePodCardAction(ctx, body); ok {
			return card
		}
	}

	// Any other action is a command to run.
	cmd, args := splitCommandArgs(body)
	return b.renderCommandCard(ctx, sender, recv, cmd, args)
}

// splitCardNavAction splits "nav:/help" or "act:/model foo" into prefix and body.
func splitCardNavAction(action string) (prefix, body string) {
	if i := strings.Index(action, ":"); i >= 0 {
		return action[:i], strings.TrimSpace(action[i+1:])
	}
	return "", strings.TrimSpace(action)
}

// splitCommandArgs splits "duty oncall" into command and args.
func splitCommandArgs(body string) (cmd, args string) {
	body = strings.TrimSpace(body)
	if i := strings.IndexByte(body, ' '); i >= 0 {
		return body[:i], strings.TrimSpace(body[i+1:])
	}
	return body, ""
}

// cardNavRuntime builds the DynamicRuntime a card navigation runs commands in.
//
// The session row is attached because the menu it builds must be the menu the
// conversation's agents were given: without it a card would be granted nothing
// at all while the text help listed the granted commands, which is the same
// card/help disagreement the group names used to have — pointing the other way,
// and a worse one to be on the wrong side of, since the card is the thing a
// person clicks.
func (b *botBiz) cardNavRuntime(ctx context.Context, sender v1.Sender, recv *v1.Receiver) *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		IsSRE:        b.sr.IsSREUser(sender.Identity()),
		IsAdmin:      b.sr.IsAdminUser(sender.Identity()),
		Sender:       sender,
		Receiver:     recv,
		ConvID:       recv.Id,
		IsCardAction: true,
		SessionM:     b.sessionFor(ctx, recv.Id),
		LarkServices: b,
		// The command package cannot reach the scheduler, and which commands an
		// agent was given is stated in the same terms: see DynamicRuntime's
		// fields.
		ResolvedAgents: b.resolvedAgentsOnce(),
	}
}

// commandGroups builds the ordered command groups from the chat command tree,
// omitting hidden commands.
func (b *botBiz) commandGroups(ctx context.Context, sender v1.Sender, recv *v1.Receiver) []*commandGroup {
	dr := b.cardNavRuntime(ctx, sender, recv)
	cctx := runtime.Inject(ctx, dr)
	out := runtime.NewStructuredResponder(dr)
	root := command.NewBotCommandTree(cctx, b.sr, out)

	order := make([]string, 0)
	byName := make(map[string]*commandGroup)
	for _, c := range root.Commands() {
		if c.Hidden {
			continue
		}
		name := c.Annotations[chat.ChatGroup]
		if name == "" {
			name = "其他"
		}
		g, ok := byName[name]
		if !ok {
			g = &commandGroup{name: name}
			byName[name] = g
			order = append(order, name)
		}
		short := c.Short
		if short == "" {
			short = c.Name()
		}
		g.items = append(g.items, commandInfo{name: c.Name(), short: short})
	}

	groups := make([]*commandGroup, 0, len(order))
	for _, n := range order {
		groups = append(groups, byName[n])
	}
	return groups
}

// renderHelpCard renders the root menu: one row per command group, navigable
// into renderGroupCard, plus session-level shortcuts (history, reasoning).
func (b *botBiz) renderHelpCard(ctx context.Context, sender v1.Sender, recv *v1.Receiver) *larkcard.Card {
	groups := b.commandGroups(ctx, sender, recv)

	cb := larkcard.NewCard().
		Title("帮助", "blue").
		Markdown("输入 **/命令** 直接执行，或点击下方分组查看命令。").
		Buttons(
			larkcard.DefaultBtn("📜 最近消息", "nav:/history"),
			larkcard.DefaultBtn("🧠 推理开关", "nav:/reasoning"),
			larkcard.DefaultBtn("🤖 切换 Agent", "nav:/switch"),
		)

	for _, g := range groups {
		cb.ListItem(chatGroupLabel(g.name), "查看", "nav:group:"+g.name)
	}
	cb.Note("ChatOps 命令，按需执行")
	return cb.Build()
}

// renderHistoryCard renders the most recent cached messages for the receiver's
// conversation, sourced from the message cache populated by the IM relay.
func (b *botBiz) renderHistoryCard(ctx context.Context, recv *v1.Receiver) *larkcard.Card {
	const historyLimit = 10
	recs, err := b.sr.Store.Messages().ListByConv(ctx, recv.Id, historyLimit)
	if err != nil || len(recs) == 0 {
		return larkcard.NewCard().Title("最近消息", "blue").Markdown("暂无历史消息").Build()
	}

	cb := larkcard.NewCard().Title("最近消息", "blue")
	for _, r := range recs {
		name := r.SenderName
		if name == "" {
			name = r.SenderID
		}
		if name == "" {
			name = "未知"
		}
		content := strings.TrimSpace(r.Content)
		if len(content) > 120 {
			content = content[:120] + "…"
		}
		cb.Markdown("**" + name + "**: " + content)
	}
	return cb.Build()
}

// renderReasoningCard shows the current reasoning toggle and offers a button to
// flip it. Toggling is a side-effecting "act:" action that mutates the
// per-session preference and re-renders the card in place.
func (b *botBiz) renderReasoningCard(recv *v1.Receiver, arg string) *larkcard.Card {
	switch strings.ToLower(arg) {
	case "on", "开":
		b.setReasoning(recv.Id, true)
	case "off", "关":
		b.setReasoning(recv.Id, false)
	}

	on := b.reasoningEnabled(recv.Id)
	state := "开启 " + larkcard.GlyphDone
	btnLabel := "关闭"
	action := "act:/reasoning off"
	if !on {
		state = "关闭 " + larkcard.GlyphFailed
		btnLabel = "开启"
		action = "act:/reasoning on"
	}

	return larkcard.NewCard().
		Title("推理开关", "blue").
		Markdown("**当前状态**: " + state).
		Markdown("开启后，AI 在回答前会展示其推理过程（需模型支持）。").
		Buttons(larkcard.PrimaryBtn(btnLabel, action)).
		Build()
}

// renderAgentCard lists the agents available from oai-agent-apiserver and marks the
// currently declared one. Each agent row replaces the conversation's declaration
// with that agent.
//
// It also says what the declaration resolves to right now, which is the half a
// declaration cannot express on its own: `@peer` means one thing to somebody who
// has bound an account and another to somebody who has not.
func (b *botBiz) renderAgentCard(ctx context.Context, event *callback.CardActionTriggerEvent, recv *v1.Receiver) *larkcard.Card {
	// Read from the session row, not from a process-local snapshot: the
	// declaration is shared state, and a card that showed one instance's opinion
	// of it would disagree with the next message's answer.
	refs := b.sessionRefs(ctx, recv.Id)
	agents, err := b.sr.Clientset.Agent().ListAgent(ctx)
	if err != nil {
		return larkcard.NewCard().Title("切换 Agent", "blue").Markdown("无法获取 Agent 列表").Build()
	}

	cb := larkcard.NewCard().Title("切换 Agent", "blue")
	// Labelled the declaration rather than "the current agent": in a private chat
	// a usable binding outranks this list, and the line rendered below is the one
	// that reports what the caller actually runs as.
	if len(refs) == 0 {
		cb.Markdown("**会话声明**: 跟随本会话类型的默认")
	} else {
		cb.Markdown("**会话声明**: " + strings.Join(refs, ", "))
	}
	// Rendered as its own line rather than folded into the one above: it carries
	// markdown of its own (the agent names) and bolding around backticks is not
	// the emphasis it looks like.
	if line := b.resolvedAgentLine(ctx, event); line != "" {
		cb.Markdown(line)
	}

	cb.ListItemBtn("跟随默认", "选择", "primary", "act:/switch")
	for _, a := range agents {
		name := a.GetName()
		if name == "" {
			continue
		}
		desc := a.GetDisplayName()
		if desc == "" {
			desc = name
		}
		// A declaration is a list; this card can only express the one-element
		// case, so the tick marks exactly that rather than "the list contains
		// this name" — which would tick every row of a longer declaration.
		if len(refs) == 1 && refs[0] == name {
			desc = larkcard.GlyphDone + " " + desc
		}
		cb.ListItemBtn(desc, "选择", "default", "act:/switch "+name)
	}
	cb.Note("选择后，本会话的 AI 回答由该 Agent 的设定驱动")
	return cb.Build()
}

// selectAgent replaces the conversation's declaration (or clears it, for an
// empty arg) and re-renders the card in place.
//
// # Why this checks the caller's role
//
// The declaration decides which agent answers everyone in this conversation.
// Anyone in a group can press a card button, so without this check any member
// could point the conversation at an agent of their choosing — and the answers
// would then be attributed to, and scoped by, whoever asked next.
//
// The check reads the identity the card event carries. It is the same list the
// /agent command consults; the card is a second way to reach one action, not a
// second policy.
func (b *botBiz) selectAgent(ctx context.Context, event *callback.CardActionTriggerEvent, recv *v1.Receiver, arg string) *larkcard.Card {
	if !b.sr.IsAdminUser(senderOfCardOperator(event).Identity()) {
		return larkcard.NewCard().Title("切换 Agent", "red").
			Markdown("只有管理员可以修改本会话的 Agent。").Build()
	}
	if arg == "" || arg == "default" {
		// Clearing, not naming the default: see setSessionRefs.
		if err := b.setSessionRefs(ctx, recv.Id, nil); err != nil {
			slog.WarnContext(ctx, "failed to clear the session agent declaration",
				"conv_id", recv.Id, "error", err)
		}
		return b.renderAgentCard(ctx, event, recv)
	}
	if err := agentref.ValidateAgentName(arg); err != nil {
		return larkcard.NewCard().Title("切换 Agent", "red").Markdown(err.Error()).Build()
	}
	if err := b.setSessionRefs(ctx, recv.Id, []string{arg}); err != nil {
		slog.WarnContext(ctx, "failed to record the session agent declaration",
			"conv_id", recv.Id, "agent", arg, "error", err)
	}
	return b.renderAgentCard(ctx, event, recv)
}

// resolvedAgentLine asks the scheduler what the declaration the card is showing
// resolves to for the person looking at it.
//
// It is the same line /agent reports, from the same call, because the question
// is the same one: a declaration is an intent, and only the scheduler knows
// whether this speaker's intent currently picks anything. Two wordings would be
// two chances to disagree about the same conversation.
//
// It resolves through a full request context rather than the lighter one the
// rest of this file builds — the declaration is resolved against this
// conversation's row and this speaker's binding, and the lighter context carries
// neither. That is also why the raw card event had to be threaded this far: the
// speaker's open_id is on it and nowhere else.
func (b *botBiz) resolvedAgentLine(ctx context.Context, event *callback.CardActionTriggerEvent) string {
	if event == nil {
		// A caller that renders this card without a card event — a test, or a
		// future entry point — gets the declaration without the resolution rather
		// than a wrong one.
		return ""
	}
	dr, err := b.runtimeFor(ctx, b.cardEventContext(event))
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the agent declaration for the card", "error", err)
		return ""
	}
	res, err := b.resolvedAgents(ctx, dr)
	if err != nil {
		// Local, and harmless to lose: the declaration is stored either way, and
		// the next message will either resolve it or report the same error with a
		// reader who is expecting an answer.
		slog.WarnContext(ctx, "could not resolve what the agent declaration picks", "error", err)
		return ""
	}
	return res.Line()
}

// renderGroupCard renders one command group's commands, each row running the
// command on click.
func (b *botBiz) renderGroupCard(ctx context.Context, sender v1.Sender, recv *v1.Receiver, group string) *larkcard.Card {
	var target *commandGroup
	for _, g := range b.commandGroups(ctx, sender, recv) {
		if g.name == group {
			target = g
			break
		}
	}
	if target == nil {
		return larkcard.NewCard().Title(chatGroupLabel(group), "grey").Markdown("无此分组").Build()
	}

	cb := larkcard.NewCard().Title(chatGroupLabel(group), "blue")
	for _, item := range target.items {
		cb.ListItem(item.short, "/"+item.name, "act:/"+item.name)
	}
	cb.Note("点击按钮执行对应命令")
	return cb.Build()
}

// renderStatusCard renders a lightweight bot status card.
func (b *botBiz) renderStatusCard(recv *v1.Receiver) *larkcard.Card {
	return larkcard.NewCard().
		Title("状态", "blue").
		Markdown("**会话**: " + recv.Id).
		Note("输入 /version 查看版本，/help 查看命令").
		Build()
}

// renderCommandCard runs a command and renders its captured output as a card.
func (b *botBiz) renderCommandCard(ctx context.Context, sender v1.Sender, recv *v1.Receiver, cmd, args string) *larkcard.Card {
	cmdLine := cmd
	if args != "" {
		cmdLine = cmd + " " + args
	}

	dr := b.cardNavRuntime(ctx, sender, recv)
	cctx := runtime.Inject(ctx, dr)
	out := runtime.NewStructuredResponder(dr)
	// The error reaches the user through the card the responder drew, so the
	// card surface has nothing to add to it.
	_ = b.executeCommand(cctx, cmdLine, out)
	res := out.Result()

	switch {
	case res.Interactive != nil:
		// The command built a card of its own (a /pod answer is a picker, not a
		// paragraph). Hand it back as-is; the navigation handler renders it with
		// the session key.
		return res.Interactive
	case res.Markdown != nil && res.Markdown.Content != "":
		color := res.Markdown.Color
		if color == "" {
			color = "blue"
		}
		cb := larkcard.NewCard().Markdown(res.Markdown.Content)
		if res.Markdown.Title != "" {
			cb.Title(res.Markdown.Title, color)
		} else {
			cb.Title("/"+cmd, color)
		}
		return cb.Build()
	case res.PlainText != "":
		return larkcard.NewCard().Title("/"+cmd, "blue").Markdown(res.PlainText).Build()
	case res.Raw != nil:
		raw, _ := json.MarshalIndent(res.Raw, "", "  ")
		return larkcard.NewCard().Title("/"+cmd, "blue").Markdown("```json\n" + string(raw) + "\n```").Build()
	default:
		return larkcard.NewCard().Title("/"+cmd, "green").Markdown("已执行").Build()
	}
}
