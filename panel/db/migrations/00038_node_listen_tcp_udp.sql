-- +goose Up
-- Shadowsocks 同端口开 TCP+UDP（用户 2026-10-14 定）：nodes.listen_l4 增加取值 tcp+udp，
-- 端口兜底索引按 L4 拆成 TCP、UDP 两条部分唯一索引，存量 shadowsocks 节点改成 tcp+udp。属 node 模块。
--
-- Go 侧 protocol.ListenL4 对 shadowsocks / ss 无条件返回 tcp+udp；门禁 claimNodePort 先查冲突并报占用者名字，这里兜底。
-- 锁与耗时（本迁移整体在一个事务里，锁持到提交）：
--   nodes 换 CHECK 与 DROP/CREATE 两条部分唯一索引均在 ACCESS EXCLUSIVE 下整表校验一遍，挡住节点的读写；
--   nodes 是后台手工维护的小表（每台服务器几十个节点），毫秒级。
--   预检：SELECT count(*) FROM nodes; 以及下面第 2 步 DO 块里的冲突查询（有行就先改其中一个节点的端口）。
--   失败回滚：任一步失败（含 lock_timeout 超时、预检 RAISE、UPDATE 撞上新唯一索引 23505）整笔回滚，库保持原样。
-- 还没有生产部署（用户 10-09 确认）；迁移失败即说明库里有同一服务器、同一端口上未退役的 shadowsocks 节点和 UDP 节点，
-- 需要先改其中一个的端口再重跑。
-- 先建索引、后改数据：存量 shadowsocks 改成 tcp+udp 时若与同端口 UDP 节点冲突，会被新的 UDP 唯一索引当场拒绝（23505），
-- 整个迁移回滚，不会留下违反门禁的数据；第 2 步预检只是把这种情况提前报成看得懂的错误。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM nodes s JOIN nodes u
      ON u.server_id = s.server_id AND u.server_port = s.server_port AND u.id <> s.id
     WHERE s.node_type IN ('shadowsocks', 'ss') AND s.server_port IS NOT NULL
       AND s.server_id IS NOT NULL AND s.serving_status <> 'retired'
       AND u.listen_l4 = 'udp' AND u.serving_status <> 'retired'
  ) THEN
    RAISE EXCEPTION '同一服务器同一端口上有未退役的 shadowsocks 节点与 UDP 节点；shadowsocks 改为同端口开 TCP+UDP 后两者冲突，先改其中一个节点的端口再迁移';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE nodes DROP CONSTRAINT nodes_listen_l4_check;
ALTER TABLE nodes ADD CONSTRAINT nodes_listen_l4_check CHECK (listen_l4 IN ('tcp', 'udp', 'tcp+udp'));

-- 同机端口门禁按 L4 分两条；tcp+udp 的行同时进两条，与同端口的 tcp、udp 都冲突。Go 先查并给出占用者名字，这里兜底
DROP INDEX nodes_listen_claim_key;
CREATE UNIQUE INDEX nodes_listen_tcp_claim_key ON nodes (server_id, server_port)
  WHERE listen_l4 IN ('tcp', 'tcp+udp') AND serving_status <> 'retired' AND server_id IS NOT NULL;
CREATE UNIQUE INDEX nodes_listen_udp_claim_key ON nodes (server_id, server_port)
  WHERE listen_l4 IN ('udp', 'tcp+udp') AND serving_status <> 'retired' AND server_id IS NOT NULL;

UPDATE nodes SET listen_l4 = 'tcp+udp'
 WHERE node_type IN ('shadowsocks', 'ss') AND server_port IS NOT NULL AND listen_l4 <> 'tcp+udp';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DROP INDEX nodes_listen_udp_claim_key;
DROP INDEX nodes_listen_tcp_claim_key;

-- 新结构下 tcp+udp 与同端口 tcp 不能共存，改回 tcp 不会撞旧索引
UPDATE nodes SET listen_l4 = 'tcp' WHERE listen_l4 = 'tcp+udp';

ALTER TABLE nodes DROP CONSTRAINT nodes_listen_l4_check;
ALTER TABLE nodes ADD CONSTRAINT nodes_listen_l4_check CHECK (listen_l4 IN ('tcp', 'udp'));

CREATE UNIQUE INDEX nodes_listen_claim_key ON nodes (server_id, server_port, listen_l4)
  WHERE serving_status <> 'retired' AND server_id IS NOT NULL;
