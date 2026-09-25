// [INPUT]: 依赖 platform/pg18test 打开 notify 域的一次性库，依赖 notify.go 的 Enqueue 与 Dispatch
// [OUTPUT]: 对外提供 TestDispatchHoldsEmailWhileSwitchedOffPG18
// [POS]: domain/notify 的 PG18 测试：notify.email 降级开关关闭时派发跳过邮件渠道、邮件留在队列，站内信照常；恢复后邮件继续派发
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package notify

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestDispatchHoldsEmailWhileSwitchedOffPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant = "86000000-0000-4000-8000-000000000101"
		user   = "86000000-0000-4000-8000-000000000111"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'notify-switch-pg18','Notify Switch','CNY')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,'u@notify-switch.invalid','U','active')`, user, tenant)
	must(`INSERT INTO notification_templates(tenant_id,code,channel,subject,body,category,status) VALUES
		($1,'switch.test','email','s','b','transactional','active'),
		($1,'switch.test','inapp',NULL,'b','transactional','active')`, tenant)
	// 开关行由建租户触发器种下（开启），这里关掉它
	must(`UPDATE feature_switches SET enabled=false, reason='SMTP 被封'
		WHERE tenant_id=$1 AND code='notify.email'`, tenant)

	// 没有注册任何邮件发信器：开关开着时邮件会被标成 suppressed（渠道未配置），
	// 关着时根本不会被取出，仍是 queued —— 状态本身就能区分两条路径
	svc := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("notify-switch-salt"))
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		return svc.Enqueue(ctx, tx, tenant, user, "switch.test", map[string]string{}, "switch-test")
	}); err != nil {
		t.Fatal(err)
	}
	status := func(channel string) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT status FROM notification_deliveries
			WHERE tenant_id=$1 AND template_code='switch.test' AND channel=$2`, tenant, channel).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	if n, err := svc.Dispatch(ctx, tenant, 50); err != nil || n != 1 {
		t.Fatalf("dispatch with email off: n=%d err=%v, want only the in-app delivery", n, err)
	}
	if email, inapp := status("email"), status("inapp"); email != "queued" || inapp != "sent" {
		t.Fatalf("email off: email=%s inapp=%s, want queued / sent", email, inapp)
	}

	must(`UPDATE feature_switches SET enabled=true, reason=NULL WHERE tenant_id=$1 AND code='notify.email'`, tenant)
	if n, err := svc.Dispatch(ctx, tenant, 50); err != nil || n != 1 {
		t.Fatalf("dispatch with email back on: n=%d err=%v", n, err)
	}
	if email := status("email"); email != "suppressed" {
		t.Fatalf("email on: email=%s, want picked up (suppressed: no sender configured)", email)
	}
}
