# duty 集成

duty 是 `/oncall`、`/duty`、`/open`、`/close`、`/save`、`/hand`、`/mark` 等 ChatOps
命令背后的值班与事件能力。

## 契约

`pkg/duty` 定义 provider 契约：

```go
type Provider interface {
    Name() string
    Configured() bool
    Roster
    Incidents
}

type Roster interface {
    Schedules(ctx context.Context) ([]Schedule, error)
    OnCall(ctx context.Context, scheduleID string) (Person, error)
}

type Incidents interface {
    CreateIncident(ctx context.Context, in CreateIncidentRequest) (Incident, error)
    GetIncident(ctx context.Context, id string) (Incident, error)
    UpdateIncident(ctx context.Context, id string, in UpdateIncidentRequest) (Incident, error)
}
```

provider 还可以实现 `duty.TransitionChecker` 来描述单向事件状态机；工单镜像在推送状态
变更前会先询问它。

## 选择 provider

```yaml
duty:
  provider: regen   # none | regen
```

- `none`（或未配置 `backend.regen-endpoint` 时留空）：不读取外部排班；原生排班仍从
  `chat.oncall_*` 表加载，未迁移的 Regen 组没有值班人，工单仅保存在本地。
- `regen`：`pkg/duty/regen` 使用 `backend.regen-username` /
  `backend.regen-password` 登录，读取 `backend.regen-endpoint` 的排班，并把工单镜像成
  incident。`backend.regen-user-map` 把排班中的自由文本名字映射到飞书 `user_id`。

旧的推断逻辑仍然有效：只配置 `backend.regen-endpoint` 而不写 `duty.provider` 也会选中
Regen。

## 原生排班（默认）

值班组的排班、轮转图层、Override、不可用、节假日与升级策略现在存放在
`chat.oncall_*` 表，由 `oai-chat-apiserver` 的 `/v1/oncall-groups/:name/schedule`
管理。duty store 每次刷新时：

1. 有原生排班的组直接用本地求值器计算当前值班人，不访问 provider；
2. 只有仍保留 `schedule_id` 且没有原生排班的组才回退到 provider；
3. provider 未配置时，原生排班照常工作，legacy 组显示为不可用。

迁移顺序：先运行 `hack/crd2pg` 把组元数据从控制面回填到 `chat.oncall_group`，再运行
`hack/regen-import` 把 Regen 排班导入原生表。两者都支持 `--dry-run`。

## 实现其他 provider

1. 在新包（例如 `pkg/duty/mysystem`）实现 `duty.Provider`。
2. 把状态映射到 `duty.Status*`、严重级别映射到 `duty.Severity*`。
3. 状态机拒绝变更时从 `UpdateIncident` 返回 `duty.ErrTransitionNotAllowed`；如果希望
   调用方在调用前判断，实现 `CanTransition`。
4. 在 `internal/chat/providers.go` 的 `ProvideDutyProvider` 增加分支。
5. 补映射测试；没有迁移不要改持久化列（`schedule_id`、`incident_id`、`source`）。

## 行为保证

- provider 故障不会阻止聊天服务启动；duty store 保持为空，命令报告值班信息不可用。
- 在配置 provider 之前创建的工单没有 incident，保持本地。
- `source` 记录 provider 名称；历史行保持 `regen`。
- 关闭与解决共享 provider 的 resolved 状态；当 provider 状态机是单向时，重新打开会创建
  新的 incident。
