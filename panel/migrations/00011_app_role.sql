-- 应用数据库角色。
--
-- 为什么必须有这一步：
--   ENABLE + FORCE ROW LEVEL SECURITY 能约束表属主，但拦不住带 BYPASSRLS 属性的角色，
--   而 Docker postgres 镜像里的 POSTGRES_USER 默认就是 superuser（隐含 BYPASSRLS）。
--   若应用直接用它连库，DATA-002 与 ARC-006 就只是纸面约束 —— 跨租户查询照样返回数据。
--
--   因此：迁移与运维用 superuser，应用运行时一律用 aegis_app（NOSUPERUSER + NOBYPASSRLS）。
--
-- 密码不在此设置（不进版本库）。部署脚本会执行：
--   ALTER ROLE aegis_app LOGIN PASSWORD '<随机值>';

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    -- NOLOGIN：在部署脚本注入密码前，这个角色无法被用来连接
    CREATE ROLE aegis_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;
  ELSE
    ALTER ROLE aegis_app NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
GRANT USAGE ON SCHEMA public TO aegis_app;
GRANT USAGE ON SCHEMA app TO aegis_app;

-- 业务表：读写，但不给 TRUNCATE（TRUNCATE 绕过行级触发器，会架空 DATA-003）
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO aegis_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO aegis_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO aegis_app;

-- 后续迁移新建的对象自动继承同样的授权
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO aegis_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA app
  GRANT EXECUTE ON FUNCTIONS TO aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- 追加写表：显式收回 UPDATE/DELETE。
-- 触发器已经会抛异常，这里再加一道权限锁 —— 纵深防御，
-- 且权限错误发生在语句解析期，比触发器更早、更省资源。
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'ledger_entries', 'ledger_transactions', 'audit_events',
    'subscription_events', 'quota_adjustments', 'approval_decisions',
    'referrals', 'provisioning_steps', 'node_config_applications',
    'credential_access_log', 'system_setting_revisions'
  ] LOOP
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM aegis_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 权限字典与状态机转换表对应用只读：
-- 应用不能自己往 subscription_transitions 里插一条边来「合法化」非法跳转。
REVOKE INSERT, UPDATE, DELETE ON permissions FROM aegis_app;
REVOKE INSERT, UPDATE, DELETE ON subscription_transitions FROM aegis_app;
REVOKE INSERT, UPDATE, DELETE ON node_transitions FROM aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
-- goose 版本表对应用不可见，避免应用误改迁移状态。
-- 条件执行：check-migrations.sh 的临时库不经 goose，该表并不存在。
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tablename = 'goose_db_version') THEN
    EXECUTE 'REVOKE ALL ON goose_db_version FROM aegis_app';
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aegis_app') THEN
    EXECUTE 'REVOKE ALL ON ALL TABLES IN SCHEMA public FROM aegis_app';
    EXECUTE 'REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM aegis_app';
    EXECUTE 'REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM aegis_app';
    EXECUTE 'REVOKE USAGE ON SCHEMA public FROM aegis_app';
    EXECUTE 'REVOKE USAGE ON SCHEMA app FROM aegis_app';
    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM aegis_app';
    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM aegis_app';
    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON FUNCTIONS FROM aegis_app';
    EXECUTE 'DROP ROLE aegis_app';
  END IF;
END $$;
-- +goose StatementEnd
