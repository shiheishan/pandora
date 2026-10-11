package admin

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/routelogtest"
)

// 后台网关的日志只写路由模板，不写原始路径（守卫见 routelogtest）。
func TestAdminLogsNeverContainRawPath(t *testing.T) {
	routelogtest.Check(t, func(log *slog.Logger, rdb *redis.Client) http.Handler {
		return NewRouter(Deps{Cfg: &config.Config{RateLimitAuthPerMinute: 10}, Log: log, Redis: rdb})
	})
}
