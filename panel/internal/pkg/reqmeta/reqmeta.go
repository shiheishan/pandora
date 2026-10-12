// Package reqmeta 在 ctx 里携带请求的来源信息（客户端 IP、UA），由 httpx.RequestID 中间件写入，
// 审计、限流、登录记录等从 ctx 读取，不沿调用链逐层传参。
package reqmeta

import "context"

// Meta 是请求来源信息；IP 来自 gin 的 ClientIP（只信 nginx 覆写的 X-Real-IP）。
type Meta struct {
	IP        string
	UserAgent string
}

type key struct{}

// With 把来源信息放进 ctx。
func With(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, key{}, m)
}

// From 取 ctx 里的来源信息；不在 HTTP 请求里（任务、命令行）时为零值。
func From(ctx context.Context) Meta {
	m, _ := ctx.Value(key{}).(Meta)
	return m
}
