package public

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/routelogtest"
)

// 订阅令牌与 Telegram 回调 secret 是路径段：门户网关的任何日志（访问日志、Fail、
// Recovery、限流拒绝）都只许写路由模板。全部路由逐条打一次，新路由自动覆盖。
func TestPublicLogsNeverContainRawPath(t *testing.T) {
	res := routelogtest.Check(t, func(log *slog.Logger, rdb *redis.Client) http.Handler {
		return NewRouter(Deps{
			Cfg:   &config.Config{RateLimitPerIPPerMinute: 120, RateLimitAuthPerMinute: 10},
			Log:   log,
			Redis: rdb,
		})
	})
	// 含秘密的两条必须在被检查的路由里，日志里也要看得到它们的模板（证明真打到了）
	for _, route := range []string{"GET /{prefix}/{token}", "POST /v1/webhooks/telegram/{secret}"} {
		if !slices.Contains(res.Routes, route) {
			t.Fatalf("route %q was not exercised; routes: %v", route, res.Routes)
		}
		tpl := route[strings.IndexByte(route, ' ')+1:]
		if !strings.Contains(res.Logs, `"route":"`+tpl+`"`) &&
			!strings.Contains(res.Logs, `"route":"`+route+`"`) {
			t.Fatalf("no log line carries the template of %q", route)
		}
	}
}
