-- 旧格式加密备份（2026-10 之前导出时带 --no-owner --no-acl，归档里没有 GRANT）恢复之后的权限修复。
-- 照这样的备份恢复，00038、00039 的两个 SECURITY DEFINER 函数可能归恢复者（超级用户）所有、以超级用户身份
-- 执行，函数执行权回到 PostgreSQL 缺省（PUBLIC 可执行），迁移给专用角色 aegis_idempotency_owner 的授权也没了。
-- 这里把这一小撮补回到迁移建出来的样子：函数属主、函数执行权、专用角色的授权。运行角色 aegis_app 的其余权限
-- 另由 bootstrap.sh（configure-app-role.sql）补；它会核对这两个函数的属主，所以这一步要在 bootstrap 之前。
--
-- 写死在这里而不是从迁移里抽：旧格式备份是有限的历史存量，之后的备份都带属主与权限。每一步都先看对象在不在
-- （旧备份的迁移版本可能早于 00038）。授权语句逐字照 00038、00039 的 Up 段；pg-layout_mock_test.sh 拿迁移原文
-- 比对授给专用角色的 GRANT 集合，并核对函数的执行权只授给 aegis_app。
--
-- 用法：restore-postgres.sh 认出旧格式备份时自动执行；用更早的恢复脚本恢复过的库，以超级用户手工执行：
--   cd <安装目录>/deploy && ./psql.sh -d <库名> < legacy-privilege-repair.sql
\set ON_ERROR_STOP on
DO $repair$
DECLARE
  v_bind regprocedure := pg_catalog.to_regprocedure('app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)');
  v_done regprocedure := pg_catalog.to_regprocedure('app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)');
BEGIN
  IF pg_catalog.to_regrole('aegis_idempotency_owner') IS NULL THEN
    RETURN;
  END IF;
  -- 00038：绑定函数，以及它要用的模式、辅助函数、列与水位表
  IF v_bind IS NOT NULL THEN
    EXECUTE pg_catalog.format('ALTER FUNCTION %s OWNER TO aegis_idempotency_owner', v_bind);
    EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %s FROM PUBLIC, aegis_app', v_bind);
    EXECUTE pg_catalog.format('GRANT EXECUTE ON FUNCTION %s TO aegis_app', v_bind);
    GRANT USAGE ON SCHEMA public,app TO aegis_idempotency_owner;
    GRANT EXECUTE ON FUNCTION app.current_tenant_id() TO aegis_idempotency_owner;
    GRANT EXECUTE ON FUNCTION app.current_actor_id() TO aegis_idempotency_owner;
    GRANT EXECUTE ON FUNCTION app.idempotency_actor_scope(text,uuid)
      TO aegis_idempotency_owner;
    GRANT EXECUTE ON FUNCTION app.idempotency_scope_matches_actor(text,uuid)
      TO aegis_idempotency_owner;
    GRANT SELECT (
      id,tenant_id,actor_id,scope,idempotency_key,request_hash,status,
      claim_generation,locked_until,resource_type,resource_id
    ) ON public.idempotency_keys TO aegis_idempotency_owner;
    GRANT UPDATE (resource_type,resource_id)
      ON public.idempotency_keys TO aegis_idempotency_owner;
    IF pg_catalog.to_regclass('app.idempotency_resource_binding_00038_usage') IS NOT NULL THEN
      GRANT SELECT (singleton,used,first_bound_at), UPDATE (used,first_bound_at)
        ON app.idempotency_resource_binding_00038_usage TO aegis_idempotency_owner;
    END IF;
  END IF;
  -- 00039：完成函数与它要用的列、水位表
  IF v_done IS NOT NULL THEN
    EXECUTE pg_catalog.format('ALTER FUNCTION %s OWNER TO aegis_idempotency_owner', v_done);
    EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %s FROM PUBLIC, aegis_app', v_done);
    EXECUTE pg_catalog.format('GRANT EXECUTE ON FUNCTION %s TO aegis_app', v_done);
    GRANT UPDATE (
      status,response_code,response_format,response_payload,response_content_type,
      response_location,response_etag,response_cache_control,
      response_content_language,completed_at,locked_until
    ) ON public.idempotency_keys TO aegis_idempotency_owner;
    GRANT SELECT (
      response_code,response_body,response_format,response_payload,
      response_content_type,response_location,response_etag,response_cache_control,
      response_content_language,completed_at
    ) ON public.idempotency_keys TO aegis_idempotency_owner;
    IF pg_catalog.to_regclass('app.bound_idempotency_success_00039_usage') IS NOT NULL THEN
      GRANT SELECT (singleton,used,first_completed_at),
            UPDATE (used,first_completed_at)
        ON app.bound_idempotency_success_00039_usage TO aegis_idempotency_owner;
    END IF;
  END IF;
END
$repair$;
