# Agent 长期记忆（Memory）

## 目标

把长期记忆做成平台一等能力：

- `Memory` 是声明式资源（CRD），描述 embedding、抽取、保留、检索、安全与作用域。
- `MemoryEntry` 是 SQL 数据资源，保存真正的高频条目与向量。
- 普通用户、管理员、服务账号各走一套路由面，subject 由服务端强制或显式给出。

## 资源模型

`Memory`（`agent.onexai.io/v1alpha1`，namespace 级）：

| 字段 | 说明 |
| --- | --- |
| `spec.enabled` | 是否启用；关闭后运行时不再读写 |
| `spec.embedding.modelRef/dimensions/version` | embedding 模型、固定维度、代次 |
| `spec.extraction.enabled/modelRef/maxFactsPerRun` | 运行后自动抽取 |
| `spec.retention.ttlDays/maxEntriesPerSubject` | 保留期与每人上限 |
| `spec.retrieval.mode/topK/minScore/maxTokens` | 召回策略 |
| `spec.security.piiRedaction/blockInstructionLike/allowAdminContentRead` | PII 与投毒防护 |
| `spec.scope.subjectMode` | `user`（默认）或 `session`；`session` 时存储桶是会话 ID，而非用户 ID |

Agent 与 AgentTemplate 通过 `memoryRef` 单一引用 Memory：

```yaml
spec:
  memoryRef:
    name: team-memory
```

`AgentBinding` 不贡献 Memory：它是能力的并集，而 Memory 是单值、有状态配置。

`MemoryEntry`（`agent.memory_entry`）关键字段：

`id, namespace, memory, memory_uid, agent, subject, session_id, run_id, type, status,
content, dedupe_key, embedding, embedding_model, embedding_version, embedding_status,
source, actor, importance, confidence, access_count, last_accessed_at, expires_at,
tags, metadata, version, created_at, updated_at, deleted_at`

## API

配置面（声明式，`UnifiedAuth`）：

```text
POST   /v1/agent/memories
GET    /v1/agent/memories?offset=&limit=
GET    /v1/agent/memories/{memory}
PUT    /v1/agent/memories/{memory}
DELETE /v1/agent/memories/{memory}
```

条目面（浏览器普通用户，`SelfService`，subject 强制为 JWT `user_id`）：

```text
GET/POST   /v1/user/memories/{memory}/entries
GET/PATCH/DELETE /v1/user/memories/{memory}/entries/{id}
POST       /v1/user/memories/{memory}/entries/search
POST       /v1/user/memories/{memory}/entries/batch-delete
GET        /v1/user/memories/{memory}/stats
```

管理面（管理员，可跨 subject）：

```text
GET/POST   /v1/admin/agent/memories/{memory}/entries
GET/PATCH/DELETE /v1/admin/agent/memories/{memory}/entries/{id}
POST       /v1/admin/agent/memories/{memory}/entries/search
POST       /v1/admin/agent/memories/{memory}/entries/batch-delete
GET        /v1/admin/agent/memories/{memory}/stats
GET        /v1/admin/agent/memories/{memory}/subjects
DELETE     /v1/admin/agent/memories/{memory}/subjects/{subject}
```

服务间面（AK/SK）：与管理员面同路径，但挂在签名组 `/v1/agent/memories/{memory}/...`，subject 显式。

## 写入与检索

- 手工 / 工具 / 运行后自动保存统一进入 `Service.Create`。
- 自动保存调用运行时的 summarizer 做**原子事实抽取**：要求返回 JSON 数组
  （content/type/importance/confidence/tags），每轮最多 `maxFactsPerRun` 条；
  解析失败或模型不可用时退化为一条 episodic 摘要。
- `spec.extraction.modelRef` 指定抽取模型时优先使用该 Model；未指定、模型缺失或
  已禁用时回退到部署级 summarizer。
- `dedupeKey = sha256(namespace|memory|subject|type|normalize(content))`；命中已有 active 条目时合并重要度/可信度并返回已有条目。
- embedding 异步生成；成功后做近重复合并（cosine ≥ 0.92，保留更长内容，合并 tags，
  另一条置为 archived）。失败记录 `embedding_status=failed`，检索降级为关键字。
- 每 subject 超过 `retention.maxEntriesPerSubject` 时，按重要度/更新时间保留前 N 条，
  其余置为 archived。
- 检索：过滤 `namespace + memory + subject + status=active + expires_at`，向量候选与关键字候选做 RRF，再叠加 recency/importance/confidence；返回 `scoreBreakdown`。
  - 关键字分支使用词频得分（query 的每个词在正文中的出现次数），不是二值包含。
  - 排名后做 MMR 多样性重排（λ=0.7）：与已选条目向量高度相似的后续条目会被降权，
    避免同一事实的多条副本挤占 TopK；无 embedding 时退化为原排名。
