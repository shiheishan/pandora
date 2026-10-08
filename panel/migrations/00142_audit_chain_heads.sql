-- 审计哈希链的每租户链头（w9audit，2026-10-08）。
--
-- 为什么要它：audit.Write 原先在同租户 advisory lock 内读审计表的最大 chain_seq
-- 作链尾。读已提交事务里每条语句一个新快照，拿锁后读到的是最新链尾；但下单、续费、
-- 换套餐跑在 SERIALIZABLE 事务里，快照在事务第一条语句就定了，早于拿锁，读到的
-- 是旧链尾，并发时写出重复的 chain_seq，撞 (tenant_id, chain_seq) 唯一约束回 500。
--
-- 现在序号从这张表取：audit.Write 用 UPDATE … RETURNING 把本租户的 last_seq 加一，
-- 同时拿到上一条的 entry_hash；写完审计行再把 last_hash 改成新行的哈希（同一事务）。
--   * 读已提交：UPDATE 等行锁，拿到后作用在最新版本上（EvalPlanQual），序号与前驱
--     都是最新的，行锁顺带串行化同租户的写；
--   * 可串行化：别人在本事务快照之后改过这一行，UPDATE 直接报 40001，
--     InTxSerializableRetry 会接住重试。
--
-- 审计链闸 audit_chain_gate：一张没有列、没有行、只用来加锁的表，只有序列化事务碰它
-- （platform/db 的 chainGateSQL / EnterChainGate）。重试事务第一条语句
-- LOCK TABLE … IN SHARE ROW EXCLUSIVE MODE（LOCK 不取快照，快照晚于拿闸），彼此排队；
-- 乐观的第一次尝试取号前以 NOWAIT 拿 ROW EXCLUSIVE，闸被占着就立刻改去排队。
-- 读已提交的审计写入不碰闸。没有写入，autovacuum 不会来抢它的锁。
-- 每次少读一次审计表；序列化事务也不再对审计表加谓词锁。
--
-- 不回填：链头只是审计表链尾的缓存。某租户还没有链头时，audit.Write 在同一条
-- INSERT … ON CONFLICT 里从审计表现算（第二版链尾，没有就接第一版链尾），口径只有
-- Go 里这一处。链头被删也只会按审计表重建，不会让链分叉。
-- 锁与耗时：只建两张空表，瞬时。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE audit_chain_heads (
  tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  -- 本租户已占用的最大 chain_seq
  last_seq   bigint NOT NULL CHECK (last_seq > 0),
  -- 序号为 last_seq 那一条的 entry_hash。只在写审计的事务内部短暂等于前驱的哈希
  -- （取号与写行之间），提交后总是链尾那一条的哈希；第一条记录的前驱可以为空
  last_hash  bytea NULL
);

-- 租户隔离：ENABLE + FORCE RLS + tenant_isolation 策略（00001 的 app.enable_tenant_rls）
SELECT app.enable_tenant_rls('audit_chain_heads');

-- 运行角色要建头（INSERT … ON CONFLICT DO UPDATE）与取号（UPDATE）
GRANT SELECT, INSERT, UPDATE ON audit_chain_heads TO aegis_app;

CREATE TABLE audit_chain_gate ();

-- 没有行，策略缺省全拒；LOCK TABLE … ROW EXCLUSIVE 及以上要求 UPDATE 权限
ALTER TABLE audit_chain_gate ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_chain_gate FORCE ROW LEVEL SECURITY;
GRANT SELECT, UPDATE ON audit_chain_gate TO aegis_app;

COMMENT ON TABLE audit_chain_gate IS
  '审计链闸：只用来加表锁，不存行。序列化事务取审计号前经它排队（platform/db 的 chainGateSQL）。';

COMMENT ON TABLE audit_chain_heads IS
  '审计哈希链每租户链头：last_seq 为已占用的最大 chain_seq，last_hash 为链尾的 entry_hash。审计表链尾的缓存，缺行时由 audit.Write 从审计表重建。';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 派生表与锁表，删掉不丢证据：回滚后的代码按审计表取链尾，两张表都不再被读
DROP TABLE IF EXISTS audit_chain_gate;
DROP TABLE IF EXISTS audit_chain_heads;
