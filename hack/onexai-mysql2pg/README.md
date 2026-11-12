# onexai-mysql2pg

把 oai-agent-apiserver / oai-chat-apiserver / oai-jobserver 的 22 张表从 MySQL 搬到 PostgreSQL。

## 它做什么

服务从 MySQL 切到 PostgreSQL 时，表名和物理类型都会变：

| | MySQL | PostgreSQL |
|---|---|---|
| 布局 | 一个库，表带服务前缀 | 一个库，每服务一个 schema |
| 表名 | `onexai_chat_session` | `chat.session` |
| 类型 | `longtext` / `tinyint(1)` / `blob` / `datetime(3)` | `text` / `boolean` / `bytea` / `timestamptz` |

工具用**服务自己的 GORM 模型**读 MySQL、写 PostgreSQL：类型转换由模型承担（`tinyint(1)` → `bool`、`longtext` → `string`），
目标表结构由服务自己的 `registry.Migrate` 建出来 —— 所以落到 PG 的 schema 与应用程序期望的**逐列一致**，而不是另抄一份 DDL。

读写两半分别切一次方言（`pkg/dbschema` 的全局），两遍式执行：读侧看到 MySQL 名、写侧看到 PG 名。
两个 `*gorm.DB` 各自持有 schema 解析缓存，不会串味。

## 用法

```bash
go run ./hack/onexai-mysql2pg \
  --src-dsn 'user:pass@tcp(host:3306)/zagent?charset=utf8mb4&parseTime=true&loc=Asia%2FShanghai' \
  --dst-dsn 'host=pg-host port=5432 user=postgres password=<pw> dbname=zagent sslmode=disable'
```

| flag | 说明 |
|---|---|
| `--src-dsn` | 源 MySQL DSN。**必须带 `parseTime=true` 与 `loc=Asia%2FShanghai`**，否则 `datetime` 读成 UTC，`timestamptz` 会整体偏移 8 小时 |
| `--dst-dsn` | 目标 PostgreSQL DSN（libpq keyword/value 形式） |
| `--batch` | 每条 INSERT 的行数，默认 200 |
| `--dry-run` | 只读不写（仍会建出空的目标 schema，用于先验证读侧与建表） |
| `--timeout` | 总超时，默认 10m |

## 语义

- **幂等**：写入按主键做 `ON CONFLICT DO UPDATE`，可重复执行。停服前跑过一次、停服后再跑一次即可补齐窗口期内的增量。
- **不触发 GORM hook**（`SkipHooks`）：`SessionM.AfterCreate` 会重写 `ChatID`，会把源行的 ID 覆盖掉。
- **序列重置**：显式带 ID 插入 `bigserial` 不会推进序列，导入后逐个 `setval(pg_get_serial_sequence(...), MAX(id))`；不重置的话应用第一次自增插入就主键冲突。
- **不搬的东西**：`chat.message_record` 上遗留的两组重名索引（`idx_message_records_*`）不会重建 —— 它们是改表名留下的历史噪音，模型里没有。
- **时区**：全程按 `Asia/Shanghai` 对齐；MySQL `DATETIME` 无时区，PG 侧是 `timestamptz`，靠两端一致的 `TimeZone` 保证瞬时不变。

## 验证

```bash
# 逐表行数
mysql -N -e "select '<t>', count(*) from zagent.<t>" | diff - <(psql "$PG" -Atc "select '<t>', count(*) from <schema>.<t>")

# 抽样哈希（含时间列，能抓到精度/时区错误）
mysql -N -e "select md5(group_concat(concat_ws('|',id,msg_id,send_time) order by id)) from zagent.onexai_chat_message_record"
psql "$PG" -Atc "select md5(string_agg(concat_ws('|',id,msg_id,send_time), ',' order by id)) from chat.message_record"

# AutoMigrate 幂等：起一次服务前后各 dump 一次 schema，diff 必须为空
pg_dump --schema-only "$PG" > /tmp/a.sql; <restart services>; pg_dump --schema-only "$PG" > /tmp/b.sql
diff <(grep -v '^\\' /tmp/a.sql) <(grep -v '^\\' /tmp/b.sql)
```

## 回滚

源 MySQL 全程只读零改动。把 `coredb.dialect` 改回 `mysql`、`addr` 指回源库即可，
表名会自动回到 `onexai_*` 前缀（见 `pkg/dbschema`）。PG 侧重来只需重建库后重跑本工具。
