-- <一句话：给 app.<函数> 加什么>（<谁定的>；<任务路名>）。
--
-- CREATE OR REPLACE 别人的函数：在「当前生效那一版」上改，其余逐字不变。
--   当前生效版：find-def.py app.<函数> --body（写的是 <NNNNN> 版，Down 逐字还原它）。
--   几路并行都改它时，后合的那份要包含先合那份的改动，Down 还原到先合那份（不是更早的版本）。
-- 有源码契约按「最后一个定义它的迁移」核对的（如 app.seed_tenant_defaults 的模板数），
-- 同一提交里一起改契约；注释里不要写 `FUNCTION app.<函数>(` 字样，契约会把这个文件当成定义。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.<函数>(<参数与类型，与上一版一致>) RETURNS <与上一版一致>
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  -- <上一版函数体逐字照抄，只在要改的地方改，并在改动处注明「（NNNNN）」>
  RETURN;
END;
$$;
-- +goose StatementEnd

-- 改了签名就不是 REPLACE 而是新函数：先 DROP 旧签名，授权与 REVOKE 也要重做
-- （configure-app-role.sql 里按签名 REVOKE 的函数一并核对）。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 新口径已经写下的数据在旧函数下会被拒或被误读时，先守卫（照 00134 的 Down）：
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM <表> WHERE <只有新口径才会写出的行>) THEN
    RAISE EXCEPTION '<NNNNN> Down refused: <哪些行存在、为什么不能退>';
  END IF;
END $$;
-- +goose StatementEnd

-- 还原成 <上一版号> 的原文（find-def.py app.<函数> --before <本迁移号> --body 的输出，逐字粘贴，
-- 连 END 后有没有分号、空行、缩进都一样：往返门禁比的是 pg_dump 里的函数体文本）。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.<函数>(<参数与类型>) RETURNS <返回类型>
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  RETURN;
END;
$$;
-- +goose StatementEnd
