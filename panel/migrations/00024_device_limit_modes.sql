-- +goose Up
-- 设备数限制：管理员覆盖与两种判定模式
--
-- 限制值的来源有三层，从低到高覆盖：
--   1. plan_versions.max_devices     套餐规定，购买时确定
--   2. subscriptions.device_limit    管理员对这一条订阅的单独调整
--   3. 0 / NULL                      不限制
--
-- 之所以把覆盖值放在订阅上而不是用户上：同一个用户可能同时持有多条订阅，
-- 限制是随订阅走的（买了两个套餐就该有两份额度）。放在用户上会让
-- 「给他多开一台设备」这个操作产生歧义 —— 是哪条订阅多开？

ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS device_limit int;

COMMENT ON COLUMN subscriptions.device_limit IS
  '管理员对这条订阅的设备数覆盖。NULL 表示沿用套餐的 max_devices，0 表示不限制。';

-- 全局在线视图。
--
-- node_alive_ips 是按 (节点, 订阅, IP) 记的，同一台设备连了三个节点
-- 就会出现三行。判断「这个人到底用了几台设备」必须按 IP 去重，
-- 而不是数行数 —— 那会把一台设备算成三台。
CREATE OR REPLACE VIEW subscription_online_devices AS
SELECT tenant_id,
       subscription_id,
       count(DISTINCT ip_hash)                    AS device_count,
       count(DISTINCT node_id)                    AS node_count,
       max(last_seen_at)                          AS last_seen_at
  FROM node_alive_ips
 -- 与节点端的 TTL 对齐。取更长的窗口会把已经离线的设备继续算在内，
 -- 让用户莫名其妙地连不上
 WHERE last_seen_at > now() - interval '5 minutes'
 GROUP BY tenant_id, subscription_id;

COMMENT ON VIEW subscription_online_devices IS
  '每条订阅当前在线的设备数（按 IP 去重，跨节点汇总）。窗口 5 分钟，与节点端 TTL 一致。';

-- 判定模式。
--
-- loose （默认）：每个节点各自判断，互不知情。
--   一台设备只占一个节点的名额，用户在多个节点上能连出总数超限的设备。
--   好处是判断在节点本地完成，没有延迟、不依赖面板、不会因为上报滞后误杀。
--
-- strict：面板汇总跨节点的去重设备数，超了就把这条订阅从所有节点摘掉。
--   真正堵住共享账号，代价是有一个同步周期的延迟，
--   而且上报一旦异常（节点掉线导致 IP 残留），可能误伤正常用户。
--
-- 默认给 loose 是因为误杀的代价不对称：漏判只是少收一点钱，
-- 误判是付费用户全节点连不上，直接变成工单和退款。
INSERT INTO system_settings (tenant_id, key, value, value_schema, updated_by)
SELECT id, 'device_limit.mode', '"loose"'::jsonb,
       '{"enum":["loose","strict"]}'::jsonb, NULL
  FROM tenants
 WHERE NOT EXISTS (
   SELECT 1 FROM system_settings s
    WHERE s.tenant_id = tenants.id AND s.key = 'device_limit.mode');

-- strict 模式下超限的宽容度。
--
-- 不做成「超一个就断」：IP 会因为网络切换短暂重复，卡得太死会让
-- 正常用户在通勤路上不断掉线。留一台的余量能吸收绝大多数抖动。
INSERT INTO system_settings (tenant_id, key, value, value_schema, updated_by)
SELECT id, 'device_limit.grace', '1'::jsonb,
       '{"type":"integer","minimum":0,"maximum":5}'::jsonb, NULL
  FROM tenants
 WHERE NOT EXISTS (
   SELECT 1 FROM system_settings s
    WHERE s.tenant_id = tenants.id AND s.key = 'device_limit.grace');
