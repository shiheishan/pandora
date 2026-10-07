-- 流量汇总的 400 天口径（用户定：原始数据 31 天、汇总 400 天；审计 ledger 第三节第 4 条）。
--
-- 一、billed_bytes：两张小时汇总各加一列「乘过倍率的计费字节」
--
-- 小时汇总存的是原始字节，按日用量与配额存的是乘过倍率的字节，倍率只留在原始留档上：
-- 留档 31 天后被清理，对账就再也做不了。入库时把 Go 记账算出的每个 uid 的计费量
-- （本节点放行名单内、合规、按节点倍率折算后，与传给扣量的是同一组数）一并累加进来：
--   - node_user_traffic_hourly.billed_bytes：这一小时这个 uid 在这个节点的计费字节；
--   - node_traffic_hourly.billed_bytes：这一小时这个节点全部 uid 的计费字节；
--   - 重复上报不计（与扣量一致）。
-- 列可空，不给默认值：已有的桶里没有这个数（NULL = 未知），不能当成 0 去对账。迁移之后
-- 新开的桶从 0 起累加；跨迁移的那个桶 NULL + x 仍是 NULL，整桶算未知，不会半截有数。
-- 非负约束用 NOT VALID 加：只管之后的写入，不为存量的 NULL 扫一遍表。
--
-- 二、node_user_traffic_daily：「节点 × uid」按天汇总，保留 400 天
--
-- 小时级的节点 × uid 表每天 7–14 万行（5k 用户），留 400 天要 5–10GB；按天约 1–1.5 万行 / 天，
-- 400 天约 600 万行。按天表由 aegis-admin 的保留期任务从小时表汇总（nodefabric
-- RefreshTrafficDaily），入库热路径一条语句都不加：
--   - 只汇总已经结束 10 分钟以上的 UTC 自然日（小时桶按 received_at 的 UTC 整点切，那时
--     这一天的上报事务早已提交），而且一天只写一次（ON CONFLICT DO NOTHING）；
--   - 只汇总整天都还在小时表保留期内的日子，不会写出被清理截掉一半的一天；
--   - 每轮最多补几天、从新到旧：上线后积压的历史日子（小时表现有的 70 天）由之后的几轮
--     补齐，迁移里不回填，停写窗口不付这个代价。
-- 切日用 UTC（与小时桶同一口径）；按日用量是按用户 / 站点时区切的日，两者对账时按小时表
-- 换算，不要直接拿这张表的 day 去比。
-- 派生读数，不是证据；丢了可以在小时表保留期内重算。不挂 zz_notify 触发器。
--
-- 耗时：加可空列、加 NOT VALID 约束、建空表都只改目录，5k 规模下毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.node_traffic_hourly ADD COLUMN billed_bytes numeric;
ALTER TABLE public.node_traffic_hourly
  ADD CONSTRAINT node_traffic_hourly_billed_bytes_check CHECK (billed_bytes >= 0) NOT VALID;
COMMENT ON COLUMN public.node_traffic_hourly.billed_bytes IS
  '这一小时乘过节点倍率的计费字节（非重复上报、放行名单内的合规项）；NULL 表示这个桶跨过了 00133 之前，未知。';

ALTER TABLE public.node_user_traffic_hourly ADD COLUMN billed_bytes numeric;
ALTER TABLE public.node_user_traffic_hourly
  ADD CONSTRAINT node_user_traffic_hourly_billed_bytes_check CHECK (billed_bytes >= 0) NOT VALID;
COMMENT ON COLUMN public.node_user_traffic_hourly.billed_bytes IS
  '这一小时这个 uid 乘过节点倍率的计费字节；NULL 表示这个桶跨过了 00133 之前，未知。';

-- +goose StatementBegin
CREATE TABLE public.node_user_traffic_daily (
  tenant_id      uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  -- UTC 自然日（与小时桶同一口径）
  day            date NOT NULL,
  node_id        uuid NOT NULL REFERENCES public.nodes(id) ON DELETE CASCADE,
  node_uid       bigint NOT NULL,
  upload_bytes   numeric NOT NULL CHECK (upload_bytes >= 0),
  download_bytes numeric NOT NULL CHECK (download_bytes >= 0),
  -- 乘过倍率的计费字节；这一天有任一小时未知（00133 之前）就整天未知
  billed_bytes   numeric CHECK (billed_bytes >= 0),
  entry_count    bigint NOT NULL CHECK (entry_count > 0),
  last_report_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, day, node_id, node_uid)
);

SELECT app.enable_tenant_rls('node_user_traffic_daily');

-- 应用汇总写入、读，并由保留期任务删 400 天以前的行；不清表
GRANT SELECT, INSERT, UPDATE, DELETE ON public.node_user_traffic_daily TO aegis_app;
REVOKE TRUNCATE ON public.node_user_traffic_daily FROM aegis_app;

COMMENT ON TABLE public.node_user_traffic_daily IS
  '节点流量按 UTC 自然日汇总（节点 × uid），由保留期任务从小时表汇总已结束的日子，保留 400 天。派生读数。';
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
-- 派生表，删掉不丢证据；本迁移之前的代码不读也不写它
DROP TABLE IF EXISTS public.node_user_traffic_daily;
-- +goose StatementEnd

ALTER TABLE public.node_user_traffic_hourly DROP COLUMN IF EXISTS billed_bytes;
ALTER TABLE public.node_traffic_hourly DROP COLUMN IF EXISTS billed_bytes;
