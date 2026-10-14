package service

// 拉取日志的 PG 集成测试：保留期清理、只追加、库函数的截止下限与批量范围。

import (
	"testing"
)

func TestPurgeFetchLogs(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	for range 3 {
		de.exec(t, ctx, `INSERT INTO subscription_fetch_log (ua_family, result, fetched_at)
			VALUES ('purge-test-old', 'not_found', now() - interval '40 days')`)
	}
	for range 2 {
		de.exec(t, ctx, `INSERT INTO subscription_fetch_log (ua_family, result, fetched_at)
			VALUES ('purge-test-new', 'not_found', now() - interval '10 days')`)
	}
	const oldQ = `SELECT count(*) FROM subscription_fetch_log WHERE fetched_at < now() - interval '31 days'`
	before := countRows(t, de.env, ctx, oldQ)
	if before < 3 || before > fetchLogPurgeBatch {
		t.Fatalf("库里超过 31 天的行 %d，不在本用例假设的范围", before)
	}
	n, err := de.svc.PurgeFetchLogs(ctx)
	if err != nil {
		t.Fatalf("第一次清理: %v", err)
	}
	if n != int64(before) {
		t.Fatalf("删了 %d 行，想要 %d", n, before)
	}
	if left := countRows(t, de.env, ctx, oldQ); left != 0 {
		t.Fatalf("还剩 %d 行超过 31 天", left)
	}
	if kept := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE ua_family = 'purge-test-new'`); kept != 2 {
		t.Fatalf("10 天前的行剩 %d，想要 2", kept)
	}
	if n, err := de.svc.PurgeFetchLogs(ctx); err != nil || n != 0 {
		t.Fatalf("第二次清理: n=%d err=%v", n, err)
	}
}

func TestFetchLogAppendOnly(t *testing.T) {
	de := newDeliveryEnv(t, true)
	for name, q := range map[string]string{
		"改": `UPDATE subscription_fetch_log SET result = 'ok'`,
		"删": `DELETE FROM subscription_fetch_log`,
	} {
		// 报错会让事务作废，各用一个
		ctx := de.txCtx()
		de.exec(t, ctx, `INSERT INTO subscription_fetch_log (ua_family, result) VALUES ('append-only', 'not_found')`)
		if err := de.rawExec(ctx, q); err == nil {
			t.Fatalf("%s 拉取日志应被拒绝", name)
		}
	}
}

func TestPurgeFunctionFloor(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	de.exec(t, ctx, `INSERT INTO subscription_fetch_log (ua_family, result, fetched_at)
		VALUES ('purge-floor', 'not_found', now() - interval '10 days')`)
	before := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE fetched_at < now() - interval '31 days'`)
	var n int
	// 截止时间传「现在」也只删 31 天以前的
	if err := de.row(ctx, `SELECT purge_subscription_fetch_log(now(), 10000)`).Scan(&n); err != nil {
		t.Fatalf("调库函数: %v", err)
	}
	if n != before {
		t.Fatalf("删了 %d 行，想要 %d（只该删 31 天以前的）", n, before)
	}
	if kept := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE ua_family = 'purge-floor'`); kept != 1 {
		t.Fatalf("10 天前的行剩 %d，想要 1", kept)
	}

	bad := de.txCtx()
	if err := de.row(bad, `SELECT purge_subscription_fetch_log(now(), 0)`).Scan(&n); err == nil {
		t.Fatal("每批 0 行应报错")
	}
}
