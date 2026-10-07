package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// backlog 第 1 条：后台改自己密码与重认证同级，按账号、按 IP 两级认证限流。
func TestAdminChangePasswordHasAuthRateLimits(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("NewRouter")
	route := strings.Index(body, `.Post("/me/password", h.changePassword)`)
	if route < 0 {
		t.Fatal("POST v1/me/password route not found")
	}
	limit := strings.LastIndex(body[:route], "r.With(middleware.RateLimit(d.Redis, d.Log,")
	if limit < 0 {
		t.Fatal("POST v1/me/password must be wrapped in its own RateLimit")
	}
	chain := body[limit:route]
	for _, want := range []string{
		`middleware.ByAccount("adm_password", time.Minute, d.Cfg.RateLimitAuthPerMinute)`,
		`middleware.ByIP("adm_password_ip", time.Minute, d.Cfg.RateLimitAuthPerMinute)`,
	} {
		if !strings.Contains(chain, want) {
			t.Fatalf("POST v1/me/password rate limit missing %s", want)
		}
	}
	if strings.Count(chain, "middleware.") != 3 {
		t.Fatalf("unexpected middleware between the limiter and the route: %s", chain)
	}
}
