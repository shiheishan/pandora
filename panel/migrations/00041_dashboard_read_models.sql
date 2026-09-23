-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- DASH-01 narrows append-only traffic evidence before JSON expansion. Keep
-- duplicate quality on its own partial index so it never forces a table scan.
CREATE INDEX idx_node_traffic_reports_dashboard_window
  ON node_traffic_reports (tenant_id, received_at DESC, id)
  INCLUDE (node_id)
  WHERE duplicate_of IS NULL;

CREATE INDEX idx_node_traffic_reports_dashboard_duplicate_window
  ON node_traffic_reports (tenant_id, received_at DESC)
  WHERE duplicate_of IS NOT NULL;

-- The expression and predicate intentionally match the backlog query exactly.
CREATE INDEX idx_notification_deliveries_ready_expr
  ON notification_deliveries (
    tenant_id,
    (coalesce(next_retry_at, created_at))
  )
  INCLUDE (attempts)
  WHERE status = 'queued';

CREATE INDEX idx_notification_deliveries_health_status
  ON notification_deliveries (tenant_id, status, created_at DESC)
  INCLUDE (attempts, next_retry_at, sent_at);

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES (
  'ops.notification.read',
  'ops',
  '查看通知投递积压与健康聚合',
  false
)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, 'ops.notification.read'
FROM roles r
WHERE r.is_system
  AND r.code IN ('tenant_admin', 'platform_admin')
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM role_permissions
WHERE permission_code = 'ops.notification.read';

DELETE FROM permissions
WHERE code = 'ops.notification.read';

DROP INDEX IF EXISTS idx_notification_deliveries_health_status;
DROP INDEX IF EXISTS idx_notification_deliveries_ready_expr;
DROP INDEX IF EXISTS idx_node_traffic_reports_dashboard_duplicate_window;
DROP INDEX IF EXISTS idx_node_traffic_reports_dashboard_window;
