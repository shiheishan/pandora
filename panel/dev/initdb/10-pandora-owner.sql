-- 开发数据基座的首次初始化：空数据卷时镜像入口以超级用户 postgres 跑一次（之后不再跑）。
-- 与 deploy/bootstrap.sh roles 同一个做法：库属主是普通登录角色 pandora_owner，库用 template0、UTF8，
-- 迁移要临时关触发器，给 pandora_owner 设 session_replication_role 的权限。
-- 开发模式不检查运行角色，本机以 pandora_owner 跑，不建 pandora_app。
-- 口令只经容器环境变量进来，不出现在 SQL 文本与输出里。
\set ON_ERROR_STOP on
\getenv db PANDORA_DB
\getenv owner_password PANDORA_DB_OWNER_PASSWORD

SELECT 'CREATE ROLE pandora_owner LOGIN'
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pandora_owner') \gexec
ALTER ROLE pandora_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD :'owner_password';
SELECT pg_catalog.format('CREATE DATABASE %I OWNER pandora_owner TEMPLATE template0 ENCODING %L', :'db', 'UTF8')
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = :'db') \gexec
GRANT SET ON PARAMETER session_replication_role TO pandora_owner;
\connect :db
ALTER SCHEMA public OWNER TO pandora_owner;
