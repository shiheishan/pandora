-- 开发数据基座的首次初始化：空数据卷时镜像入口以超级用户 postgres 跑一次（之后不再跑）。
-- 与 deploy/install.sh 建库同一个做法：库属主是普通登录角色（缺省 aegis），库用 template0、UTF8。
-- 运行角色 aegis_app 不在这里建：迁移建它，deploy/bootstrap.sh（configure-app-role.sql）给口令与 LOGIN，
-- 与生产同一条路。口令只经容器环境变量进来，不出现在 SQL 文本与输出里。
\set ON_ERROR_STOP on
\getenv db PANDORA_DB
\getenv owner PANDORA_DB_OWNER
\getenv owner_password PANDORA_DB_OWNER_PASSWORD

SELECT pg_catalog.format('CREATE ROLE %I LOGIN PASSWORD %L', :'owner', :'owner_password')
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'owner') \gexec
SELECT pg_catalog.format('CREATE DATABASE %I OWNER %I TEMPLATE template0 ENCODING %L', :'db', :'owner', 'UTF8')
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = :'db') \gexec
