-- +goose Up
-- <一句话：给 <函数> 改什么>（<谁定的>）。属 <模块> 模块。
--
-- CREATE OR REPLACE 已有的函数：在「当前生效那一版」上改，其余逐字不变。
--   当前生效版：find-def.py <函数> --body（写的是 <NNNNN> 版，Down 逐字还原它）。
-- 改了参数或返回类型就不是 REPLACE 而是新函数：先 DROP 旧签名。
-- SECURITY DEFINER 函数（保留期清理 purge_*）的签名登记在 panel/deploy/configure-app-role.sql 第 9 段：
--   改签名、新增或删除都要同步改那里，否则安装器在收权限时失败（未登记的 DEFINER 函数一律拒绝）。
--   DEFINER 函数固定 search_path；要绕追加写守卫用函数定义上的 SET session_replication_role = replica（照 00023_node_retention.sql）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION <函数>(<参数与类型，与上一版一致>) RETURNS <与上一版一致>
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  -- <上一版函数体逐字照抄，只在要改的地方改，并在改动处注明「（NNNNN）」>
  RETURN;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 新口径已经写下的数据在旧函数下会被拒或被误读时，先守卫（去掉下面整段的行首「-- 」；有行就 RAISE，没有就照常退）：
-- -- +goose StatementBegin
-- DO $$
-- BEGIN
--   IF EXISTS (SELECT 1 FROM <表> WHERE <只有新口径才会写出的行>) THEN
--     RAISE EXCEPTION '<为什么不能退>';
--   END IF;
-- END
-- $$;
-- -- +goose StatementEnd

-- 还原成 <上一版号> 的原文（find-def.py <函数> --before <本迁移号> --body 的输出，逐字粘贴）。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION <函数>(<参数与类型>) RETURNS <返回类型>
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  RETURN;
END;
$$;
-- +goose StatementEnd
