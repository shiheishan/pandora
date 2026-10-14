-- +goose Up
-- 安装令牌（bootstrap_tokens）加保留期：没用过、过期或作废满 30 天的令牌允许删除（主 Agent 10-14 定）。属 node 模块。
--
-- 原先 bootstrap_tokens_guard（00020）对 DELETE 一律报错，表只增不减。现在 DELETE 只放行
-- 「used_at IS NULL 且（expires_at 或 voided_at 早于 now() - 30 天）」的行：
--   - 用过的令牌跟身份走，永远不删（server_identities.enroll_token_id 以 ON DELETE RESTRICT 引用它，
--     崩溃重试按它找回同一张证书）；
--   - 没过期、没作废满 30 天的未用令牌照旧不许删。
-- UPDATE 分支逐字不变。删除由 node 的保留期任务分批做（repo/bootstrap_retention.go），
-- 任务按 Go 侧时钟算截止时刻，这里按库时钟兜底：两边都满 30 天才删得掉。
--
-- CREATE OR REPLACE 已有的函数：在 00020 版上改，其余逐字不变；Down 逐字还原 00020 版。
--
-- 锁与耗时：只换函数体（pg_proc 一行），不碰 bootstrap_tokens 表本身，不拿表锁，不扫行，瞬间完成；
--   触发器定义（bootstrap_tokens_guard，BEFORE UPDATE OR DELETE FOR EACH ROW）不变，新函数体对之后的语句生效。
-- 预检：无（不改数据、不加约束）。
-- 失败回滚：整个迁移在一个事务里，失败即回到 00020 的函数体，重跑即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bootstrap_tokens_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    -- （00037）没用过、过期或作废满 30 天的令牌放行；其余照旧拒绝
    IF OLD.used_at IS NULL
       AND (OLD.expires_at < now() - interval '30 days'
            OR OLD.voided_at < now() - interval '30 days') THEN
      RETURN OLD;
    END IF;
    RAISE EXCEPTION 'bootstrap_tokens 只允许删除过期或作废满 30 天的未用令牌' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.server_id, NEW.token_sha256, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.server_id, OLD.token_sha256, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'bootstrap_tokens 的认证材料不可改' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.issued_by IS DISTINCT FROM OLD.issued_by
     AND NOT (OLD.issued_by IS NOT NULL AND NEW.issued_by IS NULL) THEN
    RAISE EXCEPTION 'bootstrap_tokens.issued_by 只允许不变或从非空变成 NULL' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.used_at, NEW.used_request_id) IS DISTINCT FROM (OLD.used_at, OLD.used_request_id) THEN
    IF OLD.used_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.used_at 只能从未用写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  IF NEW.voided_at IS DISTINCT FROM OLD.voided_at THEN
    IF OLD.voided_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.voided_at 只能写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- 还原成 00020 的原文（DELETE 一律拒绝）。已经删掉的令牌回不来，也不需要：它们都是没用过的过期令牌。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bootstrap_tokens_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'bootstrap_tokens 不允许删除' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.server_id, NEW.token_sha256, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.server_id, OLD.token_sha256, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'bootstrap_tokens 的认证材料不可改' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.issued_by IS DISTINCT FROM OLD.issued_by
     AND NOT (OLD.issued_by IS NOT NULL AND NEW.issued_by IS NULL) THEN
    RAISE EXCEPTION 'bootstrap_tokens.issued_by 只允许不变或从非空变成 NULL' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.used_at, NEW.used_request_id) IS DISTINCT FROM (OLD.used_at, OLD.used_request_id) THEN
    IF OLD.used_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.used_at 只能从未用写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  IF NEW.voided_at IS DISTINCT FROM OLD.voided_at THEN
    IF OLD.voided_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.voided_at 只能写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
