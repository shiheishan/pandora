-- +goose Up
-- 节点补齐 Xboard（用户 2026-10-14 定）：标签（#46）、每周期流量上限与重置日（#18）、按时段的动态倍率（#47）、
-- 中转节点（#19，表 node_relays）、ECH 密钥（#48，表 node_ech_keys）。全部属 node 模块，只有 node 的 repo 读写。
--
-- nodes 新列：
--   tags                   标签（jsonb 字符串数组，元素在 Go 里规范化与校验：去首尾空白、非空、≤16 字、去重；这里钉个数与类型）
--   traffic_limit_bytes    每周期流量上限（实际字节，不乘倍率）；NULL = 不限
--   traffic_reset_day      每月几号（站点时区 0 点）自动清零用量；NULL = 不自动重置（只能后台手动），短月取月末
--   traffic_used_bytes     本周期已用实际字节（流量入账同一事务累加，饱和到 bigint 上限）
--   traffic_reset_at       本周期用量从何时起算（建节点、手动或自动重置时写）
--   traffic_exceeded_at    超限时刻；与「有上限且已用 >= 上限」同真同假（nodes_traffic_exceeded_shape），
--                          非空时节点不进 DeliverableNodeCond / ServedOnServerCond（不下发、节点端停掉入站）
--   traffic_rate_schedule  时段倍率规则（jsonb 数组，每段 {"start":"HH:MM","end":"HH:MM","rate":"1.50"}，站点时区，
--                          半开区间、不跨零点、不重叠，由 Go 校验）；没命中用 traffic_rate
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：nodes 加列都是常量缺省（now() 是 STABLE，按快速缺省处理，不重写表），
--   各 CHECK 要整表扫一遍校验，期间 ACCESS EXCLUSIVE 挡住节点的读写；nodes 是后台手工维护的小表（每台服务器几十个节点），毫秒级。
--   两张新表建在空表上，瞬间完成。
--   预检：SELECT count(*) FROM nodes;（新列都有缺省，存量行一定满足新 CHECK）
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE nodes
  ADD COLUMN tags jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN traffic_limit_bytes bigint,
  ADD COLUMN traffic_reset_day smallint,
  ADD COLUMN traffic_used_bytes bigint NOT NULL DEFAULT 0,
  ADD COLUMN traffic_reset_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN traffic_exceeded_at timestamptz,
  ADD COLUMN traffic_rate_schedule jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE nodes
  ADD CONSTRAINT nodes_tags_shape CHECK (jsonb_typeof(tags) = 'array' AND jsonb_array_length(tags) <= 10),
  ADD CONSTRAINT nodes_traffic_limit_check CHECK (traffic_limit_bytes > 0),
  ADD CONSTRAINT nodes_traffic_reset_day_check CHECK (traffic_reset_day BETWEEN 1 AND 31),
  ADD CONSTRAINT nodes_traffic_used_check CHECK (traffic_used_bytes >= 0),
  ADD CONSTRAINT nodes_traffic_exceeded_shape CHECK (
    (traffic_exceeded_at IS NOT NULL) = (traffic_limit_bytes IS NOT NULL AND traffic_used_bytes >= traffic_limit_bytes)),
  ADD CONSTRAINT nodes_traffic_rate_schedule_shape CHECK (
    jsonb_typeof(traffic_rate_schedule) = 'array' AND jsonb_array_length(traffic_rate_schedule) <= 12);

