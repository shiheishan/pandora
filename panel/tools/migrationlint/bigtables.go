package migrationlint

// BigTables 是「大表」清单：行数随用户数、节点数或时间线性增长，线上会到十万、百万行级。
// 在这些表上：
//   - 建索引用 CREATE INDEX CONCURRENTLY（文件带 -- +goose NO TRANSACTION），
//     普通建索引持 SHARE 锁，期间整表写入排队；
//   - 不做整表重写的 ALTER（改列类型、加带易变默认值的列、加存储型生成列），
//     确有必要就在文件头写 `-- rewrite: <表> rows=<行数> est=<预估耗时>`；
//   - 回填与批量改写分批、可重跑，文件头写 `-- backfill: batched-by=…; rerunnable=…`。
//
// 同一个迁移里刚建的表是空的，不受这几条约束。
// 新增一张会随业务量增长的表，在这里登记；拿不准就登记，误登记只是多写一行标记。
var BigTables = map[string]bool{
	// 用户与会话
	"users": true, "user_identities": true, "sessions": true, "refresh_tokens": true,
	"devices": true, "device_tokens": true, "verification_codes": true, "quick_login_tokens": true,
	// 订阅与交付
	"subscriptions": true, "subscription_items": true, "subscription_credentials": true,
	"subscription_events": true, "subscription_transitions": true,
	"subscription_fetch_log": true, "subscription_usage_daily": true,
	// 订单、支付与账务
	"orders": true, "order_items": true, "order_transitions": true,
	"payments": true, "payment_events": true, "payment_intents": true, "payment_webhook_receipts": true,
	"invoices": true, "ledger_transactions": true, "ledger_entries": true,
	"commission_entries": true, "referrals": true, "gift_card_codes": true,
	// 配额与用量
	"quota_balances": true, "quota_adjustments": true,
	"usage_events": true, "usage_event_keys": true, "usage_aggregates": true, "usage_batches": true,
	"traffic_pack_grants": true, "traffic_reset_logs": true, "activity_daily": true,
	// 节点上报与下发
	"node_metrics": true, "node_traffic_reports": true, "node_traffic_hourly": true,
	"node_user_traffic_daily": true, "node_user_traffic_hourly": true,
	"node_alive_ips": true, "node_request_nonces": true, "node_transitions": true,
	"node_config_applications": true,
	// 审计、风控、幂等与投递
	"audit_events": true, "credential_access_log": true, "risk_events": true,
	"idempotency_keys": true, "notification_deliveries": true,
	"webhook_deliveries": true, "plugin_hook_deliveries": true,
	// 工单
	"tickets": true, "ticket_messages": true,
}
