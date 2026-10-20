-- +goose Up
-- 节点在线升级的任务表（用户 10-20 定：后台分批升级、连不上面板自动装回旧版、核对发版签名；规格由主 Agent 10-20 定）。
-- 属主 node：只有 node 的 repo 读写。一行是「让一台服务器升级到某一版」的一次任务，id 即下发给节点的 job_id。
--   状态：sent（已建、已下发或正在下发）→ staged（节点已下好、验过签，交给 root 侧助手）→ 结束态
--   succeeded（新 Hello 的 binary_sha256 等于目标）/ failed（节点或面板报失败）/ rolled_back（新版没连上面板，已装回旧版）/
--   timed_out（过了 deadline_at 仍没结论，由任务 node:upgrade_watch 置）。状态机在 Go，状态更新带旧状态条件。
--   每台服务器至多一条未结束（sent / staged）的任务：部分唯一索引兜底，建任务用 ON CONFLICT DO NOTHING。
-- 锁与耗时：只新建表与索引，瞬间完成；对 servers 的外键只在建表时取 SHARE ROW EXCLUSIVE（瞬间，不扫表）。
--   预检：无（新表）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00059，重试即可。
-- 保留期：任务很少（每次发版每台一条），先不清理；量起来再加进 retention:node。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE server_upgrades (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  server_id      uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  -- 目标：取自发布清单 pdnd-manifest.json（version、serial、本机架构的 sha256）
  target_version text NOT NULL CHECK (char_length(target_version) BETWEEN 1 AND 64),
  target_serial  bigint NOT NULL CHECK (target_serial > 0),
  target_sha256  text NOT NULL CHECK (target_sha256 ~ '^[0-9a-f]{64}$'),
  -- 发起时服务器上的版本（servers.agent_version / binary_sha256 的快照，节点自报，可能未知）
  from_version   text CHECK (char_length(from_version) BETWEEN 1 AND 64),
  from_sha256    text CHECK (from_sha256 ~ '^[0-9a-f]{64}$'),
  status         text NOT NULL CHECK (status IN ('sent', 'staged', 'succeeded', 'failed', 'rolled_back', 'timed_out')),
  -- 原因码：节点回报的（download_failed、confirm_timeout…）或面板的（not_connected），失败与回退必有
  reason_code    text CHECK (char_length(reason_code) BETWEEN 1 AND 64),
  -- 节点回报的详情，面板截到 1 KiB
  detail         text CHECK (octet_length(detail) <= 1024),
  created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  -- 排进会话发送队列的时刻；没排上（会话已断）就留空
  sent_at        timestamptz,
  staged_at      timestamptz,
  finished_at    timestamptz,
  -- 过了它仍未结束就判超时：下发时刻 + 确认时限 180 秒 + 10 分钟（下载与替换）
  deadline_at    timestamptz NOT NULL,
  -- 结束态才有 finished_at
  CONSTRAINT server_upgrades_finished_shape CHECK (
    (status IN ('succeeded', 'failed', 'rolled_back', 'timed_out')) = (finished_at IS NOT NULL)),
  CONSTRAINT server_upgrades_staged_shape CHECK (status <> 'staged' OR staged_at IS NOT NULL),
  CONSTRAINT server_upgrades_reason_shape CHECK (status NOT IN ('failed', 'rolled_back') OR reason_code IS NOT NULL),
  CONSTRAINT server_upgrades_deadline_order CHECK (deadline_at > created_at),
  CONSTRAINT server_upgrades_finished_order CHECK (finished_at IS NULL OR finished_at >= created_at)
);

-- 每台服务器至多一条未结束的任务
CREATE UNIQUE INDEX server_upgrades_open_key ON server_upgrades (server_id) WHERE status IN ('sent', 'staged');
-- 后台列表取每台最近一次任务（DISTINCT ON (server_id) … ORDER BY created_at DESC）
CREATE INDEX server_upgrades_server_created ON server_upgrades (server_id, created_at DESC);
-- 超时巡检只扫未结束的
CREATE INDEX server_upgrades_open_deadline ON server_upgrades (deadline_at) WHERE status IN ('sent', 'staged');

-- 授权不写在迁移里：安装与升级在 migrate up 之后跑 configure-app-role.sql，给 pandora_app 全部表的 DML。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 任务记录只是升级过程的留档，退回上一版时一并删掉（服务器上已装的版本不受影响）
DROP TABLE IF EXISTS server_upgrades;