-- 中转节点（#19）：只出现在订阅里的条目，用父节点的协议配置、倍率、标签、ECH、池与资格，换成自己的地址与端口
-- （以及可选的 SNI / Host 覆盖）。中转机上不跑 pdnd，转发由站长自己做（iptables、realm 等）：
-- 不占服务器端口、不进配置文档与名单、没有服务状态机，只有一个启停开关。父节点只能是 nodes 里的普通节点（中转不能再挂中转）。
-- 与「自定义出站链式转发」（node_outbounds，节点出口再经别的代理出去）是两回事：那个改的是节点的出站，这个改的是用户的入口。
CREATE TABLE node_relays (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  parent_node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  -- 订阅里给用户看的名字
  name           text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 64),
  server_host    text NOT NULL CHECK (char_length(server_host) BETWEEN 1 AND 255),
  server_port    integer NOT NULL CHECK (server_port BETWEEN 1 AND 65535),
  -- 覆盖父节点的 TLS SNI（server_name）与 ws/grpc 等传输的 Host；NULL = 沿用父节点
  sni            text CHECK (char_length(sni) BETWEEN 1 AND 253),
  host_header    text CHECK (char_length(host_header) BETWEEN 1 AND 255),
  enabled        boolean NOT NULL DEFAULT true,
  sort_order     integer NOT NULL DEFAULT 0,
  row_version    bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- 订阅线路按父节点批量取、后台按父节点列出
CREATE INDEX node_relays_parent_idx ON node_relays (parent_node_id, sort_order, id);

-- ECH 密钥（#48）：每个节点至多一把「当前」密钥（superseded_at IS NULL），轮换后旧钥写 superseded_at，
-- 过渡期内仍下发给节点端解密（不再进订阅），过期由任务删除。config_list 是公开的 ECHConfigList（含 2 字节总长前缀，
-- 只放一个 ECHConfig），private_key_sealed 是 X25519 私钥经 Sealer 封存（AAD "node_ech_key:<节点 id>"）。
CREATE TABLE node_ech_keys (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  node_id            uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  public_name        text NOT NULL CHECK (char_length(public_name) BETWEEN 1 AND 253),
  config_list        bytea NOT NULL CHECK (octet_length(config_list) BETWEEN 8 AND 1024),
  private_key_sealed bytea NOT NULL CHECK (octet_length(private_key_sealed) BETWEEN 1 AND 256),
  created_at         timestamptz NOT NULL DEFAULT now(),
  superseded_at      timestamptz,
  CONSTRAINT node_ech_keys_superseded_check CHECK (superseded_at IS NULL OR superseded_at >= created_at)
);

-- 每个节点只有一把当前密钥
CREATE UNIQUE INDEX node_ech_keys_current_key ON node_ech_keys (node_id) WHERE superseded_at IS NULL;
-- 配置文档按节点批量取、过期清理按 superseded_at 找
CREATE INDEX node_ech_keys_node_idx ON node_ech_keys (node_id, created_at DESC);
CREATE INDEX node_ech_keys_superseded_idx ON node_ech_keys (superseded_at) WHERE superseded_at IS NOT NULL;

-- 授权不写在迁移里：安装与升级在 migrate up 之后跑 configure-app-role.sql。

-- +goose Down
-- 中转、ECH 密钥、标签、流量上限与时段倍率一起丢弃（回滚后的代码不认识它们）；超限停发的节点随列删除恢复下发。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DROP TABLE IF EXISTS node_ech_keys;
DROP TABLE IF EXISTS node_relays;

ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_traffic_rate_schedule_shape,
  DROP CONSTRAINT IF EXISTS nodes_traffic_exceeded_shape,
  DROP CONSTRAINT IF EXISTS nodes_traffic_used_check,
  DROP CONSTRAINT IF EXISTS nodes_traffic_reset_day_check,
  DROP CONSTRAINT IF EXISTS nodes_traffic_limit_check,
  DROP CONSTRAINT IF EXISTS nodes_tags_shape;

ALTER TABLE nodes
  DROP COLUMN IF EXISTS traffic_rate_schedule,
  DROP COLUMN IF EXISTS traffic_exceeded_at,
  DROP COLUMN IF EXISTS traffic_reset_at,
  DROP COLUMN IF EXISTS traffic_used_bytes,
  DROP COLUMN IF EXISTS traffic_reset_day,
  DROP COLUMN IF EXISTS traffic_limit_bytes,
  DROP COLUMN IF EXISTS tags;
