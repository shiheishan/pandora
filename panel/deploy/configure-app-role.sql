-- 迁移跑完之后，以 PostgreSQL 超级用户在库 pandora 里执行，收敛运行角色 pandora_app 的权限。
-- 可重复执行，结果相同。安装器在 pandora migrate up 之后经 bootstrap.sh grants 调用。
--
-- 不使用 ALTER DEFAULT PRIVILEGES：新表、新序列的权限依赖每次迁移之后重跑本脚本
--（安装与升级都如此）。未授予前运行角色拿不到权限，属于失败即关。
\set ON_ERROR_STOP on

BEGIN;

-- 1. 守卫：超级用户、角色与库属主
DO $$
BEGIN
  IF NOT (SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user) THEN
    RAISE EXCEPTION 'configure-app-role.sql 必须以超级用户执行';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pandora_owner') THEN
    RAISE EXCEPTION '缺少角色 pandora_owner，先跑 bootstrap.sh roles';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pandora_app') THEN
    RAISE EXCEPTION '缺少角色 pandora_app，先跑 bootstrap.sh roles';
  END IF;
  IF EXISTS (
    SELECT 1
      FROM pg_catalog.pg_auth_members AS m
      JOIN pg_catalog.pg_roles AS r ON r.oid = m.roleid OR r.oid = m.member
     WHERE r.rolname = 'pandora_app'
  ) THEN
    RAISE EXCEPTION 'pandora_app 不许有任何角色成员关系，请先显式 REVOKE';
  END IF;
  IF pg_catalog.pg_get_userbyid((SELECT datdba FROM pg_catalog.pg_database WHERE datname = current_database()))
     <> 'pandora_owner' THEN
    RAISE EXCEPTION '当前库的属主必须是 pandora_owner';
  END IF;
END $$;

-- 2. 角色属性收敛（与 bootstrap.sh roles 一致，防止手改）
ALTER ROLE pandora_app NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE pandora_owner NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

-- 3. 运行角色在本库内的查询守卫（按库设置，避免同集群上别的库被波及）
DO $$
DECLARE
  v_database name := pg_catalog.current_database();
BEGIN
  EXECUTE pg_catalog.format(
    'ALTER ROLE pandora_app IN DATABASE %I SET search_path TO pg_catalog, public, pg_temp',
    v_database
  );
  -- jit = off：PG18 默认 jit=on，带逐行子计划的查询编译时间常超过执行时间；
  -- postgresql-pandora.conf 也在实例级关 JIT，这里再钉住运行角色，防止托管库改回 on。
  EXECUTE pg_catalog.format(
    'ALTER ROLE pandora_app IN DATABASE %I SET jit = off', v_database
  );
  -- statement_timeout = 15s：单条语句超过 15 秒请求已无望返回；低于 nginx 常见 20s 读超时，
  -- 也低于 pandora serve 的 25 秒请求超时，便于网关干净取消而不是空烧 CPU。锁等待也计入。
  -- 迁移、备份、运维 psql 用超级用户，不受此限制。
  EXECUTE pg_catalog.format(
    'ALTER ROLE pandora_app IN DATABASE %I SET statement_timeout = %L', v_database, '15s'
  );
END $$;

-- 4. 库与 schema：运行角色只能 CONNECT + USAGE，不能 CREATE / TEMPORARY
DO $$
DECLARE
  v_database name := pg_catalog.current_database();
BEGIN
  EXECUTE pg_catalog.format('REVOKE ALL ON DATABASE %I FROM PUBLIC', v_database);
  EXECUTE pg_catalog.format('GRANT CONNECT ON DATABASE %I TO pandora_app', v_database);
  REVOKE ALL ON SCHEMA public FROM PUBLIC;
  REVOKE ALL ON SCHEMA public FROM pandora_app;
  GRANT USAGE ON SCHEMA public TO pandora_app;
END $$;

