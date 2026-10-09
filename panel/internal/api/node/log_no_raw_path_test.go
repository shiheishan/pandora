package node

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/routelogtest"
)

// 节点网关的日志只写路由模板，不写原始路径（守卫见 routelogtest）。
// enrollmentID 是服务端发的接入流水号、不是凭证（请求靠接入密钥签名），验签失败时有意
// 作为 enrollment_id 字段写进日志便于排障。
func TestNodeLogsNeverContainRawPath(t *testing.T) {
	routelogtest.Check(t, func(log *slog.Logger, _ *redis.Client) http.Handler {
		return NewRouter(Deps{Cfg: &config.Config{}, Log: log})
	}, routelogtest.PublicParam("enrollmentID"))
}
