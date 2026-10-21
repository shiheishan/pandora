-- +goose Up
-- 删 users.risk_level（用户 2026-10-21 同意按后台与用户端对齐核对的推荐清理）。属 identity 模块（docs/architecture.md 4.1）。
--
-- 这一列从 00005 建表起就没有任何代码写（永远是缺省 normal），后台早已不用它判断风险：风险等级由用户画像按共用 IP
-- 推出（identity service 的 clusterRisk），前端也不再读。Go model、DTO、前端 schema 与假后端在同一提交里删掉。
-- 删列一步做、不分「先停用再删」两步：安装器升级时先停服务再迁移（install.sh），没有新旧二进制同时连库的窗口；
-- 现在也没有生产部署（10-09 确认）。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：users 拿 ACCESS EXCLUSIVE；DROP COLUMN 只改系统目录、不重写表，
--   与行数无关，毫秒级（列上的 CHECK users_risk_level_check 随列一起删）。期间挡住所有读写 users 的请求（登录、下单等）。
--   预检：SELECT count(*) FROM users WHERE risk_level <> 'normal';  应为 0（没有写入方）；非零说明有人手工改过库，
--     这些值删列后就没有了，先确认没人要。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE users DROP COLUMN risk_level;

-- +goose Down
-- 按 00005 原样加回（类型、NOT NULL、缺省 normal、四档 CHECK，约束名与 PG 自动起的名字一致），列排到表尾。
-- 加带常量缺省的 NOT NULL 列不重写表，但校验 CHECK 要扫一遍 users（与行数成正比，ACCESS EXCLUSIVE 持到提交）。
-- 原来的值不可还原：一律回到缺省 normal（删列前本来就都是 normal）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE users ADD COLUMN risk_level text NOT NULL DEFAULT 'normal'
  CONSTRAINT users_risk_level_check CHECK (risk_level IN ('trusted', 'normal', 'elevated', 'high'));
