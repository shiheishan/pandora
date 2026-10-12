// Package authn 定义已登录的主体及其在 ctx 里的存取。由 auth 模块的鉴权中间件写入，其他模块只读。
package authn

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 两个域的 audience。
const (
	AudiencePublic = "public"
	AudienceAdmin  = "admin"
)

// ReauthWindow 是后台重认证的有效窗口。
const ReauthWindow = 15 * time.Minute

// Principal 是通过鉴权的请求主体。
type Principal struct {
	UserID    uuid.UUID
	SessionID string
	Audience  string
	AMR       []string
	// ReauthAt 是本会话最近一次重认证的时间（只有后台会话会有），零值表示没有。
	ReauthAt time.Time
}

// RecentlyReauthed 报告 now 时是否仍在重认证窗口内。
func (p Principal) RecentlyReauthed(now time.Time) bool {
	return !p.ReauthAt.IsZero() && now.Sub(p.ReauthAt) < ReauthWindow
}

type key struct{}

// With 把主体放进 ctx。
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, key{}, p)
}

// From 取 ctx 里的主体；未登录时 ok 为 false。
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(key{}).(Principal)
	return p, ok
}

// Set 把主体写进 gin 请求的 ctx，供后续中间件与处理器读取。
func Set(c *gin.Context, p Principal) {
	c.Request = c.Request.WithContext(With(c.Request.Context(), p))
}

// FromGin 取 gin 请求里的主体。
func FromGin(c *gin.Context) (Principal, bool) {
	return From(c.Request.Context())
}
