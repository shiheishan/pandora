-- +goose Up
-- 节点出站与分流（NODE-012）
--
-- 机场节点默认是直出的，但真实运营几乎总要分流：流媒体走解锁专线、
-- 广告域名直接拒绝、大陆回程走中转。这些规则必须能在面板上配，
-- 而不是登到每台机器上改配置文件。
--
-- 关键设计：这里存的是**内核中立**的格式。
--
-- sing-box 和 xray 的路由配置写法完全不同，但语义高度一致 ——
-- 两者都源自 v2ray 的设计：域名（精确/后缀/关键字/正则）、IP CIDR、
-- GeoIP、端口，匹配后指向一个出站。因此中间层只描述「匹配什么、去哪里」，
-- 由节点端翻译成各内核的方言。
--
-- 好处不只是省事：面板不绑定任何一个内核的配置格式，
-- 换内核、加内核都不需要改这两张表，也不需要用户重录一遍规则。

CREATE TABLE IF NOT EXISTS node_outbounds (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL,
  -- node_id 为 NULL 表示这是一条全局出站，所有节点都可用。
  -- 大多数出站（直连、拒绝、一条公共中转）本来就该全局共享，
  -- 逐节点复制一遍只会在改的时候漏掉几台。
  node_id      uuid REFERENCES nodes(id) ON DELETE CASCADE,

  tag          text NOT NULL,
  type         text NOT NULL,
  -- settings 是协议特有参数：server / port / uuid / password / tls / network …
  -- 平台不解释它，原样下发给节点端（与 nodes.protocol_config 同样的约定）
  settings     jsonb NOT NULL DEFAULT '{}'::jsonb,
  sort_order   int  NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT node_outbounds_type_check CHECK (type IN (
    'direct', 'block',
    'socks', 'http', 'shadowsocks', 'vmess', 'vless', 'trojan',
    'hysteria', 'hysteria2', 'tuic', 'anytls', 'shadowtls', 'wireguard'
  )),
  -- tag 是路由规则引用出站的唯一凭据，同一范围内不能重名
  CONSTRAINT node_outbounds_tag_len CHECK (char_length(tag) BETWEEN 1 AND 64)
);

-- 全局出站与节点私有出站分开建唯一索引：
-- NULL 在唯一索引里互不相等，直接写 (tenant_id, node_id, tag) 挡不住重复的全局 tag
CREATE UNIQUE INDEX IF NOT EXISTS node_outbounds_global_tag
  ON node_outbounds (tenant_id, tag) WHERE node_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS node_outbounds_node_tag
  ON node_outbounds (tenant_id, node_id, tag) WHERE node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS node_outbounds_node_idx
  ON node_outbounds (tenant_id, node_id);

CREATE TABLE IF NOT EXISTS node_routes (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL,
  node_id      uuid REFERENCES nodes(id) ON DELETE CASCADE,

  -- priority 小的先匹配。留出间隔（10、20、30…）便于中间插入，
  -- 否则每加一条规则都要重排后面所有行
  priority     int  NOT NULL DEFAULT 100,
  -- matcher 形如 {"domain_suffix":["netflix.com"],"geoip":["cn"],"port":[443]}
  -- 空对象表示无条件匹配 —— 那就是「默认出口」这条兜底规则
  matcher      jsonb NOT NULL DEFAULT '{}'::jsonb,
  outbound_tag text NOT NULL,
  enabled      boolean NOT NULL DEFAULT true,
  note         text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS node_routes_node_idx
  ON node_routes (tenant_id, node_id, priority);

-- 两张表都按租户隔离（DATA-002）
ALTER TABLE node_outbounds ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_outbounds FORCE ROW LEVEL SECURITY;
ALTER TABLE node_routes    ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_routes    FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS node_outbounds_tenant ON node_outbounds;
CREATE POLICY node_outbounds_tenant ON node_outbounds
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());

DROP POLICY IF EXISTS node_routes_tenant ON node_routes;
CREATE POLICY node_routes_tenant ON node_routes
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON node_outbounds TO aegis_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON node_routes    TO aegis_app;

COMMENT ON TABLE node_outbounds IS
  '节点出站。内核中立格式，由节点端翻译成 sing-box / xray 各自的配置。';
COMMENT ON TABLE node_routes IS
  '节点分流规则。priority 小的先匹配，matcher 为空对象表示兜底。';
