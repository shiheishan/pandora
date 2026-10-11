package httpx

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// UnmatchedRoute 是没匹配上任何路由时日志里写的占位（与访问日志的「-」一致）。
const UnmatchedRoute = "-"

// RouteTemplate 返回请求命中的 chi 路由模板（如 /{prefix}/{token}），供日志使用。
//
// 日志只写模板、不写原始路径：订阅链接的令牌、Telegram 回调的 secret 都在路径段里，
// 写原始路径等于把秘密写进日志。模板只由注册路由时的字面量与参数名组成，新加的
// 带参数路由不用登记也是安全的。
//
// 路由走完之后（处理函数里、路由内联中间件里）模板已完整，直接取；在根路由器的
// 中间件里（鉴权、租户、超时）路由还没走，模板为空或只有子路由器前缀（/v1/*），这时
// 按方法与路径到路由树里查一次——只在写错误日志时发生，不在热路径上。查不到时退回
// 已有的部分模板，什么都没有就写 UnmatchedRoute。
func RouteTemplate(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return UnmatchedRoute
	}
	partial := rctx.RoutePattern()
	if partial != "" && !strings.HasSuffix(partial, "/*") {
		return partial
	}
	if rctx.Routes != nil {
		path := r.URL.RawPath
		if path == "" {
			path = r.URL.Path
		}
		if full := rctx.Routes.Find(chi.NewRouteContext(), r.Method, path); full != "" {
			return full
		}
	}
	if partial != "" {
		return partial
	}
	return UnmatchedRoute
}
