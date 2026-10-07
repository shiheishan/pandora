package subscription

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// fetchLogRetentionScenario 证明订阅拉取日志的 31 天清理（00131 重写的 app.purge_subscription_fetch_log）：
//   - PurgeFetchLog 以运行角色删本租户 31 天以前的记录，近期的与别的租户的不动；
//   - 00018 那个不限租户、会被追加写触发器拦住的 (interval) 旧签名已不存在；
//   - 追加写保护不变：运行角色直接 DELETE 仍被拒；函数拒收短于 31 天的保留期与无租户作用域。
//
// 挂在 TestUsageDailyReadPG18 下（usage_daily 域的库套了 configure-app-role）。夹具前缀 75f8…。
func fetchLogRetentionScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	const (
		tenantA = "75f80000-0000-7000-8000-000000000001"
		tenantB = "75f80000-0000-7000-8000-000000000002"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'retain-fetch-a-pg18', 'Retain Fetch A', 'CNY'),
		       ($2, 'retain-fetch-b-pg18', 'Retain Fetch B', 'CNY')`, tenantA, tenantB)
	must(`INSERT INTO subscription_fetch_log (tenant_id, result, fetched_at)
		VALUES ($1, 'ok', now() - interval '40 days'),
		       ($1, 'not_found', now() - interval '32 days'),
		       ($1, 'ok', now() - interval '30 days'),
		       ($1, 'ok', now() - interval '1 hour'),
		       ($2, 'ok', now() - interval '40 days')`, tenantA, tenantB)

	var oldSignature bool
	if err := admin.QueryRow(ctx,
		`SELECT to_regprocedure('app.purge_subscription_fetch_log(interval)') IS NOT NULL`).Scan(&oldSignature); err != nil || oldSignature {
		t.Fatalf("cross-tenant purge_subscription_fetch_log(interval) still exists=%v err=%v", oldSignature, err)
	}

	if n, err := PurgeFetchLog(ctx, app, tenantA); err != nil || n != 2 {
		t.Fatalf("PurgeFetchLog deleted=%d err=%v, want the two rows older than 31 days", n, err)
	}
	var leftA, leftB int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2)
		  FROM subscription_fetch_log WHERE tenant_id IN ($1, $2)`, tenantA, tenantB).Scan(&leftA, &leftB); err != nil {
		t.Fatal(err)
	}
	if leftA != 2 || leftB != 1 {
		t.Fatalf("fetch log left tenantA=%d tenantB=%d, want two recent rows and the other tenant untouched", leftA, leftB)
	}
	for name, sql := range map[string]string{
		"direct delete":   `DELETE FROM subscription_fetch_log`,
		"short retention": `SELECT app.purge_subscription_fetch_log(30, 10)`,
		"zero batch":      `SELECT app.purge_subscription_fetch_log(31, 0)`,
	} {
		err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql)
			return err
		})
		if err == nil {
			t.Fatalf("%s on subscription_fetch_log was accepted for the app role", name)
		}
	}
	if _, err := app.Exec(ctx, `SELECT app.purge_subscription_fetch_log(31, 10)`); err == nil {
		t.Fatal("purge_subscription_fetch_log ran without a tenant scope")
	}
	t.Log("marker=retain_pg18_fetch_log_purged_ok")
}
