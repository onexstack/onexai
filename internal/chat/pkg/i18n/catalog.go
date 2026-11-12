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

package i18n

// builtinCatalog holds this service's translations.
//
// Its coverage is deliberate and narrow: only the strings the CLI-agent port
// introduced — /new, /switch, /sessions and /doctor — are listed. The rest of the
// service's messages, and the help text of even these four commands, are sent in
// the source language. That is not a gap in this file so much as the property
// that makes it additive: a string nobody has translated keeps working exactly as
// it did, so the catalog can grow one entry at a time instead of requiring a
// sweep before the feature is usable.
//
// TestEveryTranslatedCallHasAnEntry enforces the other direction: once a call
// site asks for a translation, the entry has to exist.
//
// zh needs no entries — the source strings are the Chinese ones. zh-TW has none
// yet and falls back to zh, which a Traditional Chinese reader will find odd in
// places but readable; that fallback is the whole reason the chain exists.
var builtinCatalog = Catalog{
	EN: map[string]string{
		// --- /new, /switch (internal/chat/pkg/command/ai/new.go) ---

		"后端只能是 llm 或 cli，收到：%s":  "The backend can only be llm or cli, got: %s",
		"本服务未启用会话管理，无法切换后端":      "Session management is not enabled on this service, so the backend cannot be switched",
		"本服务未配置 Agent 后端":        "This service has no agent backend configured",
		"无法确定当前会话，请在具体的聊天中使用该命令": "This conversation cannot be identified; run this command from inside a chat",
		"该后端未启用：%s。可用：%s":        "That backend is not enabled: %s. Available: %s",

		"已切换到 **%s** 后端。": "Switched to the **%s** backend.",
		"当前后端：**%s**。":    "Current backend: **%s**.",

		"本机 CLI 会话在自己的目录里运行，工具调用需要逐次批准。": "The local CLI session runs in its own directory, and each tool call needs approval.",
		"由 oai-agent-apiserver 提供服务。":    "Served by oai-agent-apiserver.",
		"上下文已清空，这是一个新的对话。":               "The context has been cleared; this is a new conversation.",
		"上下文保留，可以继续之前的对话。":               "The context is kept; you can carry on from before.",
		"上下文保存在对应的后端上。":                  "The context is kept on the backend that owns it.",

		// --- /dir (internal/chat/pkg/command/ai/dir.go) ---

		"本服务未启用会话管理，无法指定工作目录":                "Session management is not enabled on this service, so the working directory cannot be set",
		"--default 与路径只能给出一个":                "--default and a path cannot be given together",
		"无法使用该目录：%v。允许的目录：%s":                "That directory cannot be used: %v. Allowed directories: %s",
		"已恢复为会话自带的目录。":                       "Back to this conversation's own directory.",
		"已把本对话的工作目录设为 **%s**。":               "This conversation now works in **%s**.",
		"当前后端是 %s，本机 CLI 会话建立后生效。":           "The current backend is %s; this takes effect once a local CLI session is established.",
		"本对话的工作目录：**%s**（已指定）\n":             "This conversation's working directory: **%s** (set with /dir)\n",
		"会话自带的目录是 %s，用 /dir --default 可以回去。": "The conversation's own directory is %s; /dir --default goes back to it.",
		"本对话的工作目录：**%s**（会话自带）":              "This conversation's working directory: **%s** (its own)",
		"可用的目录：%s":                           "Available directories: %s",
		"（只列出根目录，根目录下的任意已存在目录都可以用）":          "(only the roots are listed; any existing directory under a root may be used)",
		"本机 CLI 后端未启用，无法指定工作目录":              "The local CLI backend is not enabled, so the working directory cannot be set",
		"当前 Agent 后端不支持指定工作目录":               "The current agent backend does not support choosing a working directory",
		"本机 CLI 后端未配置工作区根目录，无法指定工作目录":        "The local CLI backend has no workspace root configured, so the working directory cannot be set",

		// --- /sessions (internal/chat/pkg/command/ai/sessions.go) ---

		"本服务未启用会话管理": "Session management is not enabled on this service",
		"该命令只能在私聊中使用：它报告的是机器人所在主机的运行情况": "This command only works in a private chat: it reports on the host the bot runs on",

		"本机当前有 **%d** 个会话在运行，其中 **%d** 个正在处理消息。\n": "**%d** conversations are running on this host, **%d** of them handling a message.\n",
		"- %s 后端：%d 个\n":                "- %s backend: %d\n",
		"有 %d 个会话正在等待审批。\n":             "**%d** conversations are waiting for an approval.\n",
		"本对话没有可识别的会话标识。":                "This conversation has no identifiable key.",
		"本对话还没有开始：发送一条消息或使用 /new 即可新建。": "This conversation has not started yet: send a message, or use /new.",
		"本对话：后端 **%s**":                 "This conversation: backend **%s**",
		"，已建立本机会话":                      ", a local session is established",
		"，尚未建立本机会话（下一条消息开始时建立）":         ", no local session yet (it starts with the next message)",
		"，正在处理消息":                       ", handling a message",
		"，正在等待审批":                       ", waiting for an approval",
		"，空闲 %s":                        ", idle for %s",
		"。":                             ".",

		// --- /doctor (internal/chat/pkg/command/core/doctor.go) ---

		"服务未初始化，无法自检": "The service is not initialised, so it cannot self-check",
		"该命令只能在私聊中使用：它报告的是机器人所在主机与部署的配置": "This command only works in a private chat: it reports on the host and the deployment configuration",

		"**服务自检**\n":   "**Service self-check**\n",
		"没有发现需要注意的问题。": "Nothing needs attention.",
		"**需要注意**\n":   "**Needs attention**\n",
		"- %s\n":       "- %s\n",

		"- 会话：未启用\n": "Sessions: not enabled\n",
		"会话管理未启用：/new、/switch、排队与空闲回收都不可用": "Session management is not enabled: /new, /switch, queueing and idle reaping are all unavailable",
		"- 会话目录：%s\n": "- Session directory: %s\n",
		"- 会话：%d 个在运行（%d 个处理中，%d 个等待审批）\n": "- Sessions: %d running (%d handling a message, %d waiting for an approval)\n",
		"  - %s：%d 个\n": "  - %s: %d\n",
		"- 单轮上限：%s\n":   "- Turn limit: %s\n",

		"- Agent 后端：无\n":           "- Agent backends: none\n",
		"没有注册任何 Agent 后端：AI 对话不可用": "No agent backend is registered: AI conversations are unavailable",
		"- Agent 后端：%s（默认 %s）\n":   "- Agent backends: %s (default %s)\n",
		"  - cli 后端已启用：会话消息可在本机运行 agent 进程，仅管理员可切换\n": "  - the cli backend is enabled: a chat message can run an agent process on this host, and only an admin can switch to it\n",
		"默认后端是 llm，但它没有注册：未显式切换后端的会话都会失败":             "The default backend is llm but it is not registered: every conversation that has not switched explicitly will fail",

		"- 流式输出：关闭（回答在结束时一次性发出）\n":                 "- Streaming: off (the answer is sent in one piece at the end)\n",
		"- 流式输出：开启（间隔 %s，最小增量 %d 字符，预览上限 %d 字符）\n": "- Streaming: on (interval %s, minimum delta %d chars, preview limit %d chars)\n",
		"流式节流已关闭：每个增量都会触发一次卡片更新，可能触及飞书的更新频率上限":     "Streaming throttle is off: every delta triggers a card update, which may hit Feishu's update rate limit",
		"- 消息去重窗口：%s\n": "- Message dedup window: %s\n",

		"- 授权：SRE %d 人，管理员 %d 人\n":                    "- Authorisation: %d SREs, %d admins\n",
		"- 角色名单读取于 %s\n":                              "- Role lists read at %s\n",
		"- 名单最近一次刷新失败：%s（仍在使用上一次成功读取的名单）\n":           "- The last role-list refresh failed: %s (the previous snapshot is still in use)\n",
		"已启用 cli 后端但管理员名单为空：没有人能用 /new 或 /switch 切到它": "The cli backend is enabled but the admin list is empty: nobody can switch to it with /new or /switch",
		"管理员名单为空：/new、/switch、/sessions、/doctor 无人可用": "The admin list is empty: /new, /switch, /sessions and /doctor are available to nobody",
		"SRE 名单为空：所有 x-sre-only 命令无人可用":               "The SRE list is empty: every x-sre-only command is available to nobody",
		"- 入站限制：": "Inbound limits: ",
		"已启用 cli 后端且未设置来源/群聊白名单：任何能私聊机器人的人都能驱动本机 agent（命令本身仍限管理员）": "The cli backend is enabled with no sender/chat allow-list: anyone who can message the bot can drive the local agent (the commands themselves are still admin-only)",

		"来源不限":    "senders unlimited",
		"群聊不限":    "chats unlimited",
		"来源 %d 项": "senders: %d entries",
		"群聊 %d 项": "chats: %d entries",
		"仅群聊":     "group chats only",
		"仅回复触发消息": "replies to the triggering message only",

		"- oai-agent-apiserver：": "oai-agent-apiserver: ",
		"未配置\n":                  "not configured\n",
		"已配置\n":                  "configured\n",
		"- 数据库：":                 "Database: ",
		"- DNS 白名单：":             "DNS allow-list: ",
		"未加载\n":                  "not loaded\n",
		"已加载\n":                  "loaded\n",
		"未配置 oai-agent-apiserver：llm 后端不可用，/clear、Agent 列表等命令也会失败": "oai-agent-apiserver is not configured: the llm backend is unavailable, and so are /clear, the agent listing and other commands",
	},
}
