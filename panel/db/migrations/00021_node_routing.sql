-- 出站、分流规则与路由组（属主 node，docs/architecture.md 第 4.1 节）。
--
-- 存的是内核中立的格式：出站是 tag + 类型 + settings（原样下发），规则是 matcher（匹配什么）+ outbound_tag（去哪）。
-- 范围三选一：全局（node_id、group_id 都空）/ 节点私有（node_id）/ 路由组（group_id），CHECK 钉住至多一个非空。
-- 生效合并（节点私有 → 所在各路由组按 sort_order, id → 全局）只在 Go 里一处做，配置文档现算，不落库。
--
-- 与旧库的不同：
--   - 出站 tag 在同一范围内不分大小写唯一、不许占用内置名 direct / block，由库钉住（旧库只在 Go 里查）。
--   - 规则多一列 sort_order：保存时按 priority 稳定排序后的下标，读按 (priority, sort_order)，
--     同 priority 的规则顺序不再依赖同一事务里相同的 created_at。
--   - 节点是软删（deleted_at），外键 CASCADE 只在硬删时生效；软删节点留下的路由行与成员行不进任何读
--     （成员列表、悬空引用检查、受影响节点数都按 nodes.deleted_at IS NULL 过滤），也不会下发（节点已退役）。
--   - 没有 config_source_generation 与发布锁：配置纪元推进后会话层现算配置文档并按摘要重发。

-- +goose Up

CREATE TABLE route_groups (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  name        text NOT NULL CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 64),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  -- 组间生效顺序：小的先生效，同值按 id（uuidv7，即创建先后）
  sort_order  integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -1000000 AND 1000000),
  -- 组的元信息、出站与规则、成员任一变化都推进它，后台写接口拿它做乐观并发
  row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX route_groups_name_key ON route_groups (lower(name));

-- 成员：一个节点可在多个组里，组间顺序取 route_groups.sort_order。只整体替换（删旧插新）。
CREATE TABLE route_group_members (
  group_id   uuid NOT NULL REFERENCES route_groups(id) ON DELETE CASCADE,
  node_id    uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, node_id)
);

-- 生效合并与节点详情按节点反查所在组
CREATE INDEX route_group_members_node_idx ON route_group_members (node_id);

CREATE TABLE node_outbounds (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  node_id    uuid REFERENCES nodes(id) ON DELETE CASCADE,
  group_id   uuid REFERENCES route_groups(id) ON DELETE CASCADE,
  -- tag 是规则引用出站的唯一凭据：引用按原样精确比较（与 pdnd 查表一致），重名按不分大小写拒绝
  tag        text NOT NULL CHECK (tag = btrim(tag) AND char_length(tag) BETWEEN 1 AND 64
                                  AND lower(tag) NOT IN ('direct', 'block')),
  type       text NOT NULL CHECK (type IN (
               'direct', 'block',
               'socks', 'http', 'shadowsocks', 'vmess', 'vless', 'trojan',
               'hysteria', 'hysteria2', 'tuic', 'anytls', 'shadowtls', 'wireguard')),
  -- 协议特有参数，面板不解释，原样下发
  settings   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT node_outbounds_scope_check CHECK (node_id IS NULL OR group_id IS NULL)
);

-- 三个范围各自一个唯一索引：NULL 在唯一索引里互不相等，(node_id, group_id, tag) 一个索引挡不住全局重名
CREATE UNIQUE INDEX node_outbounds_global_tag_key ON node_outbounds (lower(tag))
  WHERE node_id IS NULL AND group_id IS NULL;
CREATE UNIQUE INDEX node_outbounds_group_tag_key ON node_outbounds (group_id, lower(tag))
  WHERE group_id IS NOT NULL;
CREATE UNIQUE INDEX node_outbounds_node_tag_key ON node_outbounds (node_id, lower(tag))
  WHERE node_id IS NOT NULL;

CREATE TABLE node_routes (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  node_id      uuid REFERENCES nodes(id) ON DELETE CASCADE,
  group_id     uuid REFERENCES route_groups(id) ON DELETE CASCADE,
  -- priority 小的先匹配；sort_order 是保存时按 priority 稳定排序后的下标
  priority     integer NOT NULL CHECK (priority >= 0),
  sort_order   integer NOT NULL CHECK (sort_order >= 0),
  -- 形如 {"domain_suffix":["example.com"],"port":[443]}；空对象是无条件匹配（兜底）
  matcher      jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(matcher) = 'object'),
  -- 内置出站在保存时规范成小写 direct / block，自定义出站原样
  outbound_tag text NOT NULL CHECK (outbound_tag = btrim(outbound_tag) AND char_length(outbound_tag) BETWEEN 1 AND 64),
  enabled      boolean NOT NULL DEFAULT true,
  note         text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT node_routes_scope_check CHECK (node_id IS NULL OR group_id IS NULL)
);

CREATE INDEX node_routes_node_idx ON node_routes (node_id) WHERE node_id IS NOT NULL;
CREATE INDEX node_routes_group_idx ON node_routes (group_id) WHERE group_id IS NOT NULL;
CREATE INDEX node_routes_global_idx ON node_routes (priority, sort_order) WHERE node_id IS NULL AND group_id IS NULL;

-- +goose Down

DROP TABLE node_routes;
DROP TABLE node_outbounds;
DROP TABLE route_group_members;
DROP TABLE route_groups;
