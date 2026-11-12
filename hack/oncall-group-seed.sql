-- chat.oncall_group: the on-call group (值班组) metadata the /oncall and /duty
-- commands resolve module names against.
--
-- Why this file exists. The duty store is built from two sources: group metadata
-- from this table, and the on-call roster from Regen. When the table is empty the
-- source falls back to listing the Regen schedules instead
-- (internal/chat/duty/module_source.go), so a deployment whose table was never
-- populated *looks* fine — until Regen itself is unreachable, at which point
-- there is no fallback left and every module name answers 不存在. Seeding the
-- table decouples the two: the group list survives a Regen outage, and only the
-- roster (谁在值班) depends on Regen.
--
-- Dialect: PostgreSQL. The live deployment stores this as chat.oncall_group
-- (dbschema splits per-service schemas on PG). On MySQL the same table is
-- onexai_chat_oncall_group and this file would need backtick quoting — do not
-- run it there as-is.
--
-- Idempotent: `name` carries a unique index (idx_chat_oncall_group_name), so
-- re-running only refreshes the presentation columns and the schedule_id.
--
-- Rollback:
--   DELETE FROM chat.oncall_group
--    WHERE name IN ('go', 'onex', 'kubernetes', 'obs', 'aiinfra');
--
-- The schedule_id values below are the Regen schedule UUIDs on the live Regen
-- instance. They must be exact: a module whose schedule_id is set but unknown is
-- *not* resolved by name (internal/chat/duty/module_source.go, resolveScheduleID)
-- and silently loses its on-call roster. Verify with:
--   SELECT name, schedule_id FROM chat.oncall_group ORDER BY "order";
-- and, on the Regen side, GET /api/v1/schedules.

INSERT INTO chat.oncall_group
  (name, display_name, status, short_desc, long_desc, agents, schedule_id,
   allocation_strategy, owner, is_split_by_biz_line, generate_weekly_rpt,
   source, welcome_message, namespace, "order", created_at, updated_at)
VALUES
  ('go',         'Go 开发',    'enabled', 'Go 语言服务、框架与工程实践问题',       '', '[]',
   '00000000-0000-0000-0000-000000000001', 'primary_backup', 'ops', 'false', 'false', 'manual', '', '',
   1, (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),
  ('onex',       'OneX 平台',  'enabled', 'OneX 智能运维平台自身的问题',           '', '[]',
   '00000000-0000-0000-0000-000000000002', 'primary_backup', 'ops', 'false', 'false', 'manual', '', '',
   2, (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),
  ('kubernetes', 'Kubernetes', 'enabled', 'K8s 集群、Pod 与容器编排问题',          '', '[]',
   '00000000-0000-0000-0000-000000000003', 'primary_backup', 'ops', 'false', 'false', 'manual', '', '',
   3, (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),
  ('obs',        '可观测性',   'enabled', '监控、日志、告警问题',                  '', '[]',
   '00000000-0000-0000-0000-000000000004', 'primary_backup', 'ops', 'false', 'false', 'manual', '', '',
   4, (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),
  ('aiinfra',    'AI 基础设施','enabled', 'AI 训练推理、GPU 与模型基础设施问题',   '', '[]',
   '00000000-0000-0000-0000-000000000005', 'primary_backup', 'ops', 'false', 'false', 'manual', '', '',
   5, (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint)
ON CONFLICT (name) DO UPDATE SET
  display_name        = EXCLUDED.display_name,
  status              = EXCLUDED.status,
  short_desc          = EXCLUDED.short_desc,
  long_desc           = EXCLUDED.long_desc,
  schedule_id         = EXCLUDED.schedule_id,
  allocation_strategy = EXCLUDED.allocation_strategy,
  owner               = EXCLUDED.owner,
  "order"             = EXCLUDED."order",
  updated_at          = EXCLUDED.updated_at;
