-- +goose Up
-- <一句话：这个迁移做什么>（<谁在什么时候定的，如 用户 2026-10-13 定>）。属 <模块> 模块（每张表只有一个属主模块，见 docs/architecture.md 4.1）。
--
-- <为什么要它；读写它的是哪个模块的 repo（只有属主模块的 repo 碰这张表）>。
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：<拿什么锁、哪张表、现在多大、预计多久>。
--   预检：<上线前跑的查询，如 SELECT count(*) FROM <表>；到多大时改成什么做法（NOT VALID + VALIDATE、CONCURRENTLY）>。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，<换低峰重试 / 别的处置>。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 约束进库：NOT NULL、CHECK、UNIQUE、外键能加就加。钱用 numeric(20,2)（Go 侧 shopspring/decimal），不用浮点。
CREATE TABLE <表> (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status      text NOT NULL CHECK (status IN ('<值1>', '<值2>')),
  amount      numeric(20,2) NOT NULL CHECK (amount >= 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- <索引用途：哪条查询走它>
CREATE INDEX <表>_user_idx ON <表> (user_id, created_at DESC);

-- 只追加的流水表挂通用守卫；configure-app-role.sql 按这个触发器自动收回运行角色的 UPDATE / DELETE：
-- CREATE TRIGGER <表>_append_only
--   BEFORE UPDATE OR DELETE OR TRUNCATE ON <表>
--   FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();
-- 允许改状态、不许删的表改挂 deny_delete()（BEFORE DELETE）。

-- 授权不写在迁移里：安装与升级在 migrate up 之后跑 configure-app-role.sql，给 pandora_app 全部表的 DML 再按守卫收回。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 逆操作把结构退回上一版的逐项原样（列、约束名与内容、索引、触发器、函数体）。
-- 有退回后放不下的数据时先守卫（去掉下面整段的行首「-- 」）：有行就 RAISE、没有就照常退（照 00016_commission_review.sql 的 Down）。
-- 不要无条件 RAISE：CI 会在测试库上 down 到底再 up。
-- -- +goose StatementBegin
-- DO $$
-- BEGIN
--   IF EXISTS (SELECT 1 FROM <表> WHERE <只有新结构才会写出的行>) THEN
--     RAISE EXCEPTION '<为什么不能退，先怎么人工处理>';
--   END IF;
-- END
-- $$;
-- -- +goose StatementEnd
DROP TABLE IF EXISTS <表>;
