-- 在线设备视图改成按订阅取数（性能修复，语义不变）。
--
-- 00094 的写法是先把整张在线记录表按订阅 GROUP BY，再让调用方 JOIN 上来。
-- PostgreSQL 不会把 JOIN 条件推进带顶层聚合的视图，于是只查一条订阅也要把全表
-- 聚合一遍；窗口又写在 WHERE 里，逐行调 app.device_limit_window_minutes(tenant_id)
-- （函数体带子查询，不能内联），每行读一次系统设置。5000 用户的实测里，门户订阅
-- 列表 1329 次 × 3.38s，是混合负载下库时间第一；后台用户列表每个用户都把视图重算一遍。
--
-- 新写法两处变化：
--   1. 以订阅为驱动，每条订阅用 LATERAL 只聚合自己的在线记录。条件
--      「subscription_id = 这条订阅 AND last_seen_at > 截止」正好落在
--      idx_node_alive_recent (subscription_id, last_seen_at) 上；视图本身没有
--      顶层聚合，调用方 LEFT JOIN 它时整段可以被上提，按订阅 id 逐条走索引，
--      而不是先算全表。
--   2. 截止时间对当前租户（app.current_tenant_id()）只算一次：两个不相关标量子查询
--      在执行计划里是 InitPlan，整条语句求值一次。别的租户的行只有绕过 RLS 的会话
--      （迁移、运维）才看得到，仍按那个租户逐个调函数，结果与逐行求值相同。
--
-- 语义与 00094 一致：列名、类型、顺序不变；每条「窗口内有在线记录」的订阅一行，
-- 窗口内没有记录的订阅不出现；设备数按 IP 去重、节点数按节点去重、取最近上报时间；
-- 截止仍是严格大于 now() - 窗口。前提是在线记录的 tenant_id 等于它所属订阅的
-- tenant_id：唯一的写入方（UniProxy 在线上报）先在本租户里按 node_uid 查到订阅再写，
-- RLS 的 WITH CHECK 也只放行当前租户。PG18 测试 TestDeviceWindowPG18 把新旧两份
-- 定义放在同一批数据上逐行比对（多租户、各窗口、非法值、边界时刻）。
--
-- 窗口仍只有一个出处 app.device_limit_window_minutes（R103），这里没有分钟字面量。
-- 在线记录的清理截止必须大于最大窗口 60 分钟，否则窗口内的行会被删掉、在线数偏小。

-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE VIEW subscription_online_devices AS
SELECT s.tenant_id,
       s.id              AS subscription_id,
       a.device_count,
       a.node_count,
       a.last_seen_at
  FROM subscriptions s
  CROSS JOIN LATERAL (
        SELECT count(DISTINCT x.ip_hash)  AS device_count,
               count(DISTINCT x.node_id)  AS node_count,
               max(x.last_seen_at)        AS last_seen_at
          FROM node_alive_ips x
         WHERE x.subscription_id = s.id
           AND x.tenant_id = s.tenant_id
           AND x.last_seen_at > now() - make_interval(mins =>
                 CASE WHEN s.tenant_id = (SELECT app.current_tenant_id())
                      THEN (SELECT app.device_limit_window_minutes(app.current_tenant_id()))
                      ELSE app.device_limit_window_minutes(s.tenant_id)
                 END)
        -- 不带 GROUP BY 的聚合总会产出一行；窗口内没有记录时由 HAVING 去掉，
        -- 与 00094「没有在线记录的订阅不出现」一致
        HAVING count(*) > 0
  ) a;

COMMENT ON VIEW subscription_online_devices IS
  '每条订阅当前在线的设备数（按 IP 去重，跨节点汇总）。窗口按租户读 device_limit.window_minutes（5/10/30/60，缺省 5，R103）；按订阅走 idx_node_alive_recent，当前租户的窗口每条语句只算一次（00098）。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
-- 恢复 00094 的定义（全表按订阅聚合、逐行求窗口）
CREATE OR REPLACE VIEW subscription_online_devices AS
SELECT tenant_id,
       subscription_id,
       count(DISTINCT ip_hash)                    AS device_count,
       count(DISTINCT node_id)                    AS node_count,
       max(last_seen_at)                          AS last_seen_at
  FROM node_alive_ips
 WHERE last_seen_at > now() - make_interval(mins => app.device_limit_window_minutes(tenant_id))
 GROUP BY tenant_id, subscription_id;

COMMENT ON VIEW subscription_online_devices IS
  '每条订阅当前在线的设备数（按 IP 去重，跨节点汇总）。窗口按租户读 device_limit.window_minutes（5/10/30/60，缺省 5，R103）。';
-- +goose StatementEnd
