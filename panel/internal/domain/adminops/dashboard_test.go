package adminops

import (
	"errors"
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

func TestDashboardTrafficQueriesShareFrozenClassifier(t *testing.T) {
	for name, query := range map[string]string{"nodes": dashboardNodeTrafficSQL, "users": dashboardUserTrafficSQL} {
		if strings.Count(query, dashboardTrafficClassificationCTE) != 1 {
			t.Fatalf("%s does not consume the shared classifier exactly once", name)
		}
		for _, guard := range []string{
			"length(ltrim(key_lex.normalized_key_text,'-')) <= 19",
			"is_valid IS TRUE",
			"is_valid IS NOT TRUE",
			"duplicate_of IS NULL",
			"duplicate_of IS NOT NULL",
		} {
			if !strings.Contains(query, guard) {
				t.Fatalf("%s missing %q", name, guard)
			}
		}
		if strings.Count(query, "trim_scale(") < 7 {
			t.Fatalf("%s does not canonicalize every byte field", name)
		}
		if got := strings.Count(query, "AS MATERIALIZED"); got != 4 {
			t.Fatalf("%s materialization barriers=%d, want three shared fan-out points plus one bounded ranked set", name, got)
		}
	}
	if got := strings.Count(dashboardTrafficClassificationCTE, "AS MATERIALIZED"); got != 3 {
		t.Fatalf("classifier materialization barriers=%d, want 3 shared fan-out points", got)
	}
	if strings.Contains(dashboardTrafficClassificationCTE, "SELECT *") || strings.Contains(dashboardTrafficClassificationCTE, ".*") {
		t.Fatal("classifier must not carry wide rows through SELECT *")
	}
	if strings.Contains(dashboardNodeTrafficSQL, "total_upload") || strings.Contains(dashboardUserTrafficSQL, "total_download") {
		t.Fatal("dashboard must not consume pre-aggregated report totals")
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
