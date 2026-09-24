-- 站点时区（契约 R49、R50，2026-09-24 用户定案）。
--
-- tenants.timezone 与 users.timezone 建表时默认都是 'UTC'，此前 Go 里没有任何
-- 写入口，所以存量的 'UTC' 全是默认值，不是谁选的。门户按日用量（00072）与
-- 后台收入趋势都按它切日，国内站点整整差 8 小时。
--
-- 站点时区改默认 Asia/Shanghai，存量 'UTC' 一并改掉；此后由后台
-- POST v1/settings/site 修改。users.timezone 不动：用户为 'UTC' 视同未设、跟随
-- 站点时区，这条口径在 nodefabric.UsageLocation 里。

-- +goose Up
ALTER TABLE tenants ALTER COLUMN timezone SET DEFAULT 'Asia/Shanghai';
UPDATE tenants SET timezone = 'Asia/Shanghai' WHERE timezone = 'UTC';

-- +goose Down
-- 迁移之后在后台主动选了 Asia/Shanghai 的租户也会被改回 UTC：两者在库里分不开，
-- 回滚就是回到「没有站点时区」的状态。
ALTER TABLE tenants ALTER COLUMN timezone SET DEFAULT 'UTC';
UPDATE tenants SET timezone = 'UTC' WHERE timezone = 'Asia/Shanghai';
