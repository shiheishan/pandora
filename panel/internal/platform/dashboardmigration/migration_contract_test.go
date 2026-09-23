package dashboardmigration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func dashboardMigration(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "migrations", "00041_dashboard_read_models.sql")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read dashboard migration: %v", err)
	}
	return strings.ToLower(regexp.MustCompile(`\s+`).ReplaceAllString(string(b), " "))
}

func requireClause(t *testing.T, sql, want string) {
	t.Helper()
	if !strings.Contains(sql, strings.ToLower(want)) {
		t.Fatalf("migration missing required clause:\n%s", want)
	}
}

func TestDashboardReadModelsMigrationContract(t *testing.T) {
	sql := dashboardMigration(t)

	for _, want := range []string{
		"create index idx_node_traffic_reports_dashboard_window on node_traffic_reports (tenant_id, received_at desc, id) include (node_id) where duplicate_of is null",
		"create index idx_node_traffic_reports_dashboard_duplicate_window on node_traffic_reports (tenant_id, received_at desc) where duplicate_of is not null",
		"create index idx_notification_deliveries_ready_expr on notification_deliveries ( tenant_id, (coalesce(next_retry_at, created_at)) ) include (attempts) where status = 'queued'",
		"create index idx_notification_deliveries_health_status on notification_deliveries (tenant_id, status, created_at desc) include (attempts, next_retry_at, sent_at)",
		"insert into permissions (code, domain, description, high_risk) values ( 'ops.notification.read', 'ops'",
		"on conflict (code) do nothing",
		"from roles r where r.is_system and r.code in ('tenant_admin', 'platform_admin')",
		"on conflict (role_id, permission_code) do nothing",
	} {
		requireClause(t, sql, want)
	}

	if strings.Contains(sql, "include (raw_payload)") || strings.Contains(sql, "raw_payload)") {
		t.Fatal("dashboard indexes must not include raw_payload")
	}
	if strings.Contains(sql, "node_uid::text") {
		t.Fatal("dashboard migration must not introduce node_uid::text lookup")
	}

	// Permissions must be removed before their dictionary row, and indexes are
	// dropped in reverse dependency/creation order so rollback is deterministic.
	down := strings.Index(sql, "-- +goose down")
	if down < 0 {
		t.Fatal("migration has no goose Down section")
	}
	for _, want := range []string{
		"delete from role_permissions where permission_code = 'ops.notification.read'",
		"delete from permissions where code = 'ops.notification.read'",
		"drop index if exists idx_notification_deliveries_health_status",
		"drop index if exists idx_notification_deliveries_ready_expr",
		"drop index if exists idx_node_traffic_reports_dashboard_duplicate_window",
		"drop index if exists idx_node_traffic_reports_dashboard_window",
	} {
		requireClause(t, sql[down:], want)
	}
	order := []string{
		"delete from role_permissions", "delete from permissions",
		"drop index if exists idx_notification_deliveries_health_status",
		"drop index if exists idx_notification_deliveries_ready_expr",
		"drop index if exists idx_node_traffic_reports_dashboard_duplicate_window",
		"drop index if exists idx_node_traffic_reports_dashboard_window",
	}
	last := -1
	for _, needle := range order {
		at := strings.Index(sql[down:], needle)
		if at < last {
			t.Fatalf("Down cleanup order is not deterministic at %q", needle)
		}
		last = at
	}
}
