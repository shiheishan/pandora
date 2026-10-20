package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// 真库里跑一轮巡检：属主检查用 fakePorts，integrity_checks 的锁行、upsert、列表与库约束用真的。
// 本表只有本包写，用例在回滚事务里，不会留下行。
func TestRunIntegrityPG(t *testing.T) {
	e, ctx := newPGEnv(t)
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	e.svc.now = func() time.Time { return t0 }
	e.ports.checks["CheckDrift"] = integrity.Result{Count: 2, Sample: []string{"a2", "a1"}}
	if err := e.svc.RunIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.ports.alerts) != 1 || e.ports.alerts[0].Code != CheckLedgerDrift {
		t.Fatalf("应告警一次 %+v", e.ports.alerts)
	}

	var count, alerted int64
	var sample string
	var since *time.Time
	if err := e.row(ctx, `SELECT count, sample::text, failing_since, alerted_count FROM integrity_checks WHERE code = ?`,
		CheckLedgerDrift).Scan(&count, &sample, &since, &alerted); err != nil {
		t.Fatal(err)
	}
	if count != 2 || sample != `["a2", "a1"]` || since == nil || !since.Equal(t0) || alerted != 2 {
		t.Fatalf("count=%d sample=%s since=%v alerted=%d", count, sample, since, alerted)
	}

	items, err := e.svc.integrityItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(e.svc.integrityChecks()) || items[0].Code != CheckLedgerDrift || items[0].Count != 2 || !items[0].CheckedAt.Equal(t0) {
		t.Fatalf("待办读到 %+v", items)
	}

	// 回到 0：同一行被覆盖，起点与告警时的数清空
	t1 := t0.Add(30 * time.Minute)
	e.svc.now = func() time.Time { return t1 }
	e.ports.checks["CheckDrift"] = integrity.Result{Sample: []string{}}
	if err := e.svc.RunIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM integrity_checks
		WHERE code = ? AND count = 0 AND sample = '[]'::jsonb AND failing_since IS NULL AND alerted_count = 0 AND checked_at = ?`,
		CheckLedgerDrift, t1).Scan(&n); err != nil || n != 1 {
		t.Fatalf("回到 0 后的行 n=%d err=%v", n, err)
	}
	if len(e.ports.alerts) != 1 {
		t.Fatalf("回到 0 不该发 %+v", e.ports.alerts)
	}
}

// 库约束拦住不成形的行。
func TestIntegrityChecksConstraintsPG(t *testing.T) {
	e, ctx := newPGEnv(t)
	bad := []struct {
		name, sql string
	}{
		{"代号格式", `INSERT INTO integrity_checks (code, count, checked_at) VALUES ('Bad-Code', 0, now())`},
		{"负数", `INSERT INTO integrity_checks (code, count, checked_at) VALUES ('test_neg', -1, now())`},
		{"0 却有起点", `INSERT INTO integrity_checks (code, count, checked_at, failing_since) VALUES ('test_a', 0, now(), now())`},
		{"非 0 没起点", `INSERT INTO integrity_checks (code, count, checked_at) VALUES ('test_b', 1, now())`},
		{"0 却记告警数", `INSERT INTO integrity_checks (code, count, checked_at, alerted_count) VALUES ('test_c', 0, now(), 1)`},
		{"0 却有样例", `INSERT INTO integrity_checks (code, count, checked_at, sample) VALUES ('test_d', 0, now(), '["x"]')`},
		{"样例比违例多", `INSERT INTO integrity_checks (code, count, checked_at, failing_since, sample) VALUES ('test_e', 1, now(), now(), '["x","y"]')`},
		{"样例超 5 个", `INSERT INTO integrity_checks (code, count, checked_at, failing_since, sample) VALUES ('test_f', 9, now(), now(), '["1","2","3","4","5","6"]')`},
		{"样例不是数组", `INSERT INTO integrity_checks (code, count, checked_at, failing_since, sample) VALUES ('test_g', 1, now(), now(), '{}')`},
		{"起点晚于检查时间", `INSERT INTO integrity_checks (code, count, checked_at, failing_since) VALUES ('test_h', 1, now(), now() + interval '1 second')`},
	}
	for _, b := range bad {
		if err := e.exec(ctx, "SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
		err := e.exec(ctx, b.sql)
		if testkit.SQLState(err) != "23514" {
			t.Fatalf("%s 应被约束拦下，err=%v", b.name, err)
		}
		if err := e.exec(ctx, "ROLLBACK TO SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.exec(ctx, `INSERT INTO integrity_checks (code, count, checked_at, failing_since, sample, alerted_count)
		VALUES ('test_ok', 3, now(), now(), '["x"]', 3)`); err != nil {
		t.Fatalf("成形的行应能写 %v", err)
	}
}
