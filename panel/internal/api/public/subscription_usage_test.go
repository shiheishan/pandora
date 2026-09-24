// [INPUT]: 读 router.go 与 subscription_usage.go 的源码，依赖 parseUsageDays 与 platform/httpx 的错误码
// [OUTPUT]: 对外提供 TestParseUsageDays、TestSubscriptionUsageRouteContract
// [POS]: api/public 按日用量接口的单元与源码契约：days 取值边界、路由在登录分组内且不挂幂等、404 中性出口与契约字段名
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestParseUsageDays(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{{"", 0}, {"1", 1}, {"30", 30}, {"93", 93}} {
		got, err := parseUsageDays(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parseUsageDays(%q) = %d, %v; want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"0", "-1", "94", "abc", "7.5"} {
		_, err := parseUsageDays(raw)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["days"] == "" {
			t.Errorf("parseUsageDays(%q) err = %v, want a validation error on days", raw, err)
		}
	}
}

func TestSubscriptionUsageRouteContract(t *testing.T) {
	b, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	router := string(b)
	route := `r.Get("/me/subscriptions/{id}/usage", h.meSubscriptionUsage)`
	at := strings.Index(router, route)
	auth := strings.Index(router, "middleware.RequireAuth(d.Log)")
	if at < 0 || auth < 0 || at < auth {
		t.Fatal("usage route must be registered inside the signed-in group")
	}
	if strings.Contains(router[max(at-160, 0):at], "Idempotency") {
		t.Fatal("usage route is a read and must not require an idempotency key")
	}

	h, err := os.ReadFile("subscription_usage.go")
	if err != nil {
		t.Fatal(err)
	}
	handler := string(h)
	for _, want := range []string{
		`errors.Is(err, subscription.ErrNotFound)`,
		`httpx.NotFoundOrForbidden()`,
		`w.Header().Set("Cache-Control", "no-store")`,
		`"timezone"`, `"period_start"`, `"period_end"`, `"days"`,
		`"today_bytes"`, `"avg_daily_bytes"`, `json:"date"`, `json:"bytes"`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("usage handler contract missing %q", want)
		}
	}
}
