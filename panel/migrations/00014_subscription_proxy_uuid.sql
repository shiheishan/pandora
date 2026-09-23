-- 订阅的协议层用户标识。
--
-- 与 subscription_credentials 的区别（两者容易混淆，但绝不能合并）：
--   · subscription_credentials.token —— 用户**拉取订阅配置**时的 HTTP 凭据
--   · subscriptions.proxy_uuid       —— 用户**连接节点**时的协议标识，
--                                       即 VMess/VLESS 的 user id、Trojan 的 password
--
-- 前者泄露只是别人能看到你的节点列表；后者泄露等于别人能直接用你的流量。
-- 两者的轮换时机、下发对象、生命周期都不同，因此分开存放。
--
-- 不复用 subscriptions.id：主键一旦泄露就无法轮换，而这个值会出现在
-- 每一个客户端配置文件里，被抄走的概率远高于内部 ID（XBD-003 要求可重置）。

-- +goose Up

-- +goose StatementBegin
ALTER TABLE subscriptions
  ADD COLUMN proxy_uuid uuid NOT NULL DEFAULT gen_random_uuid();

-- 节点端按 uuid 认证用户，必须全局唯一
CREATE UNIQUE INDEX subscriptions_proxy_uuid_key ON subscriptions (proxy_uuid);

COMMENT ON COLUMN subscriptions.proxy_uuid IS
  '协议层用户标识（VMess/VLESS uuid、Trojan password）。可独立重置，重置后旧配置立即失效。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP INDEX IF EXISTS subscriptions_proxy_uuid_key;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS proxy_uuid;
-- +goose StatementEnd
