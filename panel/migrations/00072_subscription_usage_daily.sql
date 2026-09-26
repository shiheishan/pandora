-- 按日流量（M4，门户-02 `GET v1/me/subscriptions/{id}/usage`）：每条订阅每天
-- 记一个计费后字节数，供门户概览的「本期用量」柱状图、日均与今天。
--
-- 此前没有任何按日数据源：usage_aggregates / usage_events 已被 00067 删除，
-- node_traffic_reports 只有节点级合计与原始报文，quota_balances 只有周期累计。
--
-- 写入点只有一处：UniProxy 流量上报（nodefabric），与 quota_balances 扣量在
-- 同一个事务里 upsert，被判定为重试的重复报文两边都不记——柱状图与配额读数
-- 永远出自同一次扣量。bytes 是乘过节点倍率之后的计费字节，与配额口径一致；
-- 超出套餐、改扣流量包的部分也照样计入（它同样是这条订阅这一天用掉的）。
--
-- day 是用户所在时区（用户 timezone，无效时退回租户 timezone，再退回 UTC）的
-- 自然日，由应用按上报时刻算好写入。用户改时区后，历史行保留原来的日界。
--
-- 不挂 zz_notify 触发器：写入频率就是节点上报频率，与 00076 摘掉
-- quota_balances 同一个理由；页面靠拉取刷新。
-- 行随订阅删除而删除：它是扣量的派生读数，不是账目证据（证据在
-- node_traffic_reports）。

-- +goose Up

-- +goose StatementBegin
CREATE TABLE subscription_usage_daily (
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL,
  day             date NOT NULL,
  bytes           bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, subscription_id, day),
  CONSTRAINT subscription_usage_daily_subscription_fk
    FOREIGN KEY (tenant_id, subscription_id)
    REFERENCES subscriptions (tenant_id, id) ON DELETE CASCADE
);
SELECT app.enable_tenant_rls('subscription_usage_daily');
-- 应用只累加、只读：不删行、不清表（删除只经订阅级联）。
GRANT SELECT, INSERT, UPDATE ON subscription_usage_daily TO aegis_app;
REVOKE DELETE, TRUNCATE ON subscription_usage_daily FROM aegis_app;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS subscription_usage_daily;
