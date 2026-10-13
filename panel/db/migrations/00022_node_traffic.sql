-- 节点通道的上报入账：流量留档、节点与订阅的小时汇总、机器指标、在线 IP。属主 node。
--
-- 与旧库的不同：
--   - 流量按服务器上报（一份含这台服务器各节点的条目），幂等键 UNIQUE(server_id, report_id) 是必填的；
--     不再有无编号上报的 10 秒内容哈希去重与重复件行。同一编号再来：内容摘要相同就什么都不做，不同是节点端缺陷（记日志、不入账）。
--   - 留档存的是解码后的条目（JSON 数组），不是 UniProxy 的原始报文；小时汇总由 Go 在入账的同一事务里写，不再从留档反解析。
--   - 指标按服务器记（一台机器一份），存字节等整数原值，MB / GB / 百分比在读的时候换算。
--   - 去掉租户列与 RLS；外键一律 RESTRICT（服务器、节点是软删，行不消失；订阅不删）。
--
-- 留档与指标只追加（deny_mutation）；保留期清理（SECURITY DEFINER 函数、时间索引与每天的定时任务）在 00023；
-- 不建按日汇总表（小时汇总即汇总，留 400 天）。

-- +goose Up

-- 流量上报留档：每份上报一行，争议时逐笔回溯的证据。
CREATE TABLE node_traffic_reports (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  -- 节点给的幂等键（proto TrafficReport.report_id）
  report_id       text NOT NULL CHECK (report_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
  received_at     timestamptz NOT NULL DEFAULT now(),
  -- 条目原文：[{"node_id","uid","upload","download"}…]，按 (node_id, uid, upload, download) 排序
  entries         jsonb NOT NULL CHECK (jsonb_typeof(entries) = 'array'),
  -- 条目的内容摘要（SHA-256，与条目顺序无关），判断同一编号的两份是否相同
  content_sha256  bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
  entry_count     integer NOT NULL CHECK (entry_count >= 0),
  -- 合规条目的上下行合计（实际字节，未乘倍率）
  upload_bytes    bigint NOT NULL CHECK (upload_bytes >= 0),
  download_bytes  bigint NOT NULL CHECK (download_bytes >= 0),
  -- 没有入账的条目：不合规（节点不在这台服务器、uid 非正、单项超上限）与名单外
  invalid_count   integer NOT NULL CHECK (invalid_count >= 0),
  unlisted_count  integer NOT NULL CHECK (unlisted_count >= 0),
  CONSTRAINT node_traffic_reports_server_report_key UNIQUE (server_id, report_id),
  CONSTRAINT node_traffic_reports_counts CHECK (invalid_count + unlisted_count <= entry_count)
);

CREATE TRIGGER node_traffic_reports_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON node_traffic_reports
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 节点 × 小时（UTC 整点）的流量：后台节点列表的 24 小时流量读它。
-- upload / download 是合规条目的实际字节（含名单外），billed 是已入账的计费字节（乘过倍率）。
CREATE TABLE node_traffic_hourly (
  node_id         uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
  hour_start      timestamptz NOT NULL CHECK (extract(epoch FROM hour_start) % 3600 = 0),
  upload_bytes    bigint NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
  download_bytes  bigint NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
  billed_bytes    bigint NOT NULL DEFAULT 0 CHECK (billed_bytes >= 0),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, hour_start)
);

-- 节点 × 订阅 × 小时：只记入账了的条目（名单内、找得到订阅），看板的用户流量排行与对账读它。
CREATE TABLE node_user_traffic_hourly (
  hour_start      timestamptz NOT NULL CHECK (extract(epoch FROM hour_start) % 3600 = 0),
  node_id         uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
  upload_bytes    bigint NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
  download_bytes  bigint NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
  billed_bytes    bigint NOT NULL DEFAULT 0 CHECK (billed_bytes >= 0),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (hour_start, node_id, subscription_id)
);

-- 机器指标（时序，只追加）：节点心跳带上来的一个点。整数口径与 proto HostMetrics 相同。
-- 网络是开机以来的累计字节，速率在读的时候按相邻两点差分（重启归零表现为负差，丢弃）。
CREATE TABLE node_metrics (
  server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  recorded_at       timestamptz NOT NULL,
  -- CPU 占用，万分比
  cpu_bp            integer NOT NULL CHECK (cpu_bp BETWEEN 0 AND 10000),
  mem_used_bytes    bigint NOT NULL CHECK (mem_used_bytes >= 0),
  mem_total_bytes   bigint NOT NULL CHECK (mem_total_bytes >= 0),
  disk_used_bytes   bigint NOT NULL CHECK (disk_used_bytes >= 0),
  disk_total_bytes  bigint NOT NULL CHECK (disk_total_bytes >= 0),
  -- 负载 × 100
  load1_cp          integer NOT NULL CHECK (load1_cp >= 0),
  load5_cp          integer NOT NULL CHECK (load5_cp >= 0),
  load15_cp         integer NOT NULL CHECK (load15_cp >= 0),
  net_rx_bytes      bigint NOT NULL CHECK (net_rx_bytes >= 0),
  net_tx_bytes      bigint NOT NULL CHECK (net_tx_bytes >= 0),
  tcp_conns         integer NOT NULL CHECK (tcp_conns >= 0),
  uptime_sec        bigint NOT NULL CHECK (uptime_sec >= 0),
  -- 有的项没采到（取 0）
  partial           boolean NOT NULL DEFAULT false,
  -- 查一台服务器最近 N 分钟、取最新一点都走主键
  PRIMARY KEY (server_id, recorded_at)
);

CREATE TRIGGER node_metrics_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON node_metrics
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 在线来源：节点 × 订阅 × 设备键，记最后在线时间（upsert）。只存设备键的 HMAC-SHA256
-- （IPv4 按地址、IPv6 按 /64；密钥由主密钥派生，库里的摘要无法穷举还原），在线设备数是运营指标，不留可回溯到个人的原始地址。
CREATE TABLE node_alive_ips (
  node_id         uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
  ip_hash         bytea NOT NULL CHECK (octet_length(ip_hash) = 32),
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  -- 节点列表按节点数在线用户与 IP，走主键前缀
  PRIMARY KEY (node_id, subscription_id, ip_hash)
);

-- +goose Down

DROP TABLE node_alive_ips;
DROP TABLE node_metrics;
DROP TABLE node_user_traffic_hourly;
DROP TABLE node_traffic_hourly;
DROP TABLE node_traffic_reports;
