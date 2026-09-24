// [INPUT]: 依赖 platform/pg18test 打开 public_api 域的一次性库，依赖 notifications.go 的 setNotificationPreference，依赖迁移 00076 的触发器状态
// [OUTPUT]: 对外提供 TestNotificationPreferencePG18、TestQuotaBalancesNotBroadcastPG18
// [POS]: api/public 的 PG18 测试：门户通知偏好可以反复保存（缺陷 7），流量余额的写入不再向全租户广播（缺陷 15）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

var publicAPIFixture = pg18test.Fixture{
	Domain: "PUBLIC_API", DatabasePrefix: "pandora_public_api_",
	MarkerTable: "pandora_public_api_test_marker", CommentTag: "pandora-public-api-pg18",
}

func TestNotificationPreferencePG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)

	const (
		tenant = "7b000000-0000-4000-8000-000000000001"
		user   = "7b000000-0000-4000-8000-000000000011"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','notify-pref-pg18','Notify Pref','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + user + `','` + tenant + `','pref@notify.invalid','Pref','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed preference fixture: %v", err)
		}
	}

	h := &handlers{d: Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/me/notification-preferences", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c := httpx.WithTenantID(ctx, tenant)
		c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: user, TenantID: tenant})
		w := httptest.NewRecorder()
		h.setNotificationPreference(w, req.WithContext(c))
		return w
	}
	enabledOf := func() bool {
		var on bool
		if err := admin.QueryRow(ctx, `SELECT enabled FROM notification_preferences
			WHERE user_id=$1 AND category='marketing' AND channel='email'`, user).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}

	// 第一次是插入，第二次撞主键走更新：原先 ON CONFLICT 的目标与主键不符，
	// 两次都会被 PostgreSQL 拒成 500
	if w := put(`{"category":"marketing","channel":"email","enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("first save: status=%d body=%s", w.Code, w.Body.String())
	}
	if enabledOf() {
		t.Fatal("first save did not store enabled=false")
	}
	if w := put(`{"category":"marketing","channel":"email","enabled":true}`); w.Code != http.StatusOK {
		t.Fatalf("second save: status=%d body=%s", w.Code, w.Body.String())
	}
	if !enabledOf() {
		t.Fatal("second save did not flip enabled back to true")
	}
	// 反向：交易类仍然关不掉
	if w := put(`{"category":"transactional","channel":"email","enabled":false}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disabling transactional: status=%d, want 422", w.Code)
	}
}

func TestQuotaBalancesNotBroadcastPG18(t *testing.T) {
	ctx, admin, _ := pg18test.Open(t, publicAPIFixture)
	triggerOn := func(table string) bool {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
			WHERE tgrelid = to_regclass('public.'||$1) AND tgname = 'zz_notify_'||$1 AND NOT tgisinternal`,
			table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	// 流量余额没有 user_id，挂着通知就是给全租户的高频广播（迁移 00076 摘掉）
	if triggerOn("quota_balances") {
		t.Fatal("quota_balances still carries the change-notify trigger")
	}
	// 订阅本身的变更仍要推给本人
	for _, table := range []string{"subscriptions", "subscription_credentials"} {
		if !triggerOn(table) {
			t.Fatalf("%s lost its change-notify trigger", table)
		}
	}
}