- 检索测试 `persist=false` 不增加访问次数；`explain=true` 会写一条 `action=search`
  审计（只记录 query hash、结果数与降级标记，不记录 query 原文）。运行时 prefetch
  不写检索审计，避免高频写入。

## 审计

`agent.memory_audit` 记录 create/update/delete/dedupe/purge，字段包括 namespace、
memory、subject、entryId、actor、action、source、reason、createdAt；不保存正文。
管理员通过 `GET /v1/admin/agent/memories/{memory}/audit` 查询，控制台「记忆库 →
审计」页签展示。

## 运维任务

- **Re-embedding**：`POST /v1/admin/agent/memories/{memory}/reembed`，支持
  `dryRun` 与 `limit`（默认 50，最大 200）；对 `embedding_version` 与当前 Memory
  不一致、或 `embedding_status` 非 ready 的条目重算向量。返回
  `scanned/updated/failed/remaining`，可重复执行直到 remaining 为 0。控制台详情页
  头部提供「检查待重建」「重建索引」按钮。
- **检索评测**：`POST /v1/admin/agent/memories/{memory}/eval`，请求体包含
  `subject`、`topK`、`mode` 和 `cases:[{query, expectedIds}]`；对每条用例运行一次
  检索，返回 precision@k、recall@k、MRR、实际命中的条目 ID 与缺失 ID，并给出
  平均值。评测不写检索审计、不增加访问次数。控制台「记忆库 → 评测」页签提供
  JSON 用例编辑与结果表。
- **维护 worker**：agent apiserver 启动后每小时执行一次：
  - 将 `expires_at` 已过的 active 条目置为 `expired`；
  - 物理删除软删除满 30 天的条目。
  - 列表的默认 active 过滤也会排除已过期条目，保证与检索一致。

### 作用域语义

- `user`：存储桶 = 用户 subject；跨会话共享。
- `session`：存储桶 = 会话 ID（运行时用 `SessionKey`/`SessionID`）；同一会话内共享，
  不同会话隔离。运行时工具、prefetch、自动保存都会带上会话 ID。
- 管理端按「会话 ID」浏览会话级记忆；普通用户页面不提供手工新增（由运行时写入）。
  用户级记忆仍按 subject 浏览。

## 安全

- 普通用户条目读写强制 `subject = JWT user_id`；伪造 subject 返回参数错误。
- 管理员面由 `RequireAdmin` 保护；跨 subject 查询必须显式给 subject。
- 跨 subject 读取正文受 `security.allowAdminContentRead` 控制；未开启时返回显式脱敏占位符，
  元数据、归属、删除/purge 仍可用。普通用户读自己的 subject 不受此限制。
- Memory 配置写操作仅管理员（`UserGuard`）。
- PII 支持 redact/hash/drop；指令型内容可拒绝；超长/重复写入由调用方限流。
- 删除为软删；`DELETE .../subjects/{subject}` 是 GDPR purge，返回删除计数。
- Agent/AgentTemplate 引用 Memory 时，admission 会阻止删除该 Memory。

## 向量索引（pgvector）

PostgreSQL 安装 `vector` 扩展后，检索自动切到数据库侧：

- `agent.memory_entry.embedding_vec vector` 与可移植的 `embedding` blob 同步写入；
  blob 仍是 MMR、近重复合并以及 MySQL/SQLite 的读取来源。
- 每个 embedding 维度自动建立**表达式部分 HNSW 索引**：

  ```sql
  CREATE INDEX ... ON agent.memory_entry
    USING hnsw ((embedding_vec::vector(N)) vector_cosine_ops)
    WHERE vector_dims(embedding_vec) = N;
  ```

  因此同一张表可以容纳不同模型维度的 Memory，且各自走自己的索引。
- 检索候选 = HNSW 近邻 ∪ 关键字 `ILIKE` 预筛选；pgvector 0.8+ 打开
  `hnsw.iterative_scan = relaxed_order`，避免过滤后候选不足。
- 索引在 Memory 创建/更新、首次写入以及服务启动时按需创建（`CREATE INDEX CONCURRENTLY`，
  不阻塞写入）。
- 扩展缺失或 DDL 失败时自动回退到有界扫描 + 进程内 cosine，检索不会因此失败；
  日志会给出 `the pgvector extension is not installed` 提示。

安装：

```bash
# Debian / Ubuntu（发行版仓库或 PGDG）
sudo apt install postgresql-17-pgvector

# macOS
brew install pgvector          # Postgres.app 18 已自带
```

```sql
CREATE EXTENSION IF NOT EXISTS vector;   -- 每个存放记忆的数据库执行一次
```

应用不会自行执行 `CREATE EXTENSION`：它需要超级用户，应由 DBA 在部署阶段完成。
`embedding_vec` 列由服务检测到扩展后自动补齐。

## 运维

- 新表 `agent.memory_entry` 由 `registry.Migrate` 自动创建；旧原型表 `agent.memory` 可用
  `hack/drop-agent-memory-prototype.sql` 清理。
- rerank、冲突消解仍在后续阶段。
