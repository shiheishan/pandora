-- +goose Up
-- 节点入站证书的状态（用户 2026-10-20 定：节点证书走 Let's Encrypt，节点端自己申请，状态随心跳回报）。属 node 模块。
--
-- 节点端（能力 cert_acme_v1）在心跳 NodeRuntime.cert 里报：标识符、到期时间、最近一次失败的原因码与详情、
-- 下次申请或续期的计划时间。只有 node 的 repo 读写，与 runtime_status 同一条写路径、只在变化时写；
-- 绑定 / 改绑 / 解绑服务器时与运行状态一起清空（上一台报的不算数）。
-- 自动申请（acme）的节点有标识符；手填证书（file）的只有到期时间。
-- 锁与耗时：nodes 上加 6 个可空、无缺省值的列与几条 CHECK。ADD COLUMN 只改目录、不重写表；
--   新列全为 NULL，CHECK 在同一事务里校验也只是扫一遍 nodes（节点表是几百到几千行的小表），瞬间完成。
--   要 nodes 的 ACCESS EXCLUSIVE 锁：心跳写运行状态与订阅读线路都会短暂排队，lock_timeout 5 秒内拿不到就整笔放弃。
--   预检：SELECT count(*) FROM nodes;（小表，无需 NOT VALID）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00060，换个时间重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE nodes
  ADD COLUMN cert_identifier text,
  ADD COLUMN cert_not_after timestamptz,
  ADD COLUMN cert_error_code text,
  ADD COLUMN cert_error_detail text,
  ADD COLUMN cert_next_attempt_at timestamptz,
  ADD COLUMN cert_state_at timestamptz,
  -- 域名或 IP（与配置文档的 cert_identifier 相同），DNS 名字最长 253
  ADD CONSTRAINT nodes_cert_identifier_shape CHECK (char_length(cert_identifier) BETWEEN 1 AND 253),
  -- 节点端的机器可读原因码（pdnd/certs 的原因表），小写字母、数字、下划线
  ADD CONSTRAINT nodes_cert_error_code_shape CHECK (cert_error_code ~ '^[a-z0-9_]{1,64}$'),
  -- 详情只跟着原因码出现，至多 1 KiB
  ADD CONSTRAINT nodes_cert_error_detail_shape CHECK (
    cert_error_detail IS NULL OR (cert_error_code IS NOT NULL AND octet_length(cert_error_detail) <= 1024)),
  -- 任一证书字段有值时必须记下报告时刻；全空时报告时刻也空
  ADD CONSTRAINT nodes_cert_state_shape CHECK (
    (cert_identifier IS NULL AND cert_not_after IS NULL AND cert_error_code IS NULL
      AND cert_error_detail IS NULL AND cert_next_attempt_at IS NULL) = (cert_state_at IS NULL));

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 证书状态只是节点端回报的现状，下一次心跳会重报；退回上一版时直接删掉，不需要数据守卫。
ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_cert_state_shape,
  DROP CONSTRAINT IF EXISTS nodes_cert_error_detail_shape,
  DROP CONSTRAINT IF EXISTS nodes_cert_error_code_shape,
  DROP CONSTRAINT IF EXISTS nodes_cert_identifier_shape,
  DROP COLUMN IF EXISTS cert_state_at,
  DROP COLUMN IF EXISTS cert_next_attempt_at,
  DROP COLUMN IF EXISTS cert_error_detail,
  DROP COLUMN IF EXISTS cert_error_code,
  DROP COLUMN IF EXISTS cert_not_after,
  DROP COLUMN IF EXISTS cert_identifier;
