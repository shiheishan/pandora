-- 账号状态进节点下发纪元（用户 2026-10-07 定「封禁即断，解封恢复」；w8node）。
--
-- 节点用户名单（nodefabric 的 Service.nodeUsers）从这一版起只收 active 账号的订阅：后台改状态
-- （adminops.SetUserStatus）、风控批量停用（adminops.DisableIPClusterAccounts）、以后的注销流程、
-- 运维直接执行的 SQL，都是 UPDATE users SET status。00101 的用户触发器只在 user_group_id 被写时
-- 推进纪元，改状态不推进：aegis-node 的名单缓存要等 TTL，事件流也收不到信号。
--
-- 这里把那条触发器换成「user_group_id 或 status 真变了才推进」。纪元一前进，aegis-node 的
-- 下发信号循环（nodestream_epoch.go）一秒内重算名单、推给连着的节点，pdnd 按新名单断开被移出
-- 用户的已有连接；恢复成 active 同样推进，自动回到名单。登录时间等其余列的写照旧不推进。
--
-- 锁：DROP / CREATE TRIGGER 对 users 拿 SHARE ROW EXCLUSIVE，只建触发器、不扫表，毫秒级；
-- lock_timeout 拿不到锁就失败重来，不排在业务事务后面堵住整张表。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_users ON public.users;

-- 用户换组（R104）或账号状态变化（封禁即断、解封恢复）；登录时间等其余列的写不推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_users
  AFTER UPDATE OF user_group_id, status ON public.users
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.user_group_id IS DISTINCT FROM NEW.user_group_id
        OR OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 恢复 00101 原样
-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_users ON public.users;
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_users
  AFTER UPDATE OF user_group_id ON users
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd
