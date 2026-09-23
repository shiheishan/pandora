package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/platform/config"
)

// configDomainAdmin 把域常量固定在本包，避免 handlers 里散落字符串字面量。
const configDomainAdmin = config.DomainAdmin

// timeoutCtx 在请求上下文之上再加一层超时。
// 直接用 r.Context() 会继承网关的整体超时，而就绪检查这类探针
// 需要一个更短的独立预算，否则数据库卡住时探针也跟着卡满 25 秒。
func timeoutCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
