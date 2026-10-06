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