-- 5. 运行角色不得拥有任何对象
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT format('%I.%I', n.nspname, c.relname) AS obj
      FROM pg_catalog.pg_class c
      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_catalog.pg_roles o ON o.oid = c.relowner
     WHERE o.rolname = 'pandora_app'
       AND n.nspname NOT IN ('pg_catalog', 'information_schema')
       AND c.relkind IN ('r', 'i', 'S', 'v', 'm', 'c', 'f', 'p')
  LOOP
    RAISE EXCEPTION 'pandora_app 不得为对象属主：%', r.obj;
  END LOOP;

  FOR r IN
    SELECT format('%I.%I(%s)', n.nspname, p.proname,
                   pg_catalog.pg_get_function_identity_arguments(p.oid)) AS obj
      FROM pg_catalog.pg_proc p
      JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
      JOIN pg_catalog.pg_roles o ON o.oid = p.proowner
     WHERE o.rolname = 'pandora_app'
       AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  LOOP
    RAISE EXCEPTION 'pandora_app 不得为对象属主：%', r.obj;
  END LOOP;

  FOR r IN
    SELECT format('schema %I', n.nspname) AS obj
      FROM pg_catalog.pg_namespace n
      JOIN pg_catalog.pg_roles o ON o.oid = n.nspowner
     WHERE o.rolname = 'pandora_app'
       AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  LOOP
    RAISE EXCEPTION 'pandora_app 不得为对象属主：%', r.obj;
  END LOOP;

  FOR r IN
    SELECT format('%I.%I', n.nspname, t.typname) AS obj
      FROM pg_catalog.pg_type t
      JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
      JOIN pg_catalog.pg_roles o ON o.oid = t.typowner
     WHERE o.rolname = 'pandora_app'
       AND n.nspname NOT IN ('pg_catalog', 'information_schema')
       AND t.typtype IN ('b', 'c', 'd', 'e', 'r')
  LOOP
    RAISE EXCEPTION 'pandora_app 不得为对象属主：%', r.obj;
  END LOOP;
END $$;

-- 6. 表：先收回再授予 DML；迁移版本表不给运行角色
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM pandora_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO pandora_app;
DO $$
BEGIN
  IF to_regclass('public.goose_db_version') IS NOT NULL THEN
    REVOKE ALL ON public.goose_db_version FROM pandora_app;
  END IF;
END $$;

-- 7. 只追加表：按 deny_mutation / deny_delete 触发器自动收回 UPDATE / DELETE
DO $$
DECLARE
  r record;
  v_schema text;
  v_table text;
BEGIN
  FOR r IN
    SELECT n.nspname AS schema_name,
           c.relname AS table_name,
           p.proname AS func_name,
           t.tgtype
      FROM pg_catalog.pg_trigger t
      JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
      JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND NOT t.tgisinternal
       AND p.proname IN ('deny_mutation', 'deny_delete')
  LOOP
    v_schema := r.schema_name;
    v_table := r.table_name;
    IF r.func_name IN ('deny_mutation', 'deny_delete') AND (r.tgtype & 8) <> 0 THEN
      EXECUTE format('REVOKE DELETE ON %I.%I FROM pandora_app', v_schema, v_table);
    END IF;
    IF r.func_name = 'deny_mutation' AND (r.tgtype & 16) <> 0 THEN
      EXECUTE format('REVOKE UPDATE ON %I.%I FROM pandora_app', v_schema, v_table);
    END IF;
  END LOOP;
END $$;

-- 8. 序列
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM pandora_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO pandora_app;

-- 9. 保留期清理函数（SECURITY DEFINER）与未登记 DEFiner 失败即关
DO $$
DECLARE
  v_sig text;
  v_required constant text[] := ARRAY[
    'purge_audit_events(timestamptz, integer)',
    'purge_unverified_payment_events(timestamptz, integer)',
    'purge_subscription_fetch_log(timestamptz, integer)',
    'purge_node_traffic_reports(timestamptz, integer)',
    'purge_node_metrics(timestamptz, integer)'
  ];
  v_fn record;
BEGIN
  FOREACH v_sig IN ARRAY v_required LOOP
    IF to_regprocedure('public.' || v_sig) IS NULL THEN
      RAISE EXCEPTION '缺少保留期清理函数 public.%，请先 pandora migrate up', v_sig;
    END IF;
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.%s TO pandora_app', v_sig);
  END LOOP;

  FOR v_fn IN
    SELECT n.nspname, p.proname,
           pg_catalog.pg_get_function_identity_arguments(p.oid) AS args
      FROM pg_catalog.pg_proc p
      JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.prosecdef
  LOOP
    IF NOT (
      v_fn.proname = 'purge_audit_events'
      OR v_fn.proname = 'purge_unverified_payment_events'
      OR v_fn.proname = 'purge_subscription_fetch_log'
      OR v_fn.proname = 'purge_node_traffic_reports'
      OR v_fn.proname = 'purge_node_metrics'
    ) THEN
      RAISE EXCEPTION '发现没登记的 SECURITY DEFINER 函数 %.%(%)，在 configure-app-role.sql 里核对后登记',
        v_fn.nspname, v_fn.proname, v_fn.args;
    END IF;
  END LOOP;
END $$;

COMMIT;
