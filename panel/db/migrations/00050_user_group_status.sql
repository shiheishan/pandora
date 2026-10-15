-- +goose Up
-- 用户组停用与「连价删组」（用户 10-14 定 C+B）：
--   1. user_groups 加 status（active / disabled）：停用的组在后台的分配下拉里不再出现、不能再加新成员，
--      已有成员不受影响（照样看到组的套餐、专属价、节点池、订阅域名）；可以再启用。
--   2. prices.user_group_id 的外键从 ON DELETE RESTRICT 改成 ON DELETE CASCADE：删组时连带删掉这个组的专属价
--      （不分在售、归档）。被订单项、订阅、礼品卡引用过的价格由它们 price_id 的 RESTRICT 外键挡住，
--      级联删除失败、删组整笔回滚，组和价格都还在（identity 回 409，提示改用停用）。
--   3. 价格冻结守卫 catalog_guard_price 的 DELETE 分支只多放行「所属组已在本事务里被删」这一种情况，
--      也就是删组的级联；组还在时直接删价格照旧拒绝。UPDATE 分支与 00008 一字不差。
--
-- 锁与耗时：
--   ADD COLUMN 带常量默认值只改目录、不重写表，要 user_groups 的 ACCESS EXCLUSIVE，拿到即完成。
--   换外键（同一条 ALTER TABLE 里先 DROP 再 ADD）：要 prices 与 user_groups 的 ACCESS EXCLUSIVE，ADD 时扫一遍 prices
--   校验现有行，耗时与价格行数成正比。价格表只有几十到几百行、没有生产部署，直接加，不拆成 NOT VALID + VALIDATE
--   （那是为了大表少持锁，这里没有意义，还要多一步）。
--   CREATE OR REPLACE FUNCTION 只改目录，触发器 prices_guard 绑定的是函数名，不用重建。
--   拿不到锁时 lock_timeout 5 秒报错。
-- 预检（可选，下面的 DO 块会再核一遍）：
--   SELECT conname FROM pg_constraint WHERE conrelid = 'prices'::regclass AND contype = 'f'
--     AND confrelid = 'user_groups'::regclass;   -- 应只有 prices_user_group_id_fkey 一条
-- 失败回滚：任一步失败（含 lock_timeout 超时、约束名对不上）整笔回滚，库保持原样，排查后重试。
-- 部署顺序：旧进程不认识 status 列（GORM 扫描时忽略多出的列，插入时由默认值补 active），先迁移再换进程即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE user_groups
  ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled'));

-- 00008 里外键是列内联写的，约束名由 PG 生成；名字对不上就停下，不猜
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'prices'::regclass
       AND conname = 'prices_user_group_id_fkey'
       AND contype = 'f'
       AND confrelid = 'user_groups'::regclass
  ) THEN
    RAISE EXCEPTION 'prices 指向 user_groups 的外键不叫 prices_user_group_id_fkey，先核对约束名再迁移';
  END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE prices
  DROP CONSTRAINT prices_user_group_id_fkey,
  ADD CONSTRAINT prices_user_group_id_fkey
    FOREIGN KEY (user_group_id) REFERENCES user_groups(id) ON DELETE CASCADE;

-- 价格：不可删（例外见下）；除状态与 row_version 外任何列都不可改；状态只许 active → archived，且 row_version 恰好加一。
-- 唯一放行的删除：所属组已不存在。prices.user_group_id 是 ON DELETE CASCADE，删组时先删组行、再级联删价格，
-- 级联执行时本事务里已看不到那个组，所以这个条件只会在删组的级联里成立（组还在时直接 DELETE 价格照旧拒绝；
-- 公开价 user_group_id 为空，永远不放行）。
-- 放行是安全的：被订单项、订阅、礼品卡引用的价格由它们 price_id 的 ON DELETE RESTRICT 外键挡住，
-- 级联删除失败、删组整笔回滚；能被删掉的只有从没被买过的组专属价，订单与订阅的价格快照不受影响。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_guard_price() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.user_group_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM user_groups WHERE id = OLD.user_group_id) THEN
      RETURN OLD;
    END IF;
    RAISE EXCEPTION '价格 % 只能归档，不能删除', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF (to_jsonb(NEW) - ARRAY['status', 'row_version']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'row_version']) THEN
    RAISE EXCEPTION '价格 % 不可修改，请归档后新建', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF NOT (OLD.status = 'active' AND NEW.status = 'archived' AND NEW.row_version = OLD.row_version + 1) THEN
    RAISE EXCEPTION '价格 % 只能从在售归档一次', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- 逆序还原：守卫恢复 00008 原样 → 外键改回 RESTRICT → 删 status 列。
-- 停用信息丢失：status 整列删掉，Down 之前停用的组回到「正常」（没有这一列就没有停用）。
-- 外键改回 RESTRICT 不需要清数据：现存价格的组都还在（CASCADE 期间组删了价格也跟着删了）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 价格：不可删；除状态与 row_version 外任何列都不可改；状态只许 active → archived，且 row_version 恰好加一。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_guard_price() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '价格 % 只能归档，不能删除', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF (to_jsonb(NEW) - ARRAY['status', 'row_version']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'row_version']) THEN
    RAISE EXCEPTION '价格 % 不可修改，请归档后新建', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF NOT (OLD.status = 'active' AND NEW.status = 'archived' AND NEW.row_version = OLD.row_version + 1) THEN
    RAISE EXCEPTION '价格 % 只能从在售归档一次', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE prices
  DROP CONSTRAINT prices_user_group_id_fkey,
  ADD CONSTRAINT prices_user_group_id_fkey
    FOREIGN KEY (user_group_id) REFERENCES user_groups(id) ON DELETE RESTRICT;

ALTER TABLE user_groups DROP COLUMN status;
