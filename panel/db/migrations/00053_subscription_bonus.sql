-- +goose Up
-- subscription：去掉流量包，送的流量改为「当期有效」（用户 2026-10-16 定，见 .claude/rules/redesign-2026-10-16.md「套餐」）。
-- 属 subscription 模块。
--   1. 删 traffic_pack_grants（流量包余额，含「未分配」）与 traffic_pack_transfers（改挂流水）。
--   2. quota_balances 加 bonus_value：本期额外送的量（礼品卡、后台「加流量」、盲盒奖品）。
--      剩余 remaining 改为 limit_value + bonus_value - consumed（limit_value 空 = 不限，remaining 仍为空）。
--      周期滚动时由 Go 把 bonus_value 清零；一次性套餐用 total 周期，不滚动，送的量一直留在总量里。
--      一次性套餐「叠加」买的量加在 limit_value 上，不进 bonus_value。
-- 还没有生产部署（用户 10-09 确认），流量包余额直接丢弃，不折算进配额。
--
-- 锁与耗时（一个事务，锁持到提交）：两张流量包表直接 DROP；quota_balances 加列（带常量缺省值，只改目录）后
--   改生成列表达式（SET EXPRESSION）要重写整表、重建它的索引，耗时与订阅数成正比，期间挡住节点流量上报的写入。
--   部署顺序：先停 serve / worker，再 migrate，再起新进程。
-- 预检（可选，看会丢什么）：SELECT count(*), sum(granted_bytes - consumed_bytes) FROM traffic_pack_grants;
--   SELECT count(*) FROM quota_balances;
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在上一版，排查后重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE traffic_pack_transfers;
DROP TABLE traffic_pack_grants;

ALTER TABLE quota_balances
  ADD COLUMN bonus_value bigint NOT NULL DEFAULT 0 CHECK (bonus_value >= 0);
ALTER TABLE quota_balances
  ALTER COLUMN remaining SET EXPRESSION AS (limit_value + bonus_value - consumed);

-- +goose Down
-- 退回上一版：送的量并进 limit_value（有上限的行；不限的行本来就不受影响），再删 bonus_value、还原 remaining。
-- 两张流量包表按 00011 原文重建（空表：流量包余额无从恢复）。退回后都放得下，不需要数据守卫。
-- 锁与耗时同 Up：改生成列表达式要重写整表。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE quota_balances SET limit_value = limit_value + bonus_value
 WHERE bonus_value <> 0 AND limit_value IS NOT NULL;
ALTER TABLE quota_balances
  ALTER COLUMN remaining SET EXPRESSION AS (limit_value - consumed);
ALTER TABLE quota_balances DROP COLUMN bonus_value;

CREATE TABLE traffic_pack_grants (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  subscription_id uuid,
  source          text NOT NULL CHECK (source IN ('order', 'gift_card', 'admin')),
  source_id       uuid NOT NULL,
  granted_bytes   bigint NOT NULL CHECK (granted_bytes > 0),
  consumed_bytes  bigint NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0 AND consumed_bytes <= granted_bytes),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_pack_grants_source_key UNIQUE (source, source_id),
  CONSTRAINT traffic_pack_grants_sub_user_fk
    FOREIGN KEY (subscription_id, user_id) REFERENCES subscriptions (id, user_id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_grants_id_user_key UNIQUE (id, user_id)
);
CREATE INDEX traffic_pack_grants_open_sub_idx ON traffic_pack_grants (subscription_id, created_at, id)
  WHERE consumed_bytes < granted_bytes AND subscription_id IS NOT NULL;
CREATE INDEX traffic_pack_grants_open_user_idx ON traffic_pack_grants (user_id, created_at, id)
  WHERE consumed_bytes < granted_bytes;
CREATE INDEX traffic_pack_grants_user_idx ON traffic_pack_grants (user_id, created_at DESC, id DESC);
CREATE TRIGGER traffic_pack_grants_deny_delete
  BEFORE DELETE ON traffic_pack_grants FOR EACH ROW EXECUTE FUNCTION deny_delete();
CREATE TRIGGER traffic_pack_grants_deny_truncate
  BEFORE TRUNCATE ON traffic_pack_grants FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

CREATE TABLE traffic_pack_transfers (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  grant_id             uuid NOT NULL,
  user_id              uuid NOT NULL,
  from_subscription_id uuid,
  to_subscription_id   uuid NOT NULL,
  remaining_bytes      bigint NOT NULL CHECK (remaining_bytes >= 0),
  actor_kind           text NOT NULL CHECK (actor_kind IN ('user', 'admin', 'system')),
  actor_id             uuid REFERENCES users(id) ON DELETE RESTRICT,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_pack_transfers_moves CHECK (from_subscription_id IS DISTINCT FROM to_subscription_id),
  CONSTRAINT traffic_pack_transfers_actor_shape CHECK ((actor_kind IN ('user', 'admin')) = (actor_id IS NOT NULL)),
  CONSTRAINT traffic_pack_transfers_grant_fk
    FOREIGN KEY (grant_id, user_id) REFERENCES traffic_pack_grants (id, user_id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_transfers_from_fk
    FOREIGN KEY (from_subscription_id, user_id) REFERENCES subscriptions (id, user_id) ON DELETE RESTRICT,
  CONSTRAINT traffic_pack_transfers_to_fk
    FOREIGN KEY (to_subscription_id, user_id) REFERENCES subscriptions (id, user_id) ON DELETE RESTRICT
);
CREATE INDEX traffic_pack_transfers_grant_idx ON traffic_pack_transfers (grant_id, created_at);
CREATE INDEX traffic_pack_transfers_to_idx ON traffic_pack_transfers (to_subscription_id);
CREATE INDEX traffic_pack_transfers_from_idx ON traffic_pack_transfers (from_subscription_id)
  WHERE from_subscription_id IS NOT NULL;
CREATE TRIGGER traffic_pack_transfers_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON traffic_pack_transfers
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();
