-- 时区进节点下发纪元（性能总方案第 0 波 C 缓存收拢，w12cache；给 N2 的「推送查订阅走名单缓存」铺路）。
--
-- 按日流量按「用户 timezone，无效退回站点 timezone，再退回 UTC」切日（nodefabric 的 UsageLocation）。
-- N2 要把这两个时区随节点用户名单一起缓存（名单缓存按下发纪元判有效，见 00101、00153），
-- 不再每份流量上报单独查一次订阅；那样时区一改、缓存就必须作废，否则改完时区之后的上报还按旧时区
-- 切日。站点时区只经 POST v1/settings/site 改（adminops 的站点设置），用户时区经个人资料改，都很少写。
--
-- 这里：
--   - tenants：时区真变了才推进下发纪元（约束触发器，提交时推进，同 00101）；
--   - users：00141 那条触发器换成「user_group_id、status 或 timezone 真变了才推进」。登录时间等其余
--     列的写照旧不推进。
-- 推进走 app.bump_node_delivery_epoch()（函数体不动，仍是 00153 的版本：推进序列并发 'd'）。
--
-- 锁：DROP / CREATE TRIGGER 对 users、tenants 拿 SHARE ROW EXCLUSIVE，只建触发器、不扫表，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
-- 站点时区（按日流量的切日口径）
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_tenant_timezone
  AFTER UPDATE OF timezone ON public.tenants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.timezone IS DISTINCT FROM NEW.timezone)
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

DROP TRIGGER zz_node_delivery_epoch_users ON public.users;

-- 用户换组（R104）、账号状态变化（封禁即断、解封恢复）或时区变化（按日流量切日）；其余列的写不推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_users
  AFTER UPDATE OF user_group_id, status, timezone ON public.users
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.user_group_id IS DISTINCT FROM NEW.user_group_id
        OR OLD.status IS DISTINCT FROM NEW.status
        OR OLD.timezone IS DISTINCT FROM NEW.timezone)
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 恢复 00141 原样
-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_users ON public.users;
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_users
  AFTER UPDATE OF user_group_id, status ON public.users
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.user_group_id IS DISTINCT FROM NEW.user_group_id
        OR OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

DROP TRIGGER zz_node_delivery_epoch_tenant_timezone ON public.tenants;
-- +goose StatementEnd
