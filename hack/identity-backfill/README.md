# identity-backfill

一次性把存量数据里的飞书 **open_id** 改写成 **user_id**。

## 为什么需要它

onexai 以前用「手边哪个 ID 就用哪个」的方式标识一个人：有的列存 open_id，有的列存 user_id。两者不可互换——

- `open_id` 是**应用内**唯一：同一个人在 chat 的机器人、将来的第二个机器人、opsassist 里各有一个不同的 open_id；它是 `ou_xxx` 随机串，运维没法写进配置文件，应用重建/迁移后全部失效。
- `user_id` 是**租户内**唯一、由管理员按工号/邮箱定义（`go.kong`），SRE 名单、`regen-user-map`、`/invite`、建群、agent 的 authz 规则、归属键全都按它写。

代码已经统一到 user_id（见 `internal/chat/im`、`pkg/api/chat/v1`），于是**改写前落库的行仍然指着旧的那套 ID**。这个工具负责把它们搬过去：找出列里残留的 open_id，调飞书通讯录接口换出 user_id，再回写。

幂等：再跑一次找不到任何 open_id，什么也不做。

## 用法

```bash
go run ./hack/identity-backfill \
  --onex-mysql-dsn='user:pass@tcp(host:3306)/onex?parseTime=true' \
  --lark-app-id=<飞书应用 App ID> \
  --lark-app-secret=<飞书应用 App Secret>
```

**默认是 dry-run**：只扫描、解析、报告，不写任何东西。确认报告无误后加 `--apply` 才会真正回写。

（这里用 `--apply` 而不是 `opsassist-migrate` 的 `--dry-run`，是因为那个工具只往空表里插行，而这个工具**原地覆盖**已有的列——默认写、需要显式 `--dry-run` 才安全，反过来才对。）

先看一下会动多少行：

```bash
go run ./hack/identity-backfill ... --apply=false --limit=100
```

### 参数

| 参数 | 说明 |
|---|---|
| `--onex-mysql-dsn` | 目标 onex 库（必填）。本工具**固定连 MySQL**（改写的是冻结的旧库），因此下表用的是 MySQL 表名；切到 PostgreSQL 后同样的表名是 `chat.*` / `job.*` |
| `--lark-app-id` / `--lark-app-secret` | 用来调通讯录、把 open_id 换成 user_id 的飞书自建应用（必填）。**应用必须已开启「获取用户 user ID」权限**，否则报告里全是 unresolved |
| `--tables` | 只处理其中几列，`表.列` 逗号分隔，如 `onexai_chat_chat_message.sender_id,onexai_chat_ticket.reporter_id`；默认 `all` |
| `--apply` | 真正回写；不传则只报告 |
| `--limit` | 每列最多处理多少个 id（0 = 不限），用于先小范围试跑 |
| `--resolve-sleep` | 每次调通讯录接口后的停顿，默认 120ms，避免触发限流 |

### 报告怎么看

```
column                                      ids     rows  rewritable
----------------------------------------------------------------------
onexai_chat_chat_message.sender_id    812     9421        9421
onexai_chat_ticket.reporter_id          7       12          11
onexai_chat_ticket.assignee_id          3        4           4

sample of the mapping (of 815 resolved):
  ou_1f2e... -> go.kong

unresolved (1 ids, 1 rows) — left as-is; check that the app may read user IDs:
  ou_9ab7... (1 rows)
```

`unresolved` 是这次的残差：一般是**人已离职**（租户里查不到）或**应用缺权限**。这部分行保持原样（回写不会半点成功半点失败），需要人工处理。

## 覆盖的列

| 表.列 | 来源 |
|---|---|
| `onexai_chat_chat_message.sender_id` | 机器人归档的每条群消息；历史值来自 opsassist 导入 |
| `onexai_chat_session.session_id` / `chat_id` | 仅 `session_type='private_chat'`：私聊会话按人建键，而消息现在按 user_id 查，旧键永远查不到 |
| `onexai_chat_ticket.reporter_id` / `assignee_id` | opsassist 从多维表格人员字段导入的标量列（旧名 `creator_id` / `primary_assignee_id`） |
| `onexai_jobserver_job.username` / `onexai_jobserver_cronjob.username` | Job/CronJob 的归属人（`cmdutil.ResolveUsername`），从飞书建的单归属人是当时发出的 ID |

## 与 opsassist-migrate 的关系

`hack/opsassist-migrate` 是这些历史 open_id 的来源，它现在也接上了同一个解析器（`internal/pkg/larkkit.IdentityResolver`）：新导入的行直接写 user_id，解析不了的行把 open_id 留在原字段并告警。所以正确顺序是：

```
opsassist-migrate  →  identity-backfill（把两边都解析不到的残差再扫一遍）
```

两者都幂等，顺序对调也只是多做一次扫描。
