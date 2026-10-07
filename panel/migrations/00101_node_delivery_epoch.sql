-- 节点下发纪元（node delivery epoch）：给 aegis-node 的进程内缓存一个「输入变没变」的读后写一致信号。
--
-- 节点每 15 秒拉一次用户名单，名单只取决于（租户, 节点池）与几张业务表；aegis-node 按池缓存
-- 结果，同池几百个节点合成一次查询。缓存要做到「改完立刻生效」——付款后的新用户、配额刚用尽
-- 的老用户、改成 strict 的设备判定——就得知道这些输入什么时候变过，而它们大多由别的进程写
-- （后台、门户、定时任务），甚至是运维直接执行的 SQL。
--
-- 做法：一个全局序列当纪元。下面这些写一提交就推进它；aegis-node 认证节点、拉生效配置时在同一条
-- 查询里顺手读出当前纪元，缓存条目记着自己算出时的纪元，纪元前进了就重算。
--   - 序列不加锁、不进事务：几百个并发写事务推进它不会互相等，也不会和业务行锁成环
--     （用一行计数器的话，每个写事务都要在提交时抢同一行）。
--   - 约束触发器 DEFERRABLE INITIALLY DEFERRED：在提交时才推进，纪元前进与数据可见之间只隔
--     提交本身。读方恰好卡在这道缝里算出旧结果的极端情况，由缓存自己的 TTL（几秒）兜底。
--   - 高频表只在「能不能用」翻转时推进：配额余额只在 remaining 跨过 0 时、流量包只在
--     consumed_bytes 与 granted_bytes 的大小关系翻转时、节点只在 status / serving_status
--     被写时、用户只在 user_group_id 被写时。每次扣量、每次心跳都不碰纪元。
--   - 不追踪的输入（在线设备记录、订阅到期的时间流逝）只靠 TTL：前者每分钟都在变，后者没有写。
--
-- 纪元只是「可能变了」的信号，多推进一次只是多算一次；回滚直接删掉，代码读不到会报错，
-- 所以回滚迁移必须连同 aegis-node 一起回退。

-- +goose Up

SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE SEQUENCE node_delivery_epoch AS bigint;

GRANT SELECT ON SEQUENCE node_delivery_epoch TO aegis_app;

CREATE FUNCTION app.bump_node_delivery_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
  PERFORM nextval('public.node_delivery_epoch');
  RETURN NULL;
END;
$$;

COMMENT ON FUNCTION app.bump_node_delivery_epoch() IS
  '节点下发纪元：下发输入变化时在提交时推进 node_delivery_epoch，aegis-node 据此作废进程内缓存。';

-- 订阅：状态、周期、套餐版本、设备数、node_uid、proxy_uuid 都进下发名单
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_subscriptions
  AFTER INSERT OR UPDATE OR DELETE ON subscriptions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 套餐版本：限速、设备上限
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_plan_versions
  AFTER INSERT OR UPDATE OR DELETE ON plan_versions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 套餐版本与节点池的授权关系
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_plan_node_pools
  AFTER INSERT OR UPDATE OR DELETE ON plan_node_pools
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 节点池的用户组限定（R104）
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_node_pool_user_groups
  AFTER INSERT OR UPDATE OR DELETE ON node_pool_user_groups
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 用户换组（R104）；登录时间等其余列的写不推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_users
  AFTER UPDATE OF user_group_id ON users
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 设备判定模式与余量（以及其余设置；设置写得很少）
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_system_settings
  AFTER INSERT OR UPDATE OR DELETE ON system_settings
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 配额余额：每次上报流量都会改，只在「用尽 / 未用尽」翻转时推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_quota_rows
  AFTER INSERT OR DELETE ON quota_balances
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_quota_exhaustion
  AFTER UPDATE ON quota_balances
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((OLD.remaining IS NOT NULL AND OLD.remaining <= 0)
        IS DISTINCT FROM (NEW.remaining IS NOT NULL AND NEW.remaining <= 0))
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 流量包：扣量会改，只在「还有余量 / 已用完」翻转时推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_traffic_pack_rows
  AFTER INSERT OR DELETE ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_traffic_pack_balance
  AFTER UPDATE ON traffic_pack_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((OLD.consumed_bytes < OLD.granted_bytes)
        IS DISTINCT FROM (NEW.consumed_bytes < NEW.granted_bytes))
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 节点签名身份：签发、吊销、过期改写
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_node_identities
  AFTER INSERT OR UPDATE OR DELETE ON node_identities
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 节点生命周期与服务状态（身份查询按它们拒绝退役节点）；心跳写的列不推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_node_status
  AFTER UPDATE OF status, serving_status ON nodes
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_node_status ON nodes;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_node_identities ON node_identities;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_traffic_pack_balance ON traffic_pack_grants;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_traffic_pack_rows ON traffic_pack_grants;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_quota_exhaustion ON quota_balances;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_quota_rows ON quota_balances;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_system_settings ON system_settings;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_users ON users;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_node_pool_user_groups ON node_pool_user_groups;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_plan_node_pools ON plan_node_pools;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_plan_versions ON plan_versions;
DROP TRIGGER IF EXISTS zz_node_delivery_epoch_subscriptions ON subscriptions;
DROP FUNCTION IF EXISTS app.bump_node_delivery_epoch();
DROP SEQUENCE IF EXISTS node_delivery_epoch;
-- +goose StatementEnd
