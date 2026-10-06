// [INPUT]: 依赖同包 context.go 的 PrincipalFrom 与 httpx.go 的 Fail / New / CodeUnauthorized
// [OUTPUT]: 对外提供 RequireUser
// [POS]: platform/httpx 的登录检查出口：处理器开头「没有登录用户就回 401 需要登录」的唯一写法，门户各处理器共用；鉴权中间件已挡掉匿名请求，这里是处理器自己的第二道防线
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package httpx

import (
	"log/slog"
	"net/http"
)

// RequireUser 取当前登录用户。主体缺失或不是用户（UserID 为空）时写出
// 401 unauthorized「需要登录」并返回 false，调用方直接 return。
func RequireUser(w http.ResponseWriter, r *http.Request, log *slog.Logger) (*Principal, bool) {
	p := PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		Fail(w, r, log, New(CodeUnauthorized, "需要登录"))
		return nil, false
	}
	return p, true
}
