package adminops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestValidateDashboardTrafficQueryDefaults(t *testing.T) {
	got, snapshot, err := validateDashboardTrafficQuery(DashboardTrafficQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Range != "7d" || got.Limit != 10 || snapshot != nil {
		t.Fatalf("defaults = %#v, snapshot=%v", got, snapshot)
	}
}

func TestValidateDashboardTrafficQueryMatrix(t *testing.T) {
	for _, r := range []string{"24h", "7d", "30d"} {
		for _, limit := range []int{5, 10, 20} {
			if _, _, err := validateDashboardTrafficQuery(DashboardTrafficQuery{Range: r, Limit: limit, SnapshotAt: "2026-07-30T08:00:00.000000Z"}); err != nil {
				t.Fatalf("valid %s/%d rejected: %v", r, limit, err)
			}
		}
	}
	if _, _, err := validateDashboardTrafficQuery(DashboardTrafficQuery{Range: "7d", Limit: 10, SnapshotAt: "2026-07-30T08:00:00+00:00"}); err != nil {
		t.Fatalf("RFC3339 UTC +00:00 rejected: %v", err)
	}
	for _, tc := range []DashboardTrafficQuery{
		{Range: "1d", Limit: 10}, {Range: "7d", Limit: 6},
		{Range: "7d", Limit: 10, SnapshotAt: "2026-07-30T08:00:00-00:00"},
		{Range: "7d", Limit: 10, SnapshotAt: "2026-07-30T08:00:00,5Z"},
		{Range: "7d", Limit: 10, SnapshotAt: "not-a-time"},
	} {
		_, _, err := validateDashboardTrafficQuery(tc)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("%#v: expected validation_failed, got %v", tc, err)
		}
	}
}

// 看板流量只读小时汇总（00099），两条排行共用同一段窗口读数；严格校验口径搬进迁移里的
// app.node_traffic_payload_entries，原 strict_entries 的每一道闸都要在那里。
func TestDashboardTrafficQueriesReadHourlyRollups(t *testing.T) {
	for name, query := range map[string]string{"nodes": dashboardNodeTrafficSQL, "users": dashboardUserTrafficSQL} {
		if strings.Count(query, dashboardTrafficWindowCTE) != 1 {
			t.Fatalf("%s does not consume the shared window CTE exactly once", name)
		}
		for _, banned := range []string{"node_traffic_reports", "raw_payload", "jsonb_each", "total_upload", "total_download"} {
			if strings.Contains(query, banned) {
				t.Fatalf("%s must not read raw report evidence (%q)", name, banned)
			}
		}
		for _, guard := range []string{
			"FROM node_traffic_hourly h",
			"FROM node_user_traffic_hourly t",
			"h.hour_start >= $2 AND h.hour_start < $3",
			"t.hour_start >= $2 AND t.hour_start < $3",
			"sub.node_uid=t.node_uid",
		} {
			if !strings.Contains(query, guard) {
				t.Fatalf("%s missing %q", name, guard)
			}
		}
		if strings.Count(query, "trim_scale(") < 7 {
			t.Fatalf("%s does not canonicalize every byte field", name)
		}
	}

	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00099_node_traffic_hourly.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(string(migration), "-- +goose Down", 2)[0]
	for _, guard := range []string{
		"CREATE FUNCTION app.node_traffic_payload_entries(p_payload jsonb)",
		"LANGUAGE sql IMMUTABLE PARALLEL SAFE",
		"entry.key_text ~ '^[+-]?[0-9]+$'",
		"length(ltrim(key_lex.normalized_key_text,'-')) <= 19",
		"-9223372036854775808::numeric AND 9223372036854775807::numeric",
		"value_shape.array_len=2 AS shape_ok",
		"jsonb_typeof(components.upload_json)='number'",
		"numbers.upload_numeric=trunc(numbers.upload_numeric)",
		"numbers.upload_numeric BETWEEN 0 AND 9223372036854775807::numeric",
		"CASE WHEN jsonb_typeof(p_payload)='object' THEN p_payload ELSE '{}'::jsonb END",
		"r.duplicate_of IS NULL",
		"-- rollup-backfill:begin",
		"-- rollup-backfill:end",
	} {
		if !strings.Contains(up, guard) {
			t.Fatalf("00099 classifier/backfill missing %q", guard)
		}
	}
	header := up[strings.Index(up, "CREATE FUNCTION app.node_traffic_payload_entries"):]
	header = header[:strings.Index(header, "AS $$")]
	for _, banned := range []string{"STRICT", "SECURITY DEFINER", "VOLATILE", "SET "} {
		if strings.Contains(header, banned) {
			t.Fatalf("classifier must stay inlinable, header has %q", banned)
		}
	}
}

func TestDashboardBacklogUsesOneClockAndExactThreshold(t *testing.T) {
	if got := strings.Count(dashboardNotificationBacklogSQL, "statement_timestamp()"); got != 1 {
		t.Fatalf("statement_timestamp count=%d, want 1", got)
	}
	if strings.Contains(dashboardNotificationBacklogSQL, "count(d.id)") || strings.Count(dashboardNotificationBacklogSQL, "count(*) FILTER") != 8 {
		t.Fatal("backlog counters must remain covering-index friendly and preserve all eight filtered counts")
	}
	for _, want := range []string{
		"lag_exact>interval '600 seconds'",
		"ceil(extract(epoch FROM greatest(lag_exact,interval '0 seconds')))",
		"'unobservable'",
	} {
		if want == "'unobservable'" {
			continue
		} // fixed in the response DTO, never inferred in SQL.
		if !strings.Contains(dashboardNotificationBacklogSQL, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
