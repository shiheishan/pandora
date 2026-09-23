-- 清理孤儿表：迁移建了、Go 从不引用、库内也没有任何对象依赖的 21 张表。
--
-- 它们不是功能，只是早期 schema 设计的痕迹（SSO、两步验证、云编排、对账、旧计量、旧 webhook）。
-- 留着的代价是每个读 schema 的人都会以为这些能力已经实现。
--
-- 入选条件（写这份迁移时逐张核对，见 migrations/RESERVED-TABLES.md 的保留清单）：
--   · 非测试 Go、tests/invariants.sql、deploy/ 脚本与 configure-app-role.sql 都不引用；
--   · 没有保留下来的表用外键指向它；
--   · 迁移里没有函数、视图或其他表的策略读写它（自身的 RLS 策略与触发器随表删除）；
--   · 不在冻结契约里点名：risk_events、client_releases 被 CLIENT-AUTH-01 冻结契约引用，保留。
--
-- 不用 CASCADE：任何意料之外的依赖都应该让迁移失败，而不是被静默连带删除。
-- 顺序先子后父：user_identities → identity_providers，reconciliation_discrepancies →
-- reconciliation_runs，webhook_deliveries → webhook_endpoints。
-- usage_events 是按月分区表：删父表会带走全部月分区，DEFAULT 分区先显式删掉以便审阅。

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
-- 身份（00002）：企业 SSO、两步验证、通行密钥、组织成员均未实现
DROP TABLE user_identities;
DROP TABLE identity_providers;
DROP TABLE passkeys;
DROP TABLE totp_secrets;
DROP TABLE recovery_codes;
DROP TABLE organization_members;

-- 目录与订阅（00003）：订阅直接关联套餐版本，不用明细
DROP TABLE subscription_items;

-- 计费（00004）：支付争议与对账未实现
DROP TABLE disputes;
DROP TABLE reconciliation_discrepancies;
DROP TABLE reconciliation_runs;

-- 节点编排（00005）：云资源回收未实现
DROP TABLE orphan_resources;

-- 计量（00006）：计量走 node_traffic_reports / usage_batches / usage_sources。
-- 分区辅助函数只为 usage_events 服务、没有调用方，表删了它就是悬空对象，一起删
DROP TABLE usage_events_overflow;
DROP TABLE usage_events;
DROP FUNCTION app.ensure_usage_partition(date);
DROP TABLE usage_aggregates;
DROP TABLE usage_event_keys;

-- 运营（00008）：礼品卡真实实现在 00045，工单附件未实现
DROP TABLE gift_codes;
DROP TABLE ticket_attachments;

-- 安全审计（00009）：真实 webhook 在 00051 的 plugin_hooks / plugin_hook_deliveries，安全告警未实现
DROP TABLE webhook_deliveries;
DROP TABLE webhook_endpoints;
DROP TABLE security_alerts;

-- 节点协议（00013）：权限组真实实现用 node_pools / plan_node_pools
DROP TABLE node_group_members;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
  -- 这些表从未写入业务数据，但重建它们等于把已经删掉的误导重新放回来。
  -- 需要其中某项能力时，按真实设计写新迁移，不要回滚到空壳。
  RAISE EXCEPTION '00067 Down is not supported: orphan tables are not restored; write a forward migration instead';
END $$;
-- +goose StatementEnd
